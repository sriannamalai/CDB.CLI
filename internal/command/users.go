package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// usersDB is the authentication database. CouchDB fixes the name; it is not a
// path the operator navigates to, so it is spelled once here.
const usersDB = "_users"

// userIDPrefix is the prefix CouchDB requires on every user document id.
const userIDPrefix = "org.couchdb.user:"

// hashFields are the members CouchDB writes in place of a plaintext password.
// They are never shown, and they are removed before a document with a new
// password is written back: a document carrying both a "password" and a stale
// "derived_key" is the one way to lock a user out of their own account.
var hashFields = []string{"password_sha", "salt", "derived_key", "iterations", "password_scheme"}

// userDocID is the document id for a user name.
func userDocID(name string) string { return userIDPrefix + name }

// usersDetails is the long help for users.
const usersDetails = `A CouchDB installation has two kinds of account. An ordinary user is a
document in the _users database, with a name, a list of roles and a password
the server hashes on write. A server admin is a key in the [admins]
configuration section instead, and may do anything on the server; --admin
manages those.

Passwords are never taken from the command line, where they would reach the
shell history and the process list. "users add" and "users passwd" prompt
twice with the echo off; a script passes the password as a single line on
standard input with --password-stdin.

"users add" creates the _users database if the server does not have one yet.
"users rm" refuses to remove the account the session is authenticated as.

A name that is a server admin is treated as one by "users passwd" and
"users rm" even without --admin, and the _users document of the same name, if
there is one, is left alone: the [admins] key is the credential the server
actually logs that name in with.`

// Users returns the users command.
func Users() Command {
	return Command{
		Name:    "users",
		Summary: "List and manage CouchDB accounts",
		Example: `$ cdb users
 NAME  | KIND         | ROLES
-------+--------------+----------------
 admin | server admin |
 alice | user         | editor, reader

$ cdb users show alice
$ cdb users add alice --roles editor,reader
Password for alice:
Created user "alice".

$ printf '%s\n' "$NEW_PASSWORD" | cdb users passwd alice --password-stdin
$ cdb users rm alice --yes`,
		Usage:       "[list | show NAME | add NAME | passwd NAME | rm NAME]",
		Details:     usersDetails,
		MinArgs:     0,
		MaxArgs:     2,
		NeedsClient: true,
		Flags: func(fs *pflag.FlagSet) {
			fs.String("roles", "", "comma-separated roles for a new user")
			fs.Bool("admin", false, "act on a server admin rather than a _users document")
			fs.Bool("password-stdin", false, "read the password as one line from standard input")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			sub := inv.Arg(0)
			if sub == "" {
				sub = "list"
			}
			switch sub {
			case "list":
				return usersList(ctx, s)
			case "show":
				return usersShow(ctx, s, inv.Arg(1))
			case "add":
				return usersAdd(ctx, s, inv)
			case "passwd":
				return usersPasswd(ctx, s, inv)
			case "rm":
				return usersRemove(ctx, s, inv)
			default:
				return nil, Usagef("users", "unknown subcommand %q; expected list, show, add, passwd or rm", sub)
			}
		},
	}
}

// serverAdmins reads the names in the [admins] configuration section. Only the
// names are kept: the values are password hashes.
func serverAdmins(ctx context.Context, s *session.Session) ([]string, error) {
	entries, err := s.Client.Config(ctx, defaultNode, "admins", "")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Key)
	}
	return names, nil
}

// adminHashPoll is how often the [admins] section is re-read while waiting for
// a password to be hashed, and adminHashWait is how long that wait lasts.
const (
	adminHashPoll = 100 * time.Millisecond
	adminHashWait = 2 * time.Second
)

// waitForAdminHash waits for CouchDB to hash a password just written to the
// [admins] section. The PUT returns before the hashing runs: for the next
// fraction of a second the section still holds the plaintext, and every login
// with it is refused, so reporting success at once hands the operator a
// credential that does not work yet — and the natural response, retrying the
// login, is the worst one, because CouchDB locks an account out after a run of
// failures. Two writes inside that window are worse still: the hashing of the
// first can land after the second value and put the first password back.
//
// The wait is bounded, and running out is not a failure: the write did land,
// and CouchDB 3.0 hashes synchronously, so there is nothing to wait for there.
// The value is only tested for the "-" that marks every CouchDB password hash;
// it is never returned, printed or logged.
func waitForAdminHash(ctx context.Context, s *session.Session, name string) {
	deadline := time.Now().Add(adminHashWait)
	for {
		entries, err := s.Client.Config(ctx, defaultNode, "admins", name)
		if err != nil {
			return
		}
		if len(entries) == 1 && strings.HasPrefix(entries[0].Value, "-") {
			return
		}
		if !time.Now().Before(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(adminHashPoll):
		}
	}
}

// usersList prints every account: the _users documents whose type is "user",
// and the server admins from the configuration.
func usersList(ctx context.Context, s *session.Session) (Result, error) {
	admins, err := serverAdmins(ctx, s)
	if err != nil {
		return nil, couch.AsAdmin(err, "managing users")
	}
	rows := Rows{Columns: []Column{{Title: "name"}, {Title: "kind"}, {Title: "roles"}}}
	for _, name := range admins {
		rows.Items = append(rows.Items, userRow(name, "server admin", nil))
	}

	page, err := s.Client.AllDocs(ctx, usersDB, couch.AllDocsOptions{IncludeDocs: true})
	if err != nil {
		if e, ok := couch.AsError(err); ok && e.Status == 404 && len(rows.Items) == 0 {
			return Message{Text: `No users are defined; the _users database does not exist yet. "users add <name>" creates it.`}, nil
		}
		if e, ok := couch.AsError(err); ok && e.Status == 404 {
			// There are server admins but no _users database. That is a normal
			// single-admin install, not a failure.
			return rows, nil
		}
		return nil, couch.AsAdmin(err, "managing users")
	}
	for _, row := range page.Rows {
		doc, ok := decodeUserDoc(row.Doc)
		if !ok {
			continue
		}
		rows.Items = append(rows.Items, userRow(doc.Name, "user", doc.Roles))
	}
	if len(rows.Items) == 0 {
		rows.Hint = "no accounts are defined"
	}
	return rows, nil
}

// userDoc is the part of a _users document cdb reads. The hash members are
// deliberately absent: nothing in this package has a reason to hold them.
type userDoc struct {
	ID    string   `json:"_id"`
	Rev   string   `json:"_rev"`
	Name  string   `json:"name"`
	Type  string   `json:"type"`
	Roles []string `json:"roles"`
}

// decodeUserDoc decodes a row's document, reporting false for anything that is
// not a user: the _design/_auth document, and any other document an operator
// has put in the database.
func decodeUserDoc(raw json.RawMessage) (userDoc, bool) {
	if len(raw) == 0 {
		return userDoc{}, false
	}
	var doc userDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return userDoc{}, false
	}
	if doc.Type != "user" || doc.Name == "" {
		return userDoc{}, false
	}
	return doc, true
}

func userRow(name, kind string, roles []string) Row {
	joined := strings.Join(roles, ", ")
	return Row{
		Cells: []string{name, kind, joined},
		JSON:  jsonObject("name", name, "kind", kind, "roles", joined),
	}
}

// usersShow prints one account's fields. The hashes are never shown — only
// whether a password is set at all, which is the one thing about them an
// operator can act on.
func usersShow(ctx context.Context, s *session.Session, name string) (Result, error) {
	if name == "" {
		return nil, Usagef("users", "usage: users show <name>")
	}
	admin, err := isServerAdmin(ctx, s, name)
	if err != nil {
		return nil, err
	}
	if admin {
		// A server admin has no _users document to read: everything cdb can
		// say about one is in the [admins] key, whose value is a hash and so
		// is never shown. The name is the one users list prints, so show
		// answers for it rather than reporting a missing document.
		rows := Rows{Columns: []Column{{Title: "field"}, {Title: "value"}}}
		for _, kv := range [][2]string{{"name", name}, {"kind", "server admin"}, {"roles", "_admin"}, {"password", "set"}} {
			rows.Items = append(rows.Items, Row{Cells: []string{kv[0], kv[1]}, JSON: jsonObject("field", kv[0], "value", kv[1])})
		}
		return rows, nil
	}
	raw, _, err := s.Client.GetDocument(ctx, usersDB, userDocID(name), couch.GetOptions{})
	if err != nil {
		return nil, couch.AsAdmin(err, "managing users")
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, err
	}
	doc, _ := decodeUserDoc(raw)
	rows := Rows{Columns: []Column{{Title: "field"}, {Title: "value"}}}
	add := func(k, v string) {
		rows.Items = append(rows.Items, Row{Cells: []string{k, v}, JSON: jsonObject("field", k, "value", v)})
	}
	add("name", doc.Name)
	add("type", doc.Type)
	add("roles", strings.Join(doc.Roles, ", "))
	add("password", passwordState(members))
	return rows, nil
}

// passwordState says whether the server holds a password for this account,
// without saying anything about what it is.
func passwordState(members map[string]json.RawMessage) string {
	for _, f := range []string{"password_sha", "derived_key"} {
		if _, ok := members[f]; ok {
			return "set"
		}
	}
	return "not set"
}

// passwordFor obtains the password for an account. It is never a command-line
// argument: an argument reaches the shell history and every process list on
// the machine. --password-stdin takes one line, which is what a script uses;
// otherwise the operator is asked twice with the echo off.
func passwordFor(s *session.Session, inv Invocation, name string) (string, error) {
	if inv.Bool("password-stdin") {
		line, err := s.Reader().ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		// A short read that still produced a line is a file with no trailing
		// newline, which is fine. A read that broke is not: treating a partial
		// line from a failed device as the password would set a password
		// nobody typed.
		if err != nil && !errors.Is(err, io.EOF) {
			return "", Errorf(err, "Reading the password from standard input failed: %v", err)
		}
		if line == "" {
			return "", Usagef("users", "no password arrived on standard input")
		}
		return line, nil
	}
	if !s.Prefs.Interactive {
		return "", Usagef("users", "reading a password needs a terminal; pass it as one line on standard input with --password-stdin")
	}
	first, err := readSecret(s, "Password for "+name)
	if err != nil {
		return "", err
	}
	again, err := readSecret(s, "Repeat password for "+name)
	if err != nil {
		return "", err
	}
	if first != again {
		return "", Errorf(nil, "The two passwords did not match; nothing was changed.")
	}
	if first == "" {
		return "", Errorf(nil, "An empty password is not accepted.")
	}
	return first, nil
}

// isServerAdmin reports whether the name is a key in [admins].
func isServerAdmin(ctx context.Context, s *session.Session, name string) (bool, error) {
	admins, err := serverAdmins(ctx, s)
	if err != nil {
		return false, couch.AsAdmin(err, "managing users")
	}
	for _, a := range admins {
		if a == name {
			return true, nil
		}
	}
	return false, nil
}

// usersAdd creates an account. --admin writes the [admins] configuration key,
// which is what a server admin is; everything else writes a _users document.
func usersAdd(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
	name := inv.Arg(1)
	if name == "" {
		return nil, Usagef("users", "usage: users add <name> [--roles r1,r2] [--admin]")
	}
	admin, err := isServerAdmin(ctx, s, name)
	if err != nil {
		return nil, err
	}
	if admin {
		return nil, Errorf(nil, "Server admin %q already exists; \"users passwd %s --admin\" changes the password.", name, name)
	}
	if inv.Bool("admin") {
		password, err := passwordFor(s, inv, name)
		if err != nil {
			return nil, err
		}
		if _, err := s.Client.SetConfig(ctx, defaultNode, "admins", name, password); err != nil {
			return nil, couch.AsAdmin(err, "managing users")
		}
		waitForAdminHash(ctx, s, name)
		return Message{Text: fmt.Sprintf("Created server admin %q.", name)}, nil
	}

	created, err := ensureUsersDB(ctx, s)
	if err != nil {
		return nil, err
	}
	if !created {
		if _, _, err := s.Client.GetDocument(ctx, usersDB, userDocID(name), couch.GetOptions{}); err == nil {
			return nil, Errorf(nil, "User %q already exists; \"users passwd %s\" changes the password.", name, name)
		} else if e, ok := couch.AsError(err); !ok || e.Status != 404 {
			return nil, couch.AsAdmin(err, "managing users")
		}
	}
	password, err := passwordFor(s, inv, name)
	if err != nil {
		return nil, err
	}
	roles := splitRoles(inv.String("roles"))
	if roles == nil {
		roles = []string{}
	}
	doc, err := json.Marshal(map[string]any{
		"_id": userDocID(name), "name": name, "type": "user",
		"roles": roles, "password": password,
	})
	if err != nil {
		return nil, err
	}
	if _, err := s.Client.PutDocument(ctx, usersDB, userDocID(name), doc, ""); err != nil {
		return nil, couch.AsAdmin(err, "managing users")
	}
	if created {
		return Message{Text: fmt.Sprintf("Created the _users database and user %q.", name)}, nil
	}
	return Message{Text: fmt.Sprintf("Created user %q.", name)}, nil
}

// ensureUsersDB creates the authentication database when the server has none,
// reporting whether it had to. CouchDB needs it before a user document can be
// written, and a server that has never had an admin has never had one.
func ensureUsersDB(ctx context.Context, s *session.Session) (bool, error) {
	exists, err := s.Client.DatabaseExists(ctx, usersDB)
	if err != nil {
		return false, couch.AsAdmin(err, "managing users")
	}
	if exists {
		return false, nil
	}
	if err := s.Client.CreateDatabase(ctx, usersDB, false, 0); err != nil {
		return false, couch.AsAdmin(err, "managing users")
	}
	s.Cache().InvalidateDatabases()
	return true, nil
}

// usersPasswd sets a new password. For a _users document the hash members are
// removed first: CouchDB's update hook replaces "password" with fresh ones, and
// a document carrying both is one the server refuses to authenticate against.
func usersPasswd(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
	name := inv.Arg(1)
	if name == "" {
		return nil, Usagef("users", "usage: users passwd <name> [--admin] [--password-stdin]")
	}
	admin, err := isServerAdmin(ctx, s, name)
	if err != nil {
		return nil, err
	}
	if admin || inv.Bool("admin") {
		password, err := passwordFor(s, inv, name)
		if err != nil {
			return nil, err
		}
		if _, err := s.Client.SetConfig(ctx, defaultNode, "admins", name, password); err != nil {
			return nil, couch.AsAdmin(err, "managing users")
		}
		waitForAdminHash(ctx, s, name)
		return Message{Text: fmt.Sprintf("Changed the password of server admin %q.", name)}, nil
	}

	// The document is read first so that an unknown name is refused before the
	// operator types anything.
	raw, rev, err := s.Client.GetDocument(ctx, usersDB, userDocID(name), couch.GetOptions{})
	if err != nil {
		return nil, couch.AsAdmin(err, "managing users")
	}
	password, err := passwordFor(s, inv, name)
	if err != nil {
		return nil, err
	}
	err = putUserPassword(ctx, s, name, raw, rev, password)
	if e, ok := couch.AsError(err); ok && e.Status == 409 {
		// Someone edited the document while the password was being typed, so
		// the revision read before the prompt is stale. Re-read and write once
		// more, onto their version rather than over it.
		raw, rev, err = s.Client.GetDocument(ctx, usersDB, userDocID(name), couch.GetOptions{})
		if err != nil {
			return nil, couch.AsAdmin(err, "managing users")
		}
		err = putUserPassword(ctx, s, name, raw, rev, password)
	}
	if err != nil {
		return nil, couch.AsAdmin(err, "managing users")
	}
	return Message{Text: fmt.Sprintf("Changed the password of %q.", name)}, nil
}

// putUserPassword writes one user document back with a new password, without
// the hash members CouchDB wrote for the old one. Everything else in the
// document is kept, so a role an operator added elsewhere survives a password
// change.
func putUserPassword(ctx context.Context, s *session.Session, name string, raw json.RawMessage, rev, password string) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return err
	}
	for _, f := range hashFields {
		delete(members, f)
	}
	quoted, err := json.Marshal(password)
	if err != nil {
		return err
	}
	members["password"] = quoted
	doc, err := json.Marshal(members)
	if err != nil {
		return err
	}
	_, err = s.Client.PutDocument(ctx, usersDB, userDocID(name), doc, rev)
	return err
}

// usersRemove deletes an account, after refusing to delete the one the session
// is using. Removing your own account mid-session leaves a connection that
// works until it is closed and then cannot be reopened, which is the worst
// possible way to find out.
func usersRemove(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
	name := inv.Arg(1)
	if name == "" {
		return nil, Usagef("users", "usage: users rm <name>")
	}
	sess, err := s.Client.Session(ctx)
	if err != nil {
		return nil, couch.AsAdmin(err, "managing users")
	}
	if sess.Name == name {
		return nil, Errorf(nil, "You are connected as %s; remove that user from another account.", name)
	}
	admin, err := isServerAdmin(ctx, s, name)
	if err != nil {
		return nil, err
	}
	if admin || inv.Bool("admin") {
		if err := Confirm(ctx, s, fmt.Sprintf("Delete server admin %s?", name)); err != nil {
			return nil, err
		}
		if _, err := s.Client.DeleteConfig(ctx, defaultNode, "admins", name); err != nil {
			return nil, couch.AsAdmin(err, "managing users")
		}
		return Message{Text: fmt.Sprintf("Deleted server admin %q.", name)}, nil
	}
	if err := Confirm(ctx, s, fmt.Sprintf("Delete user %s?", name)); err != nil {
		return nil, err
	}
	rev, err := s.Client.GetRev(ctx, usersDB, userDocID(name))
	if err != nil {
		return nil, couch.AsAdmin(err, "managing users")
	}
	if _, err := s.Client.DeleteDocument(ctx, usersDB, userDocID(name), rev); err != nil {
		return nil, couch.AsAdmin(err, "managing users")
	}
	return Message{Text: fmt.Sprintf("Deleted user %q.", name)}, nil
}
