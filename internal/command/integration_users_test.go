package command

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// The live tests in this file open their session with integrationSession,
// which lives at internal/command/integration_tail_test.go:20 and is shared by
// the whole package. Every account they create is named with a "t4-" prefix
// and removed again by a t.Cleanup, which runs even when the test fails: a
// leftover server admin changes what every later test sees.

// TestUsersRoundTripAgainstALiveServer creates a _users user, changes its
// password, proves the new password logs in, and removes it. Every password in
// this test arrives on standard input, so none of them reaches a command line.
func TestUsersRoundTripAgainstALiveServer(t *testing.T) {
	if os.Getenv("CDB_TEST_URL") == "" {
		t.Skip("CDB_TEST_URL is not set")
	}
	s := integrationSession(t)
	s.Prefs.Yes = true
	const name = "t4-user"
	t.Cleanup(func() {
		rev, err := s.Client.GetRev(context.Background(), usersDB, userDocID(name))
		if err == nil {
			_, _ = s.Client.DeleteDocument(context.Background(), usersDB, userDocID(name), rev)
		}
	})

	s.SetStdin(strings.NewReader("firstpass\n"))
	if _, err := invoke(t, Users(), s, "add", name, "--roles", "editor", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	res, err := invoke(t, Users(), s, "show", name)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{}
	for _, item := range res.(Rows).Items {
		fields[item.Cells[0]] = item.Cells[1]
	}
	if fields["password"] != "set" || fields["roles"] != "editor" {
		t.Fatalf("after add, show = %v", fields)
	}

	s.SetStdin(strings.NewReader("secondpass\n"))
	if _, err := invoke(t, Users(), s, "passwd", name, "--password-stdin"); err != nil {
		t.Fatal(err)
	}

	// The real check that passwd worked: the new password logs in and the old
	// one does not. Build a second client rather than reusing the session's.
	if err := loginWorks(t, name, "secondpass"); err != nil {
		t.Fatalf("the new password was refused: %v", err)
	}
	if err := loginWorks(t, name, "firstpass"); err == nil {
		t.Fatal("the old password still works; the hash fields were left in the document")
	}

	if _, err := invoke(t, Users(), s, "rm", name); err != nil {
		t.Fatal(err)
	}
	list, err := invoke(t, Users(), s)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list.(Rows).Items {
		if item.Cells[0] == name {
			t.Fatal("the user is still listed after rm")
		}
	}
}

// loginWorks opens a session-authenticated client for one user and reads
// _session with it. The password is a parameter, never a log line.
func loginWorks(t *testing.T, name, password string) error {
	t.Helper()
	c, err := couch.New(couch.Config{
		URL: os.Getenv("CDB_TEST_URL"), Auth: couch.AuthSession,
		Username: name, Secret: password, UserAgent: "cdb",
	})
	if err != nil {
		return err
	}
	defer c.Close()
	sess, err := c.Session(context.Background())
	if err != nil {
		return err
	}
	if sess.Name != name {
		return errors.New("the server answered anonymously")
	}
	return nil
}

// waitForAdminHash waits until CouchDB has hashed a freshly written [admins]
// password, which it does after the PUT has already returned: for about a
// hundred milliseconds the section still holds the plaintext, and every login
// with it is refused (measured against 3.5.2 and 3.0.1 on 2026-09-14). Each
// write is waited for, not just the last one, because two writes to the same
// key in that window race: the hashing of the first can land after the second
// value and put the first password back. Retrying the login instead of waiting
// would be worse than useless — CouchDB locks an account out after a run of
// failures. The value itself is never printed, only whether it starts with the
// "-" that marks every CouchDB password hash.
func waitForAdminHash(t *testing.T, s *session.Session, name string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		entries, err := s.Client.Config(context.Background(), defaultNode, "admins", name)
		if err != nil {
			t.Fatalf("reading the [admins] section: %v", err)
		}
		if len(entries) == 1 && strings.HasPrefix(entries[0].Value, "-") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("CouchDB never hashed the password of server admin %q", name)
}

// TestServerAdminRoundTripAgainstALiveServer adds a second server admin,
// changes its password and removes it again, which is the [admins] half of §5.
func TestServerAdminRoundTripAgainstALiveServer(t *testing.T) {
	if os.Getenv("CDB_TEST_URL") == "" {
		t.Skip("CDB_TEST_URL is not set")
	}
	s := integrationSession(t)
	s.Prefs.Yes = true
	const name = "t4-admin"
	t.Cleanup(func() {
		// A leftover server admin changes what every later test sees, so the
		// removal is checked rather than attempted: this has to run even when
		// the test above has already failed.
		if _, err := s.Client.DeleteConfig(context.Background(), defaultNode, "admins", name); err != nil {
			if e, ok := couch.AsError(err); !ok || e.Status != 404 {
				t.Errorf("removing the test server admin %q left it behind: %v", name, err)
			}
		}
	})

	s.SetStdin(strings.NewReader("firstpass\n"))
	if _, err := invoke(t, Users(), s, "add", name, "--admin", "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	waitForAdminHash(t, s, name)
	list, err := invoke(t, Users(), s)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range list.(Rows).Items {
		if item.Cells[0] == name && item.Cells[1] == "server admin" {
			found = true
		}
		if strings.Contains(string(item.JSON), "pbkdf2") {
			t.Fatal("a password hash reached the listing")
		}
	}
	if !found {
		t.Fatal("the new server admin is not listed")
	}

	s.SetStdin(strings.NewReader("secondpass\n"))
	if _, err := invoke(t, Users(), s, "passwd", name, "--password-stdin"); err != nil {
		t.Fatal(err)
	}
	waitForAdminHash(t, s, name)
	if err := loginWorks(t, name, "secondpass"); err != nil {
		t.Fatalf("the new admin password was refused: %v", err)
	}
	if _, err := invoke(t, Users(), s, "rm", name); err != nil {
		t.Fatal(err)
	}
}
