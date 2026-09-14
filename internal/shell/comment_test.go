package shell

import (
	"errors"
	"testing"
)

// knownCommand is the classifier these cases parse with: only "ls" and "cat"
// name a command, so ".id" is a jq stage.
func knownCommand(name string) bool { return name == "ls" || name == "cat" }

// Issue #53.3: a comment ends the line, so a "|" with nothing but a comment
// after it is a line that ends with "|" rather than a jq stage holding the
// comment text.
func TestACommentAfterAPipeEndsTheLine(t *testing.T) {
	for _, in := range []string{"ls | # note", "ls |# note", "ls |\t# note"} {
		if _, err := Parse(in, knownCommand); !errors.Is(err, ErrTrailingPipe) {
			t.Errorf("Parse(%q) = %v, want %v", in, err, ErrTrailingPipe)
		}
	}
}

// A comment after a stage that has words of its own ends that stage and no
// more: the stage is what was typed before the "#".
func TestACommentEndsTheStageItIsIn(t *testing.T) {
	line, err := Parse("ls | .id # the id", knownCommand)
	if err != nil {
		t.Fatal(err)
	}
	if len(line.Stages) != 2 {
		t.Fatalf("Stages = %#v, want 2", line.Stages)
	}
	if line.Stages[1].Expr != ".id" {
		t.Errorf("Stages[1].Expr = %q, want %q", line.Stages[1].Expr, ".id")
	}
}

// A line of nothing but a comment is still the no-op it has always been.
func TestACommentedLineIsANoOp(t *testing.T) {
	line, err := Parse("# just a note", knownCommand)
	if err != nil {
		t.Fatal(err)
	}
	if len(line.Stages) != 0 {
		t.Errorf("Stages = %#v, want none", line.Stages)
	}
}
