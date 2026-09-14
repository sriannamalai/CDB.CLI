package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/render"
)

// stageBuffer is how many values may sit between two stages before the stage
// above has to wait. It is what turns a pipeline into a stream rather than a
// sequence of whole result sets: a feed with no end fills 64 values and then
// runs at the speed of the stage below it.
const stageBuffer = 64

// stageCommand is one command stage, looked up and with its flags parsed.
type stageCommand struct {
	cmd  command.Command
	fs   *pflag.FlagSet
	args []string
}

// stageName is what a stage is called in an error sentence.
func stageName(st Stage) string {
	if st.Argv != nil {
		return st.Argv[0]
	}
	return "jq"
}

// failure holds the first stage failure of a line. Several stages can fail at
// once — one of them cancels the others — and the operator is owed the failure
// that started it, not whichever goroutine was scheduled next.
type failure struct {
	mu    sync.Mutex
	first error
}

func (f *failure) record(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.first == nil {
		f.first = err
	}
}

func (f *failure) result() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.first
}

// liveness is what the executor learns about the stages above the last one:
// whether any of them produced a live result, a feed with no end such as
// "tail --follow". Every producer reports once, before it sends its first
// value, so the last stage can wait for the answer without deadlocking — no
// stage has to read a value to know what its own result is.
type liveness struct {
	pending sync.WaitGroup
	live    atomic.Bool
}

// report records one producer's answer and marks it in.
func (l *liveness) report(live bool) {
	if live {
		l.live.Store(true)
	}
}

// wait blocks until every producer has reported and answers whether any of
// them was live. A line with one stage has no producers and answers false at
// once.
func (l *liveness) wait() bool {
	l.pending.Wait()
	return l.live.Load()
}

// isLive reports whether a result is a stream that must be written as it
// arrives.
func isLive(res command.Result) bool {
	st, ok := res.(command.Stream)
	return ok && st.Live
}

// runPipeline runs a line and renders its last stage.
func (sh *Shell) runPipeline(ctx context.Context, line Line) error {
	return sh.execute(ctx, line, nil)
}

// Capture runs one line and returns the values its last stage produced instead
// of rendering them. It is what "set <name> = <pipeline>" uses.
func (sh *Shell) Capture(ctx context.Context, input string) ([]json.RawMessage, error) {
	line, err := Parse(input, sh.isCommand)
	if err != nil {
		return nil, err
	}
	var vals []json.RawMessage
	if err := sh.execute(ctx, line, &vals); err != nil {
		return nil, err
	}
	return vals, nil
}

// execute runs every stage of a parsed line. With into non-nil the last
// stage's values are collected there instead of rendered.
func (sh *Shell) execute(ctx context.Context, line Line, into *[]json.RawMessage) error {
	if len(line.Stages) == 0 {
		return nil
	}
	// A command that changes the session cannot head a line whose other stages
	// run beside it; the check comes before anything is prepared or opened.
	if err := sh.checkPipelineHead(line); err != nil {
		return err
	}
	restore, forceJSON, verbose, err := sh.applyLinePrefs(line.Stages[0])
	if err != nil {
		// Stage 1 fails before any stage is numbered, and an unknown command
		// or a misspelled flag there is as much a stage failure as the same
		// mistake in stage 2: the operator is owed the same "stage 1 (…)".
		return stageError(0, len(line.Stages), line.Stages[0], err, verbose)
	}
	defer restore()
	// Every command stage runs in its own goroutine and a session is not safe
	// for concurrent use, so the one mutation a stage would make on its own —
	// the lazy auto-connect — happens once, here, before any stage starts.
	if err := sh.connectFor(ctx, line); err != nil {
		return err
	}

	// parent is kept so that a cancellation can be told apart from the cancel
	// below: the operator's Ctrl-C is the line's outcome, the shutdown cancel
	// of a line that is already done is not.
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		fail    failure
		wg      sync.WaitGroup
		sources liveness
		in      <-chan json.RawMessage
	)
	total := len(line.Stages)
	for i := 0; i < total-1; i++ {
		out := make(chan json.RawMessage, stageBuffer)
		src, dst, n := in, out, i
		wg.Add(1)
		sources.pending.Add(1)
		go func() {
			defer wg.Done()
			defer close(dst)
			// A stage that fails before it knows its own result still has to
			// report, or the last stage would wait on an answer that is never
			// coming.
			reported := sync.OnceFunc(sources.pending.Done)
			defer reported()
			if err := sh.feedStage(ctx, line.Stages[n], src, dst, func(live bool) {
				sources.report(live)
				reported()
			}); err != nil {
				fail.record(stageError(n, total, line.Stages[n], err, verbose))
				cancel()
			}
		}()
		in = out
	}
	last := total - 1
	lastErr := sh.finishStage(ctx, line.Stages[last], in, forceJSON, into, sources.wait)
	if lastErr != nil {
		fail.record(stageError(last, total, line.Stages[last], lastErr, verbose))
	}
	// Unblock any stage still waiting on a send now that nothing will read it:
	// the last stage can stop before its source runs dry, which is what makes
	// "changes --follow | head" end at all.
	cancel()
	wg.Wait()
	err = fail.result()
	// That shutdown is what those stages then report, and it is not a failure
	// of a line whose last stage finished: only a cancellation the caller
	// asked for is.
	if lastErr == nil && parent.Err() == nil && errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// stageError names the stage a line failed in. A line with one stage keeps the
// sentence it has always printed. A cancelled context is not a failure the
// operator needs located: it is Ctrl-C.
func stageError(n, total int, st Stage, err error, verbose bool) error {
	if err == nil || total < 2 || errors.Is(err, context.Canceled) {
		return err
	}
	name := stageName(st)
	return &command.StageError{
		Stage: n + 1,
		Name:  name,
		Text:  fmt.Sprintf("stage %d (%s): %s", n+1, name, render.ErrorMessage(err, verbose)),
		Err:   err,
	}
}

// prepare expands the stage's variables, looks the command up and parses its
// flags. Expansion comes first so that a flag or a path that arrived in a
// variable is the one pflag and the command see.
func (sh *Shell) prepare(st Stage) (stageCommand, error) {
	argv, err := expandStage(st, sh.sess.Vars, sh.args)
	if err != nil {
		return stageCommand{}, err
	}
	name := argv[0]
	c, ok := sh.reg.Lookup(name)
	if !ok {
		return stageCommand{}, command.Usagef(name, "unknown command. Type \"help\" to see the command list.")
	}
	fs := sh.reg.NewFlagSet(c)
	if err := fs.Parse(argv[1:]); err != nil {
		return stageCommand{}, command.Usagef(c.Name, "%v\nusage: %s %s", err, c.Name, c.Usage)
	}
	args := fs.Args()
	if err := c.CheckArgsErr(args); err != nil {
		return stageCommand{}, err
	}
	return stageCommand{cmd: c, fs: fs, args: args}, nil
}

// applyLinePrefs reads the per-line preferences off stage 1 and puts them on
// the session for the length of the line: --yes, --verbose, --anonymous and
// --replication-url apply to every command stage of this line and are gone
// afterwards. (--json is not a preference; it reaches the renderer as
// forceJSON.) Stage 1 is parsed here and again when it runs, which costs one
// pflag pass and keeps the two paths from drifting apart.
func (sh *Shell) applyLinePrefs(first Stage) (restore func(), forceJSON, verbose bool, err error) {
	restore = func() {}
	if first.Argv == nil {
		return restore, false, sh.sess.Prefs.Verbose, nil
	}
	sc, err := sh.prepare(first)
	if err != nil {
		return restore, false, sh.sess.Prefs.Verbose, err
	}
	p := &sh.sess.Prefs
	prevYes, prevVerbose := p.Yes, p.Verbose
	prevAnon, prevReplication := p.Anonymous, p.ReplicationURL
	restore = func() {
		p.Yes, p.Verbose = prevYes, prevVerbose
		p.Anonymous, p.ReplicationURL = prevAnon, prevReplication
	}
	if v, ferr := sc.fs.GetBool("yes"); ferr == nil && v {
		p.Yes = true
	}
	if v, ferr := sc.fs.GetBool("verbose"); ferr == nil && v {
		p.Verbose = true
	}
	// --anonymous has to be on the session before the auto-connect below, and
	// before "connect" runs: openProfile is what acts on it.
	if v, ferr := sc.fs.GetBool("anonymous"); ferr == nil && v {
		p.Anonymous = true
	}
	if v, ferr := sc.fs.GetString("replication-url"); ferr == nil && v != "" {
		p.ReplicationURL = v
	}
	forceJSON, _ = sc.fs.GetBool("json")
	return restore, forceJSON, p.Verbose, nil
}

// sessionCommands are the commands that change the session every stage of a
// line shares: the connection, the profile, the working directory, the
// variables. A *session.Session is not safe for concurrent use
// (internal/session/session.go:66-67), and a stage that mutates one while the
// stages below it read it is a race no test would reliably catch, so such a
// command may head a line only when it is the whole line.
//
// help, history and clear are not among them: they read the registry, the
// history and the terminal, and change nothing any other stage looks at, so
// "history | .[]" is a line like any other.
var sessionCommands = map[string]struct{}{
	"connect":  {},
	"profiles": {},
	"cd":       {},
	"exit":     {},
	"run":      {},
	"set":      {},
	"unset":    {},
}

// checkPipelineHead refuses a session command at the head of a line with more
// stages after it. A line of one stage is unaffected: "cd /movies" and
// "connect prod" are the same commands they have always been.
func (sh *Shell) checkPipelineHead(line Line) error {
	if len(line.Stages) < 2 || line.Stages[0].Argv == nil {
		return nil
	}
	// The name is resolved through the registry first, so that an alias for a
	// session command is refused by the name the set is written in.
	name := line.Stages[0].Argv[0]
	if c, ok := sh.reg.Lookup(name); ok {
		name = c.Name
	}
	if _, ok := sessionCommands[name]; ok {
		return command.Usagef("", "%s cannot start a pipeline.", name)
	}
	return nil
}

// connectFor opens the connection once when any command stage needs one.
func (sh *Shell) connectFor(ctx context.Context, line Line) error {
	if sh.sess.Connected() {
		return nil
	}
	for _, st := range line.Stages {
		if st.Argv == nil {
			continue
		}
		c, ok := sh.reg.Lookup(st.Argv[0])
		if ok && c.NeedsClient {
			return command.Open(ctx, sh.sess, "")
		}
	}
	return nil
}

// feedStage runs one stage that is not the last, writing every value it
// produces to dst. report is called once, before the first value is sent, with
// whether this stage is a live source — a feed with no end — so that the last
// stage can decide how to render without waiting for a feed that may never
// end.
//
// Only a stage that reads no pipeline can be that source. put, rm and cat
// answer with a live stream wherever they sit, because the stage above them
// may be a feed; taken as a source of their own they would make "ls | cat |
// .id" live, and a line with an end would never be paged.
func (sh *Shell) feedStage(ctx context.Context, st Stage, src <-chan json.RawMessage, dst chan<- json.RawMessage, report func(live bool)) error {
	if st.Argv == nil {
		f, err := compileFilter(st.Expr, sh.bindings())
		if err != nil {
			return err
		}
		// A jq stage is never a source of its own: it is live exactly when
		// the source above it is, which that stage has already said.
		report(false)
		for {
			in, ok, err := receive(ctx, src)
			if err != nil || !ok {
				return err
			}
			vals, err := f.apply(in)
			if err != nil {
				return err
			}
			for _, v := range vals {
				if err := send(ctx, dst, v); err != nil {
					return err
				}
			}
		}
	}
	res, err := sh.runCommandStage(ctx, st, src)
	if err != nil {
		return err
	}
	report(src == nil && isLive(res))
	return streamResult(ctx, res, func(v json.RawMessage) error { return send(ctx, dst, v) })
}

// lastResult builds the result the last stage hands over: a jq stage as a
// stream of the values its filter produces, a command stage as the result the
// command returned.
//
// Either is live only when some stage above it was live. A line whose source
// is finite — "find … | .id", "ls | cat | put /copies" — is an ordinary
// result, and a terminal pages it the way it pages "find" on its own; only a
// feed with no end keeps the unbuffered rendering a feed needs. sources is
// what the executor learned from the stages above, and it is not asked when
// there are none.
func (sh *Shell) lastResult(ctx context.Context, st Stage, src <-chan json.RawMessage, sources func() bool) (command.Result, error) {
	if st.Argv == nil {
		f, err := compileFilter(st.Expr, sh.bindings())
		if err != nil {
			return nil, err
		}
		return filterStream(ctx, f, src, sources()), nil
	}
	res, err := sh.runCommandStage(ctx, st, src)
	if err != nil {
		return nil, err
	}
	if res == nil {
		// A stage that hands back neither a result nor an error has nothing to
		// render. On a cancelled line that is the cancellation showing up as a
		// stage that stopped early, and the line failed of that; anywhere else
		// it is a command with a bug in it, which is worth a sentence rather
		// than a line that quietly printed nothing.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s returned no result", stageName(st))
	}
	// put, rm and cat answer with a live stream because the stage above them
	// may be a feed; when it is not, their rows are a finite result like any
	// other.
	if src != nil && isLive(res) && !sources() {
		s := res.(command.Stream)
		s.Live = false
		return s, nil
	}
	return res, nil
}

// finishStage runs the last stage. With into nil it renders: a command stage
// the way a one-command line always has, a jq stage as single-column rows with
// JSON forced, which is what the one filter stage did before there were
// pipelines. With into non-nil the values the stage produced are collected
// there instead, which is what a capture wants.
func (sh *Shell) finishStage(ctx context.Context, st Stage, src <-chan json.RawMessage, forceJSON bool, into *[]json.RawMessage, sources func() bool) error {
	res, err := sh.lastResult(ctx, st, src, sources)
	if err != nil {
		return err
	}
	if st.Argv == nil {
		forceJSON = true
	}
	if into != nil {
		return streamResult(ctx, res, func(v json.RawMessage) error {
			*into = append(*into, v)
			return nil
		})
	}
	return render.New(sh.sess.Stdout, render.OptionsFor(sh.sess.Prefs, sh.sess.Stdout, forceJSON)).Render(res)
}

// runCommandStage runs one command stage. src is nil for stage 1; a command
// reached in a later stage that reads no pipeline is a usage error rather than
// a command that silently drops everything above it.
func (sh *Shell) runCommandStage(ctx context.Context, st Stage, src <-chan json.RawMessage) (command.Result, error) {
	sc, err := sh.prepare(st)
	if err != nil {
		return nil, err
	}
	inv := command.Invocation{
		Args:    sc.args,
		Flags:   sc.fs,
		Stdin:   sh.sess.Stdin(),
		Stdout:  sh.sess.Stdout,
		Stderr:  sh.sess.Stderr,
		Shell:   true,
		Capture: st.Capture,
	}
	if src != nil {
		if sc.cmd.Pipe == command.PipeNone {
			return nil, command.NoPipeError(sc.cmd.Name)
		}
		inv.Pipe = command.NewPipe(src)
	}
	return sc.cmd.Run(ctx, sh.sess, inv)
}

// filterStream is a jq stage as a stream of the values it produces: one input
// value in, and none, one or several out, each written as it is made. It is
// live when the source above it is — a stream collected before printing shows
// a change feed nothing — and finite otherwise, so that a result with an end
// is paged.
func filterStream(ctx context.Context, f *filter, src <-chan json.RawMessage, live bool) command.Result {
	// One input can produce several values, and "select(…)" produces none, so
	// the values of one input are held until they have all been handed out and
	// only then is the next one read.
	var pending []json.RawMessage
	return command.Stream{
		Live:    live,
		Columns: []command.Column{{Title: "value"}},
		Next: func() (command.Row, bool, error) {
			for len(pending) == 0 {
				in, ok, err := receive(ctx, src)
				if err != nil || !ok {
					return command.Row{}, false, err
				}
				if pending, err = f.apply(in); err != nil {
					return command.Row{}, false, err
				}
			}
			v := pending[0]
			pending = pending[1:]
			return command.Row{Cells: []string{string(v)}, JSON: v}, true, nil
		},
	}
}

// streamResult hands the JSON side of a result to emit as it becomes
// available: a Stream is forwarded row by row rather than collected, so a live
// source keeps whatever is below it fed.
func streamResult(ctx context.Context, res command.Result, emit func(json.RawMessage) error) error {
	switch v := res.(type) {
	case command.Document:
		// A document with no JSON has nothing to hand on, and emitting the nil
		// would put an empty value in a capture's array.
		if v.JSON == nil {
			return nil
		}
		return emit(v.JSON)
	case command.Rows:
		for _, item := range v.Items {
			if item.JSON == nil {
				continue
			}
			if err := emit(item.JSON); err != nil {
				return err
			}
		}
		return nil
	case command.Stream:
		for {
			row, ok, err := v.Next()
			if err != nil || !ok {
				return err
			}
			if row.JSON == nil {
				continue
			}
			if err := emit(row.JSON); err != nil {
				return err
			}
		}
	case command.Message:
		b, err := json.Marshal(v.Text)
		if err != nil {
			return err
		}
		return emit(b)
	case command.Empty:
		return nil
	default:
		return fmt.Errorf("a %s result cannot be piped", res.ResultKind())
	}
}

// receive reads one value from the stage above, or reports the line's
// cancellation.
func receive(ctx context.Context, src <-chan json.RawMessage) (json.RawMessage, bool, error) {
	select {
	case v, ok := <-src:
		if !ok {
			return nil, false, nil
		}
		return v, true, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

// send writes one value to the stage below, or reports the line's
// cancellation. Every write goes through it, so a failed stage's cancel
// unblocks a stage that is waiting on a full channel and no drain is needed.
func send(ctx context.Context, dst chan<- json.RawMessage, v json.RawMessage) error {
	select {
	case dst <- v:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// bindings are the jq variables every filter stage of this line is compiled
// with: the session's variables, bound by name with their stored values, so
// that "select(.year > $year)" works and jq's own "$" syntax is intact.
func (sh *Shell) bindings() map[string]any {
	if sh.sess.Vars == nil {
		return nil
	}
	return sh.sess.Vars.All()
}
