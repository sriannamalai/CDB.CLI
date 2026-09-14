package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// liveEmitter is a stub command whose result is a live stream, the way "tail
// --follow" is: a feed with no end, which is the one thing that must never be
// collected before it is printed.
func liveEmitter(name string, vals ...string) command.Command {
	return command.Command{
		Name: name, MinArgs: 0, MaxArgs: -1,
		Run: func(_ context.Context, _ *session.Session, _ command.Invocation) (command.Result, error) {
			var i int
			return command.Stream{
				Live:    true,
				Columns: []command.Column{{Title: "value"}},
				Next: func() (command.Row, bool, error) {
					if i == len(vals) {
						return command.Row{}, false, nil
					}
					v := vals[i]
					i++
					return command.Row{Cells: []string{v}, JSON: json.RawMessage(v)}, true, nil
				},
			}, nil
		},
	}
}

// liveConsumer is a stub command that reads a pipeline and answers with a live
// stream, the way put, rm and cat do.
func liveConsumer(name string) command.Command {
	return command.Command{
		Name: name, Pipe: command.PipeDocuments, MinArgs: 0, MaxArgs: -1,
		Run: func(ctx context.Context, _ *session.Session, inv command.Invocation) (command.Result, error) {
			return command.Stream{
				Live:    true,
				Columns: []command.Column{{Title: "value"}},
				Next: func() (command.Row, bool, error) {
					v, ok, err := inv.Pipe.Next(ctx)
					if err != nil || !ok {
						return command.Row{}, false, err
					}
					return command.Row{Cells: []string{string(v)}, JSON: v}, true, nil
				},
			}, nil
		},
	}
}

// closedSource is a pipeline channel holding vals and nothing more.
func closedSource(vals ...string) <-chan json.RawMessage {
	src := make(chan json.RawMessage, len(vals))
	for _, v := range vals {
		src <- json.RawMessage(v)
	}
	close(src)
	return src
}

// Issue #52: a jq last stage over a finite source is an ordinary result, so a
// terminal pages it the way it pages "find" on its own.
func TestALastJQStageOverAFiniteSourceIsNotLive(t *testing.T) {
	var out bytes.Buffer
	sh := pipeShell(t, &out)
	res, err := sh.lastResult(context.Background(), Stage{Expr: ".id"}, closedSource(`{"id":"a"}`), func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	st, ok := res.(command.Stream)
	if !ok {
		t.Fatalf("result = %T, want command.Stream", res)
	}
	if st.Live {
		t.Error("Live = true, want false: a finite source is paged")
	}
}

// The same stage over a live source stays live: a change feed shows nothing
// when its rows are held back.
func TestALastJQStageOverALiveSourceStaysLive(t *testing.T) {
	var out bytes.Buffer
	sh := pipeShell(t, &out)
	res, err := sh.lastResult(context.Background(), Stage{Expr: ".id"}, closedSource(`{"id":"a"}`), func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if st, ok := res.(command.Stream); !ok || !st.Live {
		t.Errorf("result = %#v, want a live command.Stream", res)
	}
}

// A consuming last stage — put, rm, cat — answers with a live stream of its
// own. Over a finite source that stream is paged too.
func TestAConsumingLastStageOverAFiniteSourceIsNotLive(t *testing.T) {
	var out bytes.Buffer
	sh := pipeShell(t, &out, liveConsumer("sink"))
	st := Stage{Argv: []string{"sink"}}
	res, err := sh.lastResult(context.Background(), st, closedSource(`{"id":"a"}`), func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	s, ok := res.(command.Stream)
	if !ok {
		t.Fatalf("result = %T, want command.Stream", res)
	}
	if s.Live {
		t.Error("Live = true, want false: a finite source is paged")
	}
}

func TestAConsumingLastStageOverALiveSourceStaysLive(t *testing.T) {
	var out bytes.Buffer
	sh := pipeShell(t, &out, liveConsumer("sink"))
	st := Stage{Argv: []string{"sink"}}
	res, err := sh.lastResult(context.Background(), st, closedSource(`{"id":"a"}`), func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := res.(command.Stream); !ok || !s.Live {
		t.Errorf("result = %#v, want a live command.Stream", res)
	}
}

// A one-command line is untouched: "tail --follow" on its own is still live,
// because there is no stage above it to be finite.
func TestAOneCommandLineKeepsItsOwnLiveness(t *testing.T) {
	var out bytes.Buffer
	sh := pipeShell(t, &out, liveEmitter("feed", `{"id":"a"}`))
	st := Stage{Argv: []string{"feed"}}
	res, err := sh.lastResult(context.Background(), st, nil, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := res.(command.Stream); !ok || !s.Live {
		t.Errorf("result = %#v, want a live command.Stream", res)
	}
}

// The executor learns a stage's liveness from the result the stage produced,
// and it learns it before the stage sends anything, which is what lets the
// last stage wait for the answer.
func TestFeedStageReportsWhetherItsResultIsLive(t *testing.T) {
	var out bytes.Buffer
	emit, _ := emitter("emit", `{"id":"a"}`)
	sh := pipeShell(t, &out, emit, liveEmitter("feed", `{"id":"a"}`))
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"emit", false},
		{"feed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := make(chan json.RawMessage, stageBuffer)
			var got, reported bool
			st := Stage{Argv: []string{tc.name}}
			err := sh.feedStage(context.Background(), st, nil, dst, func(live bool) {
				got, reported = live, true
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reported {
				t.Fatal("the stage reported no liveness")
			}
			if got != tc.want {
				t.Errorf("reported %v, want %v", got, tc.want)
			}
		})
	}
}

// A jq stage is never itself a live source: it is live only when something
// above it is.
func TestAJQFeedStageReportsNotLive(t *testing.T) {
	var out bytes.Buffer
	sh := pipeShell(t, &out)
	dst := make(chan json.RawMessage, stageBuffer)
	var got, reported bool
	err := sh.feedStage(context.Background(), Stage{Expr: ".id"}, closedSource(`{"id":"a"}`), dst, func(live bool) {
		got, reported = live, true
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reported || got {
		t.Errorf("reported %v (reported=%v), want false", got, reported)
	}
}

// A middle put, rm or cat answers with a live stream by design, because the
// stage above it may be a feed. That is not a reason to call the line live:
// "ls | cat | .id" has an end, and is paged.
func TestAMiddleConsumerIsNotALiveSource(t *testing.T) {
	var out bytes.Buffer
	sh := pipeShell(t, &out, liveConsumer("cat"))
	dst := make(chan json.RawMessage, stageBuffer)
	var got, reported bool
	st := Stage{Argv: []string{"cat"}}
	err := sh.feedStage(context.Background(), st, closedSource(`{"id":"a"}`), dst, func(live bool) {
		got, reported = live, true
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reported || got {
		t.Errorf("reported %v (reported=%v), want false: only a source stage is live", got, reported)
	}
}

// The three-stage shapes the rule is for, run the way execute runs them: every
// producer reporting into one liveness, the last stage asking it. A finite
// source leaves the last stage paged however many consumers sit between it and
// the end; a feed keeps it live.
func TestAThreeStageLineTakesItsLivenessFromTheSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want bool
	}{
		// "ls | cat | .id" and "tail --follow | cat | .id" in stub form.
		{"finite", "emit | cat | .id", false},
		{"live", "feed | cat | .id", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			emit, _ := emitter("emit", `{"id":"a"}`)
			sh := pipeShell(t, &out, emit, liveEmitter("feed", `{"id":"a"}`), liveConsumer("cat"))
			if got := lastStageLiveness(t, sh, tc.line); got != tc.want {
				t.Errorf("the last stage of %q is live = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

// lastStageLiveness runs every stage of a line but the last exactly as execute
// does — each producer reporting once into one liveness — and answers whether
// the last stage would be rendered live.
func lastStageLiveness(t *testing.T, sh *Shell, input string) bool {
	t.Helper()
	line, err := Parse(input, sh.isCommand)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var (
		sources liveness
		wg      sync.WaitGroup
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
			reported := sync.OnceFunc(sources.pending.Done)
			defer reported()
			_ = sh.feedStage(ctx, line.Stages[n], src, dst, func(live bool) {
				sources.report(live)
				reported()
			})
		}()
		in = out
	}
	res, err := sh.lastResult(ctx, line.Stages[total-1], in, sources.wait)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	wg.Wait()
	return isLive(res)
}
