package command

// Every question cdb asks an operator lives here: the walk-through that builds
// the first profile, the per-kind credential questions, and the two primitives
// they are built from. They were part of connect.go until 1.4; the file had
// grown to hold profile resolution, three commands and the prompts, and the
// prompts are the part with no server in it at all.
//
// Nothing here talks to a server, reads the configuration file or touches the
// keyring. They read from the session's input and write to its output, which
// is what makes them testable with a strings.Reader and a bytes.Buffer.

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// askLine puts one question to the operator and returns the answer, or def
// when the answer is empty. Both prompts share it, so both read through the
// session's single buffered reader.
func askLine(s *session.Session, label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(s.Stdout, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(s.Stdout, "%s: ", label)
	}
	line, err := s.Reader().ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

// promptForCredentials asks for the user name and password of a server URL
// typed with neither. It is the walk-through's own two questions, reused, so
// the two flows look and behave the same. An empty password means "connect
// anonymously"; the caller acts on that.
func promptForCredentials(s *session.Session) (user, secret string, err error) {
	fmt.Fprintln(s.Stdout, "This server may need a login. Press Enter at the password to connect anonymously.")
	user, err = askLine(s, "Username", "admin")
	if err != nil {
		return "", "", err
	}
	secret, err = readSecret(s, "Password")
	if err != nil {
		return "", "", err
	}
	return user, secret, nil
}

// promptForProxy asks the three questions proxy authentication needs: who cdb
// claims to be, what roles it claims, and the secret that proves the claim.
// def is the user name already known (from a profile or CDB_USER), which is
// offered as the default.
//
// The roles answer may be blank: a proxy user with no roles is a real user,
// and an empty X-Auth-CouchDB-Roles header is not the way to say so.
func promptForProxy(s *session.Session, def string) (user string, roles []string, secret string, err error) {
	if def == "" {
		def = "admin"
	}
	user, err = askLine(s, "Username", def)
	if err != nil {
		return "", nil, "", err
	}
	raw, err := askLine(s, "Roles (comma-separated, blank for none)", "")
	if err != nil {
		return "", nil, "", err
	}
	secret, err = readSecret(s, "Shared secret")
	if err != nil {
		return "", nil, "", err
	}
	return user, splitRoles(raw), secret, nil
}

// promptForAuthKind asks the questions one authentication kind needs and
// returns the credential they collected. The answers that belong to the
// profile are written into p: the user name for session and proxy, and the
// roles a proxy operator typed. "none" — and a kind cdb does not know — asks
// nothing, because there is no credential to collect.
//
// Every entry point that builds a profile interactively comes through here.
// The kinds used to be dispatched separately in each of them, and the copy in
// "profiles add" knew only about proxy: an IAM key was asked for at "Username"
// with echo on and then written into config.toml, which is plaintext.
func promptForAuthKind(s *session.Session, kind string, p *config.Profile) (string, error) {
	switch kind {
	case string(couch.AuthSession):
		user, err := askLine(s, "Username", "admin")
		if err != nil {
			return "", err
		}
		p.Username = user
		return readSecret(s, "Password")
	case string(couch.AuthJWT):
		return readSecret(s, "Bearer token")
	case string(couch.AuthProxy):
		user, roles, secret, err := promptForProxy(s, p.Username)
		if err != nil {
			return "", err
		}
		p.Username = user
		// Roles typed at the prompt win; a blank answer leaves whatever
		// --roles named rather than claiming none.
		if len(roles) > 0 {
			p.Roles = roles
		}
		return secret, nil
	case string(couch.AuthIAM):
		return readSecret(s, "IAM API key")
	}
	return "", nil
}

// promptForProfile walks an operator through creating the first profile. over
// is what --auth and --roles named: the kind becomes the prompt's default, so
// the operator confirms the flag rather than retyping it, and the roles stand
// unless the proxy questions collect some of their own.
func promptForProfile(s *session.Session, over authOverride) (config.Profile, string, error) {
	ask := func(label, def string) (string, error) { return askLine(s, label, def) }
	serverURL, err := ask("Server URL", "http://localhost:5984")
	if err != nil {
		return config.Profile{}, "", err
	}
	// Re-ask until the answer is one cdb understands. A typo like "sesion"
	// would otherwise skip the username and secret questions and offer to
	// save a profile that can never log in — couch.New now refuses an auth
	// kind it does not recognise (client.go), so it would not even connect.
	defKind := over.Kind
	if defKind == "" {
		defKind = "session"
	}
	var auth string
	for {
		auth, err = ask("Authentication (session, jwt, proxy, iam, none)", defKind)
		if err != nil {
			return config.Profile{}, "", err
		}
		if validAuthKind(auth) {
			break
		}
		fmt.Fprintf(s.Stdout, "%q is not an authentication kind; expected session, jwt, proxy, iam or none.\n", auth)
	}
	name, err := ask("Profile name", "local")
	if err != nil {
		return config.Profile{}, "", err
	}
	if err := checkProfileName("connect", name); err != nil {
		return config.Profile{}, "", err
	}
	p := config.Profile{Name: name, URL: serverURL, Auth: auth}
	if auth == string(couch.AuthProxy) {
		p.Roles = over.Roles
	}
	secret, err := promptForAuthKind(s, auth, &p)
	if err != nil {
		return config.Profile{}, "", err
	}
	return p, secret, nil
}

// askYesNo puts a yes/no question to the operator. Enter means yes; a read
// error — Ctrl-D, or a script whose input ran out — means no. The question
// exists to write a file and a keyring entry, and an answer nobody gave is not
// consent to write them. The connection itself has already succeeded either
// way, so nothing is lost by declining.
func askYesNo(s *session.Session, label string) bool {
	fmt.Fprintf(s.Stdout, "%s [Y/n]: ", label)
	line, err := s.Reader().ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	if err != nil && answer == "" {
		return false
	}
	switch answer {
	case "", "y", "yes":
		return true
	}
	return false
}

// readSecret reads a secret without echoing it when stdin is a terminal.
func readSecret(s *session.Session, label string) (string, error) {
	fmt.Fprintf(s.Stdout, "%s: ", label)
	if f, ok := s.Stdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(s.Stdout)
		return string(b), err
	}
	line, err := s.Reader().ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
