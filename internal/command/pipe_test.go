package command

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestParseReferenceAcceptsEveryShape(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     string
		wantDB string
		wantID string
	}{
		{"bare id", `"tt0211915"`, "", "tt0211915"},
		{"absolute path", `"/movies/tt0211915"`, "movies", "tt0211915"},
		{"object with _id", `{"_id":"tt0211915","_rev":"1-a"}`, "", "tt0211915"},
		{"object with id", `{"id":"tt0211915","key":"tt0211915"}`, "", "tt0211915"},
		{"object _id wins", `{"_id":"a","id":"b"}`, "", "a"},
		{"object with an absolute id", `{"id":"/movies/tt0211915"}`, "movies", "tt0211915"},
		{"design document id", `"_design/app"`, "", "_design/app"},
		{"absolute design document", `"/movies/_design/app"`, "movies", "_design/app"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, id, err := ParseReference(json.RawMessage(tc.in))
			if err != nil {
				t.Fatalf("ParseReference(%s) = %v", tc.in, err)
			}
			if db != tc.wantDB || id != tc.wantID {
				t.Errorf("ParseReference(%s) = %q,%q; want %q,%q", tc.in, db, id, tc.wantDB, tc.wantID)
			}
		})
	}
}

func TestParseReferenceRejectsEverythingElse(t *testing.T) {
	for _, in := range []string{`3`, `null`, `true`, `["a"]`, `{"name":"alice"}`, `{"_id":7}`, `""`, `"/movies"`} {
		if _, _, err := ParseReference(json.RawMessage(in)); !errors.Is(err, ErrNotReference) {
			t.Errorf("ParseReference(%s) error = %v, want ErrNotReference", in, err)
		}
	}
}

func TestNoPipeErrorNamesTheCommandOnce(t *testing.T) {
	err := NoPipeError("mkdir")
	if err.Error() != "mkdir does not read a pipeline." {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestPipeHandsValuesOverInOrder(t *testing.T) {
	src := make(chan json.RawMessage, 2)
	src <- json.RawMessage(`1`)
	src <- json.RawMessage(`2`)
	close(src)
	p := NewPipe(src)
	var got []string
	for {
		v, ok, err := p.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		got = append(got, string(v))
	}
	if len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Errorf("values = %v", got)
	}
}

// TryNext is what lets a consumer send a short batch instead of holding a live
// feed's one change back until ninety-nine more arrive.
func TestPipeTryNextDoesNotWait(t *testing.T) {
	src := make(chan json.RawMessage, 1)
	p := NewPipe(src)
	if _, _, waiting := p.TryNext(); waiting {
		t.Fatal("TryNext read from an empty pipeline")
	}
	src <- json.RawMessage(`1`)
	v, ok, waiting := p.TryNext()
	if !waiting || !ok || string(v) != "1" {
		t.Fatalf("TryNext = %s,%v,%v", v, ok, waiting)
	}
	close(src)
	if _, ok, waiting := p.TryNext(); !waiting || ok {
		t.Fatalf("a closed pipeline must report waiting=true, ok=false; got %v,%v", ok, waiting)
	}
}

func TestPipeNextReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := NewPipe(make(chan json.RawMessage))
	if _, _, err := p.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestPipeKindStrings(t *testing.T) {
	if PipeNone.String() != "" || PipeDocuments.String() != "documents" || PipeReferences.String() != "references" {
		t.Errorf("kinds = %q,%q,%q", PipeNone, PipeDocuments, PipeReferences)
	}
}
