package command

import (
	"bytes"
	"context"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/session"
)

func testSession() *session.Session {
	return session.New(bytes.NewReader(nil), &bytes.Buffer{}, &bytes.Buffer{})
}

func TestPwdReturnsCurrentPath(t *testing.T) {
	s := testSession()
	s.SetPath("/mydb/_design/app")
	res, err := Pwd().Run(context.Background(), s, Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := res.(Message)
	if !ok {
		t.Fatalf("result is %T, want Message", res)
	}
	if msg.Text != "/mydb/_design/app" {
		t.Errorf("pwd = %q, want %q", msg.Text, "/mydb/_design/app")
	}
}

func TestPwdMetadata(t *testing.T) {
	c := Pwd()
	if c.Name != "pwd" || c.NeedsClient || c.Destructive || c.MaxArgs != 0 {
		t.Errorf("Pwd() = %+v", c)
	}
}
