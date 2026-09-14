package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// pipeShell builds a shell over a registry holding only the stub commands a
// pipeline test needs, so that a stage's behaviour is the test's own.
func pipeShell(t *testing.T, out *bytes.Buffer, cmds ...command.Command) *Shell {
	t.Helper()
	reg := command.NewRegistry()
	for _, c := range cmds {
		reg.Register(c)
	}
	s := session.New(strings.NewReader(""), out, out)
	sh, err := New(reg, s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	return sh
}

// emitter is a stub command that produces vals as a Stream, one row per value,
// counting the rows it handed over.
func emitter(name string, vals ...string) (command.Command, *atomic.Int64) {
	var sent atomic.Int64
	c := command.Command{
		Name: name, MinArgs: 0, MaxArgs: -1,
		Run: func(_ context.Context, _ *session.Session, _ command.Invocation) (command.Result, error) {
			var i int
			return command.Stream{
				Columns: []command.Column{{Title: "value"}},
				Next: func() (command.Row, bool, error) {
					if i == len(vals) {
						return command.Row{}, false, nil
					}
					v := vals[i]
					i++
					sent.Add(1)
					return command.Row{Cells: []string{v}, JSON: json.RawMessage(v)}, true, nil
				},
			}, nil
		},
	}
	return c, &sent
}

// collector is a stub command that reads its whole pipeline into the slice it
// returns. It is always the last stage, so it runs on the test's goroutine and
// the slice needs no lock.
func collector(name string) (command.Command, *[]string) {
	var got []string
	c := command.Command{
		Name: name, Pipe: command.PipeDocuments, MinArgs: 0, MaxArgs: -1,
		Run: func(ctx context.Context, _ *session.Session, inv command.Invocation) (command.Result, error) {
			for {
				v, ok, err := inv.Pipe.Next(ctx)
				if err != nil || !ok {
					return command.Empty{}, err
				}
				got = append(got, string(v))
			}
		},
	}
	return c, &got
}

func TestPipelineStreamsValuesInOrder(t *testing.T) {
	var out bytes.Buffer
	emit, _ := emitter("emit", `{"id":"a"}`, `{"id":"b"}`, `{"id":"c"}`)
	sink, got := collector("sink")
	sh := pipeShell(t, &out, emit, sink)
	if err := sh.RunLine(context.Background(), "emit | .id | sink"); err != nil {
		t.Fatal(err)
	}
	want := []string{`"a"`, `"b"`, `"c"`}
	if len(*got) != len(want) {
		t.Fatalf("collected %v, want %v", *got, want)
	}
	for i := range want {
		if (*got)[i] != want[i] {
			t.Errorf("value %d = %s, want %s", i, (*got)[i], want[i])
		}
	}
}

func TestPipelineRendersTheLastJQStage(t *testing.T) {
	var out bytes.Buffer
	emit, _ := emitter("emit", `{"name":"alice"}`, `{"name":"bob"}`)
	sh := pipeShell(t, &out, emit)
	if err := sh.RunLine(context.Background(), "emit | .name"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(out.String()); len(got) != 2 || got[0] != `"alice"` || got[1] != `"bob"` {
		t.Errorf("output = %q", out.String())
	}
}

// A stage stops producing once the stage below it stops reading: the bounded
// channel is what keeps a feed with no end from filling memory. The sink reads
// one value and returns, so the emitter can get no further than the one value
// consumed, the stageBuffer values queued, and the one send it is blocked in.
func TestPipelineAppliesBackPressure(t *testing.T) {
	var out bytes.Buffer
	vals := make([]string, 10*stageBuffer)
	for i := range vals {
		vals[i] = `{"id":"x"}`
	}
	emit, sent := emitter("emit", vals...)
	one := command.Command{
		Name: "one", Pipe: command.PipeDocuments, MinArgs: 0, MaxArgs: -1,
		Run: func(ctx context.Context, _ *session.Session, inv command.Invocation) (command.Result, error) {
			if _, _, err := inv.Pipe.Next(ctx); err != nil {
				return nil, err
			}
			return command.Empty{}, nil
		},
	}
	sh := pipeShell(t, &out, emit, one)
	if err := sh.RunLine(context.Background(), "emit | one"); err != nil {
		t.Fatal(err)
	}
	if n := sent.Load(); n > int64(stageBuffer)+2 {
		t.Errorf("the emitter produced %d values with one consumed; the channel bound is %d", n, stageBuffer)
	}
}

func TestPipelineNamesTheFailingStage(t *testing.T) {
	var out bytes.Buffer
	emit, _ := emitter("emit", `{"id":"a"}`)
	boom := command.Command{
		Name: "boom", Pipe: command.PipeDocuments, MinArgs: 0, MaxArgs: -1,
		Run: func(context.Context, *session.Session, command.Invocation) (command.Result, error) {
			return nil, command.Errorf(nil, "the sky fell in")
		},
	}
	sh := pipeShell(t, &out, emit, boom)
	err := sh.RunLine(context.Background(), "emit | boom")
	var se *command.StageError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v (%T), want a *command.StageError", err, err)
	}
	if se.Error() != "stage 2 (boom): the sky fell in" {
		t.Errorf("sentence = %q", se.Error())
	}
}

// A one-command line keeps the sentence it has always printed: the stage
// prefix would be noise on a line with only one stage.
func TestPipelineDoesNotPrefixAOneStageLine(t *testing.T) {
	var out bytes.Buffer
	boom := command.Command{
		Name: "boom", MinArgs: 0, MaxArgs: -1,
		Run: func(context.Context, *session.Session, command.Invocation) (command.Result, error) {
			return nil, command.Errorf(nil, "the sky fell in")
		},
	}
	sh := pipeShell(t, &out, boom)
	err := sh.RunLine(context.Background(), "boom")
	if err == nil || err.Error() != "the sky fell in" {
		t.Errorf("error = %v", err)
	}
}

func TestPipelineRefusesACommandThatReadsNoPipeline(t *testing.T) {
	var out bytes.Buffer
	emit, _ := emitter("emit", `{"id":"a"}`)
	plain := command.Command{
		Name: "plain", MinArgs: 0, MaxArgs: -1,
		Run: func(context.Context, *session.Session, command.Invocation) (command.Result, error) {
			return command.Empty{}, nil
		},
	}
	sh := pipeShell(t, &out, emit, plain)
	err := sh.RunLine(context.Background(), "emit | plain")
	var ue *command.UsageError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "plain does not read a pipeline.") {
		t.Fatalf("error = %v (%T)", err, err)
	}
}

func TestPipelineRefusesARawResultInANonFinalStage(t *testing.T) {
	var out bytes.Buffer
	raw := command.Command{
		Name: "raw", MinArgs: 0, MaxArgs: -1,
		Run: func(context.Context, *session.Session, command.Invocation) (command.Result, error) {
			return command.Raw{Reader: strings.NewReader("bytes"), ContentType: "text/plain", Name: "poster.txt"}, nil
		},
	}
	sh := pipeShell(t, &out, raw)
	err := sh.RunLine(context.Background(), "raw | .")
	if err == nil || !strings.Contains(err.Error(), "a raw result cannot be piped") {
		t.Fatalf("error = %v", err)
	}
}

func TestPipelineCancellationIsNotAFailure(t *testing.T) {
	var out bytes.Buffer
	vals := make([]string, 10*stageBuffer)
	for i := range vals {
		vals[i] = `{"id":"x"}`
	}
	emit, _ := emitter("emit", vals...)
	ctx, cancel := context.WithCancel(context.Background())
	wait := command.Command{
		Name: "wait", Pipe: command.PipeDocuments, MinArgs: 0, MaxArgs: -1,
		Run: func(ctx context.Context, _ *session.Session, inv command.Invocation) (command.Result, error) {
			if _, _, err := inv.Pipe.Next(ctx); err != nil {
				return nil, err
			}
			cancel()
			_, _, err := inv.Pipe.Next(ctx)
			return nil, err
		},
	}
	sh := pipeShell(t, &out, emit, wait)
	err := sh.RunLine(ctx, "emit | wait")
	cancel()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// --yes on the first stage is the line's --yes: it reaches every command stage
// and is gone again once the line is done.
func TestPipelineReadsPreferencesFromTheFirstStage(t *testing.T) {
	var out bytes.Buffer
	emit, _ := emitter("emit", `{"id":"a"}`)
	var sawYes bool
	sink := command.Command{
		Name: "sink", Pipe: command.PipeDocuments, MinArgs: 0, MaxArgs: -1,
		Run: func(ctx context.Context, s *session.Session, inv command.Invocation) (command.Result, error) {
			sawYes = s.Prefs.Yes
			for {
				if _, ok, err := inv.Pipe.Next(ctx); err != nil || !ok {
					return command.Empty{}, err
				}
			}
		},
	}
	sh := pipeShell(t, &out, emit, sink)
	if err := sh.RunLine(context.Background(), "emit --yes | sink"); err != nil {
		t.Fatal(err)
	}
	if !sawYes {
		t.Error("--yes on stage 1 did not reach stage 2")
	}
	if sh.sess.Prefs.Yes {
		t.Error("--yes leaked past the line")
	}
}

// A command that hands back neither a result nor an error has a bug in it, and
// a line that printed nothing and reported nothing would hide it.
func TestPipelineReportsAStageThatReturnedNothing(t *testing.T) {
	var out bytes.Buffer
	emit, _ := emitter("emit", `{"id":"a"}`)
	silent := command.Command{
		Name: "silent", Pipe: command.PipeDocuments, MinArgs: 0, MaxArgs: -1,
		Run: func(ctx context.Context, _ *session.Session, inv command.Invocation) (command.Result, error) {
			for {
				if _, ok, err := inv.Pipe.Next(ctx); err != nil || !ok {
					return nil, nil
				}
			}
		},
	}
	sh := pipeShell(t, &out, emit, silent)
	err := sh.RunLine(context.Background(), "emit | silent")
	if err == nil || err.Error() != "stage 2 (silent): silent returned no result" {
		t.Fatalf("error = %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("output = %q", out.String())
	}
}

// The same stage on a cancelled line reports the cancellation, not the missing
// result: it stopped early because the line was cancelled.
func TestPipelineCancellationOutranksAMissingResult(t *testing.T) {
	var out bytes.Buffer
	emit, _ := emitter("emit", `{"id":"a"}`)
	ctx, cancel := context.WithCancel(context.Background())
	silent := command.Command{
		Name: "silent", Pipe: command.PipeDocuments, MinArgs: 0, MaxArgs: -1,
		Run: func(context.Context, *session.Session, command.Invocation) (command.Result, error) {
			cancel()
			return nil, nil
		},
	}
	sh := pipeShell(t, &out, emit, silent)
	err := sh.RunLine(ctx, "emit | silent")
	cancel()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// Stage 1 is numbered like every other stage: an unknown command there reads
// the same way as an unknown command further down the line.
func TestPipelinePrefixesAFailureInStageOne(t *testing.T) {
	var out bytes.Buffer
	sh := pipeShell(t, &out)
	err := sh.RunLine(context.Background(), "nosuch | .id")
	var se *command.StageError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v (%T), want a *command.StageError", err, err)
	}
	if se.Stage != 1 || se.Name != "nosuch" || !strings.HasPrefix(se.Text, "stage 1 (nosuch): ") {
		t.Errorf("error = %q", se.Text)
	}
	var ue *command.UsageError
	if !errors.As(err, &ue) {
		t.Errorf("the cause is no longer a *command.UsageError, so the line loses exit code 2")
	}
}

// A command that changes the session cannot head a line whose other stages run
// beside it, because they share the one session.
func TestPipelineRefusesASessionCommandAtTheHead(t *testing.T) {
	var out bytes.Buffer
	var ran int
	cd := command.Command{
		Name: "cd", MinArgs: 0, MaxArgs: -1,
		Run: func(context.Context, *session.Session, command.Invocation) (command.Result, error) {
			ran++
			return command.Message{Text: "/movies"}, nil
		},
	}
	sh := pipeShell(t, &out, cd)
	err := sh.RunLine(context.Background(), "cd /movies | .id")
	var ue *command.UsageError
	if !errors.As(err, &ue) || err.Error() != "cd cannot start a pipeline." {
		t.Fatalf("error = %v (%T)", err, err)
	}
	if ran != 0 {
		t.Error("the refused command ran anyway")
	}
	// The same command on its own is untouched.
	if err := sh.RunLine(context.Background(), "cd /movies"); err != nil || ran != 1 {
		t.Fatalf("error = %v, ran = %d", err, ran)
	}
}

func TestStreamResultSkipsADocumentWithNoJSON(t *testing.T) {
	var got []json.RawMessage
	emit := func(v json.RawMessage) error {
		got = append(got, v)
		return nil
	}
	if err := streamResult(context.Background(), command.Document{}, emit); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("emitted %#v; a document with no JSON has nothing to pipe", got)
	}
}
