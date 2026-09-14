package command

import (
	"os"
	"testing"
)

// Every live test in this file opens its session with integrationSession,
// which lives at internal/command/integration_tail_test.go:20 and is shared by
// the whole package. Do not open a second one.
func TestTasksAgainstALiveServer(t *testing.T) {
	if os.Getenv("CDB_TEST_URL") == "" {
		t.Skip("CDB_TEST_URL is not set")
	}
	s := integrationSession(t)
	res, err := invoke(t, Tasks(), s)
	if err != nil {
		t.Fatal(err)
	}
	switch v := res.(type) {
	case Message:
		if v.Text != "No active tasks." {
			t.Errorf("message = %q", v.Text)
		}
	case Rows:
		if len(v.Columns) != 6 {
			t.Errorf("columns = %#v", v.Columns)
		}
	default:
		t.Fatalf("result is %T", res)
	}
}
