package shell

import (
	"bytes"
	"context"
	"encoding/json"
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
	st := Stage{Argv: []string{"sink"}, Literal: []bool{false}}
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
	st := Stage{Argv: []string{"sink"}, Literal: []bool{false}}
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
	st := Stage{Argv: []string{"feed"}, Literal: []bool{false}}
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
			st := Stage{Argv: []string{tc.name}, Literal: []bool{false}}
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
