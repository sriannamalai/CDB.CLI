package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
)

// TestNotConnectedMessage covers the shell's opening auto-connect failure —
// the first message a new user ever sees, and the one place in the branch that
// used to print the raw error instead of the section 11 sentence.
func TestNotConnectedMessage(t *testing.T) {
	refused := couch.Wrap(
		fmt.Errorf(`Get "http://localhost:5984/": %w`, &net.OpError{
			Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED),
		}),
		"connect to", "server localhost:5984")

	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{
			name: "a stopped server reads as the section 11 sentence",
			err:  refused,
			want: "Not connected: Could not reach localhost:5984. Is CouchDB running?\nRun \"connect <url>\" to connect.",
		},
		{
			name: "a usage error keeps its own single prefix",
			err:  command.Usagef("connect", "no profile is saved. Run \"cdb connect <url>\" or \"cdb profiles add\"."),
			want: "connect: no profile is saved. Run \"cdb connect <url>\" or \"cdb profiles add\".\nRun \"connect <url>\" to connect.",
		},
		{
			name: "an interrupt prints nothing",
			err:  fmt.Errorf("open: %w", context.Canceled),
			want: "",
		},
		{
			name: "no error prints nothing",
			err:  nil,
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := notConnectedMessage(tc.err, false); got != tc.want {
				t.Errorf("notConnectedMessage:\n got %q\nwant %q", got, tc.want)
			}
		})
	}

	t.Run("verbose appends the raw status", func(t *testing.T) {
		got := notConnectedMessage(refused, true)
		if !strings.Contains(got, "[status 0 connection_refused") {
			t.Errorf("verbose message lost the detail: %q", got)
		}
	})

	t.Run("a plain error still renders", func(t *testing.T) {
		got := notConnectedMessage(errors.New("keyring is locked"), false)
		if !strings.HasPrefix(got, "Not connected: keyring is locked\n") {
			t.Errorf("got %q", got)
		}
	})
}
