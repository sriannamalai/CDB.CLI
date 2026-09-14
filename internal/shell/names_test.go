package shell

import (
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Issue #53.2: a positional name is one digit. "$1abc" is argument 1 followed
// by "abc", and "$12" is argument 1 followed by "2" — a variable name cannot
// begin with a digit, so there is nothing else either could mean.
func TestAPositionalNameIsOneDigit(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"put $1abc", "oneabc"},
		{"put $12", "one2"},
		{"put ${1}2", "one2"},
	} {
		got, err := expandLine(t, tc.in, session.NewVars(), []string{"one"})
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if got[1] != tc.want {
			t.Errorf("%s: argv[1] = %q, want %q", tc.in, got[1], tc.want)
		}
	}
}

// A braced reference holding something that is not a name says so, rather than
// reporting a variable nobody could have set.
func TestABracedReferenceMustHoldAName(t *testing.T) {
	_, err := expandLine(t, "put ${a-b}", session.NewVars(), nil)
	if err == nil {
		t.Fatal("${a-b} was accepted")
	}
	if want := `"a-b" is not a variable name.`; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// "env" is reserved for the environment, so a bare "$env" is a mistake with a
// sentence of its own rather than an unset variable.
func TestABareEnvIsReserved(t *testing.T) {
	for _, in := range []string{"put $env", "put ${env}", "put $env."} {
		_, err := expandLine(t, in, session.NewVars(), nil)
		if err == nil {
			t.Fatalf("%s was accepted", in)
		}
		if want := `"env" is reserved; write $env.NAME.`; err.Error() != want {
			t.Errorf("%s: error = %q, want %q", in, err.Error(), want)
		}
	}
}

// A user variable actually named "env" is out of reach, and that is the point:
// the environment owns the name.
func TestAnEnvVariableStillExpands(t *testing.T) {
	t.Setenv("CDB_TEST_NAMES", "value")
	got, err := expandLine(t, "put $env.CDB_TEST_NAMES", session.NewVars(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[1] != "value" {
		t.Errorf("argv[1] = %q, want %q", got[1], "value")
	}
}
