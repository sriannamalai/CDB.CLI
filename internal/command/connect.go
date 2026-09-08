package command

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/session"
	"golang.org/x/term"
)

// Deps is the injectable environment the connection commands need. Tests
// replace it; production leaves it nil so the real files and keyring are used.
type Deps struct {
	ConfigPath string
	Secrets    config.Secrets
	LookupEnv  func(string) (string, bool)
	// secretsErr is why Secrets is nil: the OS keyring would not open. It is
	// held rather than acted on at construction time, because a connection that
	// needs no stored secret must still work on a machine with a broken
	// keyring.
	secretsErr error
}

// SecretStore returns the keyring, or a plain error explaining why it is
// unavailable. There is deliberately no in-memory fallback: a store that
// accepts a secret and forgets it when the process exits would make
// "connect --save" report success and then fail on the next run, which is the
// worst of both outcomes.
func (d *Deps) SecretStore() (config.Secrets, error) {
	if d.Secrets != nil {
		return d.Secrets, nil
	}
	if d.secretsErr != nil {
		// A ConnectionError, not a plain one: nothing ran, and the fix is in
		// the connection rather than the command, so this exits 3 alongside the
		// keyring read failure in openProfile.
		return nil, Connectionf(d.secretsErr, "Could not open the system keyring: %s. Set CDB_PASSWORD or CDB_TOKEN to connect without saving.", trimSentence(d.secretsErr))
	}
	return nil, errors.New("no secret store is configured")
}

var deps *Deps

// SetDeps installs the dependency set. Passing nil restores the real files,
// keyring and environment.
func SetDeps(d *Deps) { deps = d }

// CurrentDeps returns the active dependency set, building the real one on
// first use.
func CurrentDeps() *Deps {
	if deps != nil {
		return deps
	}
	path, err := config.ConfigPath()
	if err != nil {
		path = "config.toml"
	}
	dir, err := config.ConfigDir()
	if err != nil {
		dir = "."
	}
	secrets, secretsErr := config.OpenSecrets(dir, promptPassphrase)
	deps = &Deps{ConfigPath: path, Secrets: secrets, LookupEnv: os.LookupEnv, secretsErr: secretsErr}
	return deps
}

// promptPassphrase supplies the encrypted file keyring's passphrase.
//
// CDB_KEYRING_PASSPHRASE is consulted first. Without it the file backend is
// unusable outside a terminal — term.ReadPassword can only fail on a pipe —
// so a script or a CI run that stores a profile's password had no way to
// complete, and now that a failed store is fatal rather than a warning, that
// would be a hard stop rather than a nuisance.
func promptPassphrase(prompt string) (string, error) {
	if v := os.Getenv("CDB_KEYRING_PASSPHRASE"); v != "" {
		return v, nil
	}
	fmt.Fprint(os.Stderr, prompt+": ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(b), err
}

// loadConfig reads the config file named by the current deps.
func loadConfig() (*config.Config, error) { return config.Load(CurrentDeps().ConfigPath) }

// checkProfileName rejects names that collide with the config file's key
// delimiter. koanf addresses settings as "profiles.<name>.url", so a name
// containing "." is indistinguishable from a nested table in a key path. The
// current parser pair happens to quote such a key and read it back intact, but
// that is an accident of go-toml's escaping rather than a guarantee, so cdb
// keeps profile names free of the delimiter instead of relying on it.
func checkProfileName(cmd, name string) error {
	if name == "" {
		return Usagef(cmd, "a profile name may not be empty")
	}
	if strings.Contains(name, ".") {
		return Usagef(cmd, "a profile name may not contain \".\"; try %q instead", strings.ReplaceAll(name, ".", "-"))
	}
	return nil
}

// splitURLCredentials separates any userinfo from a server URL. A URL such as
// https://admin:pw@host authenticates fine, but its password must never reach
// the config file or the terminal, so everything that stores or displays a URL
// goes through this first.
func splitURLCredentials(raw string) (clean, user, secret string) {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw, "", ""
	}
	user = u.User.Username()
	secret, _ = u.User.Password()
	u.User = nil
	return u.String(), user, secret
}

// connection is what openProfile resolved on the way to attaching a client:
// the profile it settled on, the secret it authenticated with, and the server
// banner it already read. Carrying them back lets "connect" report the version
// and "connect --save" persist exactly what it connected with, neither of them
// needing a second round trip.
type connection struct {
	Profile config.Profile
	Secret  string
	Info    couch.ServerInfo
}

// Open connects the session to nameOrURL, which may be a profile name, a
// server URL, or "" to mean "the default or only profile".
func Open(ctx context.Context, s *session.Session, nameOrURL string) error {
	_, err := openProfile(ctx, s, nameOrURL)
	return err
}

// openProfile does the work of Open. The returned profile's Name is "" when
// the caller passed a bare URL.
func openProfile(ctx context.Context, s *session.Session, nameOrURL string) (connection, error) {
	d := CurrentDeps()
	cfg, err := loadConfig()
	if err != nil {
		return connection{}, err
	}
	env := config.LoadEnv(d.LookupEnv)

	var profile config.Profile
	var profileName string

	// bare records "a server URL was named that carries no credentials of its
	// own". Whether that matters is decided below, once the environment has
	// had its say.
	bare := false

	switch {
	case strings.HasPrefix(nameOrURL, "http://"), strings.HasPrefix(nameOrURL, "https://"):
		// Userinfo in the URL is a credential: net/http turns it into a Basic
		// auth header. Record the user name it carries so the connection
		// reports who it is, instead of calling an authenticated session
		// anonymous.
		_, urlUser, urlSecret := splitURLCredentials(nameOrURL)
		profile = config.Profile{Name: "", URL: nameOrURL, Auth: "none", Username: urlUser}
		bare = urlUser == "" && urlSecret == ""
	case nameOrURL != "":
		p, ok := cfg.Profile(nameOrURL)
		if !ok {
			// A URL with an unexpected scheme reaches here rather than the
			// branch above. Say so instead of calling it a missing profile —
			// and redact first: the argument may carry a password.
			if strings.Contains(nameOrURL, "://") {
				return connection{}, Usagef("connect", "server URL %q must start with http:// or https://", couch.RedactURL(nameOrURL))
			}
			return connection{}, Usagef("connect", "no profile named %q. Run \"cdb profiles list\" to see the saved profiles.", couch.RedactURL(nameOrURL))
		}
		profile, profileName = p, nameOrURL
	default:
		name := env.Profile
		if name == "" {
			name = cfg.Default
		}
		if name == "" && len(cfg.Profiles) == 1 {
			name = cfg.ProfileNames()[0]
		}
		if name != "" {
			p, ok := cfg.Profile(name)
			if !ok {
				if strings.Contains(name, "://") {
					return connection{}, Usagef("connect", "server URL %q must start with http:// or https://", couch.RedactURL(name))
				}
				return connection{}, Usagef("connect", "no profile named %q. Run \"cdb profiles list\" to see the saved profiles.", couch.RedactURL(name))
			}
			profile, profileName = p, name
		} else if env.URL != "" {
			profile = config.Profile{URL: env.URL, Auth: "session"}
		} else if n := len(cfg.Profiles); n > 0 {
			// Profiles exist but none is the default and none was named, which
			// is a different problem from having none at all: telling this
			// operator that nothing is saved sends them to create a third.
			return connection{}, Usagef("connect", "%d profiles are saved but none is the default. Run \"cdb connect <name>\", or \"cdb profiles default <name>\" to pick one; \"cdb profiles list\" shows them.", n)
		} else {
			return connection{}, Usagef("connect", "no profile is saved. Run \"cdb connect <url>\" or \"cdb profiles add\".")
		}
	}

	// An explicit target wins over the environment, which wins over the config
	// file. Without this, CDB_URL — which a shell may export for convenience —
	// silently redirected a command the operator aimed at a named profile or a
	// named URL, and "cdb --url a rmdir /db" could delete a database on server
	// b. CDB_USER, CDB_PASSWORD, CDB_TOKEN and CDB_INSECURE_TLS are
	// credentials and transport settings rather than a target, so they still
	// apply: naming a profile does not mean declining the environment's
	// password.
	if nameOrURL != "" {
		env.URL = ""
	}
	// Credentials in the environment are credentials: a URL that carries none
	// of its own is not bare when CDB_USER or CDB_PASSWORD/CDB_TOKEN is set.
	if _, hasEnvSecret := env.Secret(); env.User != "" || hasEnvSecret {
		bare = false
	}
	profile = env.Apply(profile)
	// The flag wins over CDB_REPLICATION_URL, which Env.Apply has just layered
	// over the profile key — the same order --url, CDB_URL and the profile's
	// url follow. A flag the operator got wrong is a usage error, not a
	// connection failure, and the message never echoes what they typed.
	if s.Prefs.ReplicationURL != "" {
		base, rerr := couch.NormaliseReplicationURL(s.Prefs.ReplicationURL)
		if rerr != nil {
			return connection{}, Usagef("connect", "%v", rerr)
		}
		profile.ReplicationURL = base
	}

	// CDB_PASSWORD and CDB_TOKEN bypass the keyring entirely: the environment
	// is the override of last resort and must not be second-guessed by, or
	// blocked behind, an OS keychain prompt.
	secret, fromEnv := env.Secret()
	if !fromEnv && profile.Auth != "none" && profileName != "" {
		store, err := d.SecretStore()
		if err != nil {
			return connection{}, err
		}
		stored, err := store.Get(profileName)
		switch {
		case err == nil:
			secret = stored
		case errors.Is(err, config.ErrSecretNotFound):
			// A profile may legitimately have no stored secret: an anonymous
			// server, or one whose password arrives in the environment.
		default:
			// Everything else is the keyring refusing — a denied Keychain
			// prompt, a locked Secret Service, a wrong file passphrase.
			// Swallowing it leaves secret empty, the login 401s, and the
			// operator is told their password is wrong when it was never read.
			return connection{}, Connectionf(err,
				"Could not read the password for profile %q from the system keyring: %s. The passphrase or the keychain permission may be wrong; set CDB_PASSWORD to bypass it.",
				profileName, trimSentence(err))
		}
	}

	// A server URL with no credentials anywhere is the trap I3 named: a stock
	// CouchDB answers GET / and GET /_session to an anonymous client with 200,
	// so the connection reports success and then 401s on every command with a
	// sentence about a password nobody supplied. It is handled here rather
	// than in the "connect" command because this is the door every subcommand
	// and the shell's own startup go through — "cdb --url http://host ls /" is
	// the common case, not "cdb connect".
	notice := false
	if bare && !s.Prefs.Anonymous {
		if s.Prefs.Interactive {
			user, promptSecret, err := promptForCredentials(s)
			if err != nil {
				return connection{}, err
			}
			// An empty password means "none": connect anonymously rather than
			// sending an empty password the server can only reject.
			if promptSecret != "" {
				profile.Auth, profile.Username, secret = "session", user, promptSecret
			}
		} else {
			notice = true
		}
	}

	profile.Name = profileName
	conn, err := dial(ctx, s, profile, profileName, secret)
	if err != nil {
		return conn, err
	}
	if notice {
		fmt.Fprintln(s.Stderr, anonymousNotice)
	}
	return conn, nil
}

// anonymousNotice is said once, on stderr, when a connection was made with no
// credentials because there was nobody to ask for any.
const anonymousNotice = "Connected anonymously; pass --anonymous to silence this or set CDB_USER/CDB_PASSWORD."

// verifyLogin builds a client for an already-resolved profile and proves the
// credentials are accepted, returning the live client and the server banner it
// read. A failure closes the client and returns the server's own error, so the
// caller has nothing to clean up.
//
// It is the single place a credential is checked. dial attaches the client it
// hands back to the session; "profiles add" closes it again, because it is
// only asking whether the password works before writing it down.
func verifyLogin(ctx context.Context, profile config.Profile, secret string) (*couch.Client, couch.ServerInfo, error) {
	cc, err := couch.New(couch.Config{
		URL:            profile.URL,
		Auth:           couch.AuthKind(profile.Auth),
		Username:       profile.Username,
		Secret:         secret,
		InsecureTLS:    profile.InsecureTLS,
		CAFile:         profile.CAFile,
		ReplicationURL: profile.ReplicationURL,
		UserAgent:      "cdb",
	})
	if err != nil {
		return nil, couch.ServerInfo{}, err
	}
	info, err := cc.ServerInfo(ctx)
	if err != nil {
		_ = cc.Close()
		return nil, couch.ServerInfo{}, err
	}
	// Spec 6.1: the connection is verified with GET /_session, which is what
	// proves the credentials were accepted; GET / answers for anyone.
	sess, err := cc.Session(ctx)
	if err != nil {
		_ = cc.Close()
		return nil, couch.ServerInfo{}, err
	}
	// A server with no admins, or a JWT it declines to honour, answers
	// GET /_session with "name": null and a 200. Asking for authentication and
	// silently getting none is a failure, not a connection.
	if sess.Name == "" && profile.Auth != string(couch.AuthNone) {
		_ = cc.Close()
		return nil, couch.ServerInfo{}, couch.NewError(http.StatusUnauthorized, "unauthorized",
			"Login succeeded anonymously; check the username.", "authenticate", "server "+cc.Host())
	}
	return cc, info, nil
}

// dial builds a client for an already-resolved profile, proves it works, and
// attaches it to the session. attachAs is the profile name the session should
// report, which is "" for a connection that is not (yet) saved.
func dial(ctx context.Context, s *session.Session, profile config.Profile, attachAs, secret string) (connection, error) {
	cc, info, err := verifyLogin(ctx, profile, secret)
	if err != nil {
		return connection{}, err
	}
	if !supportedVersion(info.Version) {
		fmt.Fprintf(s.Stderr, "warning: this server reports CouchDB %s; cdb supports 3.2 through 3.5.\n", info.Version)
	}
	if profile.InsecureTLS {
		fmt.Fprintln(s.Stderr, "warning: TLS certificate verification is disabled for this connection.")
	}
	s.Attach(cc, attachAs)
	return connection{Profile: profile, Secret: secret, Info: info}, nil
}

// validAuthKind reports whether s is one of the three kinds couch.New acts on.
func validAuthKind(s string) bool {
	switch couch.AuthKind(s) {
	case couch.AuthSession, couch.AuthJWT, couch.AuthNone:
		return true
	}
	return false
}

// supportedVersion reports whether v is CouchDB 3.2 through 3.5.
func supportedVersion(v string) bool {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 || parts[0] != "3" {
		return false
	}
	switch parts[1] {
	case "2", "3", "4", "5":
		return true
	}
	return false
}

// Connect returns the connect command.
func Connect() Command {
	return Command{
		Name:    "connect",
		Summary: "Open a connection to a server",
		Example: `$ cdb connect --save --as local http://admin:password@localhost:5984/
Connected to CouchDB 3.5.2 at localhost:5984 as admin. Saved as profile "local".

$ cdb connect local
Connected to CouchDB 3.5.2 at localhost:5984 as admin.`,
		Usage:   "[profile | url]",
		MinArgs: 0,
		MaxArgs: 1,
		Details: "A server URL with no user name and password is asked about: a CouchDB with an admin\n" +
			"accepts an anonymous connection and then refuses every command, so connect asks for\n" +
			"credentials first. Pass --anonymous to connect without any, or set CDB_USER and\n" +
			"CDB_PASSWORD.",
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("save", false, "save the connection as a profile after connecting")
			fs.String("as", "", "profile name to save under")
		},
		Complete: func(_ context.Context, _ *session.Session, _ []string, cur string) []Candidate {
			cfg, err := loadConfig()
			if err != nil {
				return nil
			}
			var out []Candidate
			for _, n := range cfg.ProfileNames() {
				if strings.HasPrefix(n, cur) {
					p, _ := cfg.Profile(n)
					clean, _, _ := splitURLCredentials(p.URL)
					out = append(out, Candidate{Value: n, Description: clean, Tag: "profiles"})
				}
			}
			return out
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			// Reject an unusable --as before doing any network work.
			if as := inv.String("as"); as != "" {
				if err := checkProfileName("connect", as); err != nil {
					return nil, err
				}
			}
			// --anonymous is a shared flag, so both front-ends have already put
			// it on the session before Run; honour it here too for a direct
			// call that has only the flag set.
			if inv.Bool("anonymous") && !s.Prefs.Anonymous {
				s.Prefs.Anonymous = true
				defer func() { s.Prefs.Anonymous = false }()
			}
			arg := inv.Arg(0)
			var conn connection
			var err error
			guided := false
			if arg == "" && !hasAnyProfile() && s.Prefs.Interactive {
				// Spec 6.1: ask, verify, and only then offer to save. Nothing
				// reaches config.toml or the keyring until the server has
				// accepted the answers, so a mistyped password leaves no
				// half-made profile behind.
				p, secret, promptErr := promptForProfile(s)
				if promptErr != nil {
					return nil, promptErr
				}
				// The walk-through asks for a URL, an auth kind and a name,
				// so those answers stand: the CDB_* overrides exist to beat
				// the config file, not something the operator typed a second
				// ago at a prompt they were just shown. The replication URL is
				// the one key nobody is asked for, so it is the one key
				// layered on here — CDB_REPLICATION_URL first, then the flag,
				// the same order openProfile uses — and without it a first
				// run, which is exactly when the walk-through appears, could
				// never dial or save one.
				if v := config.LoadEnv(CurrentDeps().LookupEnv).ReplicationURL; v != "" {
					p.ReplicationURL = v
				}
				if s.Prefs.ReplicationURL != "" {
					p.ReplicationURL = s.Prefs.ReplicationURL
				}
				if p.ReplicationURL != "" {
					base, rerr := couch.NormaliseReplicationURL(p.ReplicationURL)
					if rerr != nil {
						return nil, Usagef("connect", "%v", rerr)
					}
					p.ReplicationURL = base
				}
				guided = true
				conn, err = dial(ctx, s, p, "", secret)
			} else {
				// openProfile owns the bare-URL question, because every other
				// command reaches a server through it too.
				conn, err = openProfile(ctx, s, arg)
			}
			if err != nil {
				return nil, err
			}
			who := s.Client.Username()
			if who == "" {
				who = "anonymous"
			}
			text := fmt.Sprintf("Connected to CouchDB %s at %s as %s.", conn.Info.Version, s.Client.Host(), who)
			save := inv.Bool("save") || inv.String("as") != ""
			if guided && !save {
				save = askYesNo(s, "Save this connection as a profile?")
			}
			if save {
				name := inv.String("as")
				if name == "" {
					name = conn.Profile.Name
				}
				if name == "" {
					name = profileNameFor(conn.Profile.URL)
				}
				if err := saveConnection(s, name, conn); err != nil {
					return nil, err
				}
				text += fmt.Sprintf(" Saved as profile %q.", name)
			}
			return Message{Text: text}, nil
		},
	}
}

// saveConnection writes the live connection to the config file under name, and
// its secret to the keyring, then records the profile on the session.
func saveConnection(s *session.Session, name string, conn connection) error {
	if _, err := storeProfile(name, conn.Profile, conn.Secret); err != nil {
		return err
	}
	s.Profile = name
	return nil
}

// storeProfile is the single path a profile takes into config.toml. Every
// caller goes through it, because the rule it enforces has to hold for all of
// them: the secret never reaches the config file, and neither does a password
// carried in the URL's userinfo — that moves into the keyring too, leaving a
// profile that reconnects the same way. It returns the profile as stored, so a
// caller can report a URL that is known to carry no credentials.
func storeProfile(name string, profile config.Profile, secret string) (config.Profile, error) {
	cfg, err := loadConfig()
	if err != nil {
		return config.Profile{}, err
	}
	clean, urlUser, urlSecret := splitURLCredentials(profile.URL)
	profile.URL = clean
	if profile.Username == "" {
		profile.Username = urlUser
	}
	if secret == "" && urlSecret != "" {
		secret = urlSecret
		if profile.Auth == "none" {
			profile.Auth = "session"
		}
	}
	// Find the keyring before writing anything. A profile whose secret was
	// silently dropped is a profile that fails on the next run, so an
	// unopenable keyring has to stop the save rather than half-complete it.
	var store config.Secrets
	if secret != "" && profile.Auth != "none" {
		if store, err = CurrentDeps().SecretStore(); err != nil {
			return config.Profile{}, err
		}
	}
	// The secret goes in first, and a failure to store it stops the save. A
	// profile in config.toml whose password never reached the keyring is a
	// profile that reports success and then fails on the next run — which is
	// exactly what the comment above exists to prevent, and a warning does not
	// prevent it. Writing the secret before the profile means a failure here
	// leaves config.toml untouched rather than half-added.
	if store != nil {
		if err := store.Set(name, secret); err != nil {
			return config.Profile{}, fmt.Errorf("could not save the password for profile %q in the system keyring: %s. The profile was not saved; set CDB_PASSWORD to connect without the keyring", name, trimSentence(err))
		}
	}
	profile.Name = name
	cfg.SetProfile(profile)
	if cfg.Default == "" {
		cfg.Default = name
	}
	if err := cfg.Save(CurrentDeps().ConfigPath); err != nil {
		return config.Profile{}, err
	}
	return profile, nil
}

// trimSentence renders a wrapped error for embedding mid-sentence. Library
// errors are inconsistent about a trailing period, and one that has it turns
// "…: reason. Set CDB_PASSWORD…" into "…: reason.. Set CDB_PASSWORD…".
func trimSentence(err error) string {
	return strings.TrimRight(err.Error(), ". ")
}

// profileNameFor derives a profile name from a server URL when the operator
// passed --save without --as. Dots become dashes: host names are dotted far
// more often than not, and --as rejects a dotted name, so deriving one would
// hand the operator a profile they could not have asked for by name.
func profileNameFor(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "default"
	}
	return strings.ReplaceAll(u.Hostname(), ".", "-")
}

func hasAnyProfile() bool {
	cfg, err := loadConfig()
	return err == nil && len(cfg.Profiles) > 0
}

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

// promptForProfile walks an operator through creating the first profile.
func promptForProfile(s *session.Session) (config.Profile, string, error) {
	ask := func(label, def string) (string, error) { return askLine(s, label, def) }
	serverURL, err := ask("Server URL", "http://localhost:5984")
	if err != nil {
		return config.Profile{}, "", err
	}
	// Re-ask until the answer is one cdb understands. couch.New ignores an
	// auth kind it does not recognise, so accepting "sesion" here would build
	// an unauthenticated client, skip the username and secret questions, and
	// offer to save a profile that can never log in.
	var auth string
	for {
		auth, err = ask("Authentication (session, jwt, none)", "session")
		if err != nil {
			return config.Profile{}, "", err
		}
		if validAuthKind(auth) {
			break
		}
		fmt.Fprintf(s.Stdout, "%q is not an authentication kind; expected session, jwt or none.\n", auth)
	}
	name, err := ask("Profile name", "local")
	if err != nil {
		return config.Profile{}, "", err
	}
	if err := checkProfileName("connect", name); err != nil {
		return config.Profile{}, "", err
	}
	p := config.Profile{Name: name, URL: serverURL, Auth: auth}
	var secret string
	switch auth {
	case "session":
		user, err := ask("Username", "admin")
		if err != nil {
			return config.Profile{}, "", err
		}
		p.Username = user
		secret, err = readSecret(s, "Password")
		if err != nil {
			return config.Profile{}, "", err
		}
	case "jwt":
		secret, err = readSecret(s, "Bearer token")
		if err != nil {
			return config.Profile{}, "", err
		}
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

// Profiles returns the profiles command.
func Profiles() Command {
	return Command{
		Name:    "profiles",
		Summary: "List, add, remove and default saved profiles",
		Example: `$ cdb profiles add local http://admin:password@localhost:5984/
Saved profile "local" for http://localhost:5984/. Run "cdb connect local" to use it.

$ cdb profiles add prod https://couch.example.com/
This server may need a login. Press Enter at the password to connect anonymously.
Username [admin]: admin
Password:
Saved profile "prod" for https://couch.example.com/. Run "cdb connect prod" to use it.

$ cdb profiles list
 NAME  | URL                    | AUTH    | DEFAULT
-------+------------------------+---------+---------
 local | http://localhost:5984/ | session | yes

$ cdb profiles default local
Default profile is now "local".`,
		Usage:   "[list | add <name> <url> | remove <name> | default <name>]",
		MinArgs: 0,
		MaxArgs: 3,
		Details: "\"profiles add\" saves a server without connecting to it. A URL with no user name\n" +
			"and password is asked about on a terminal, the way connect asks: it prompts for\n" +
			"the user name and the password with echo off, proves them against the server, and\n" +
			"stores the password in the OS keychain. A profile saved with neither could not log\n" +
			"in — it would accept every command and then refuse it. Press Enter at the password,\n" +
			"or pass --anonymous, to save a profile that connects anonymously; without a\n" +
			"terminal nothing is asked and the URL is stored as typed.",
		Complete: func(_ context.Context, _ *session.Session, args []string, cur string) []Candidate {
			if len(args) == 0 {
				var out []Candidate
				for _, v := range []string{"list", "add", "remove", "default"} {
					if strings.HasPrefix(v, cur) {
						out = append(out, Candidate{Value: v, Tag: "subcommands"})
					}
				}
				return out
			}
			cfg, err := loadConfig()
			if err != nil {
				return nil
			}
			var out []Candidate
			for _, n := range cfg.ProfileNames() {
				if strings.HasPrefix(n, cur) {
					out = append(out, Candidate{Value: n, Tag: "profiles"})
				}
			}
			return out
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			cfg, err := loadConfig()
			if err != nil {
				return nil, err
			}
			sub := inv.Arg(0)
			if sub == "" {
				sub = "list"
			}
			switch sub {
			case "list":
				rows := Rows{Columns: []Column{
					{Title: "name"}, {Title: "url"}, {Title: "auth"}, {Title: "default"},
				}}
				for _, n := range cfg.ProfileNames() {
					p, _ := cfg.Profile(n)
					def := "no"
					if cfg.Default == n {
						def = "yes"
					}
					// A hand-edited config file may hold credentials in the URL;
					// they must not be echoed back to the terminal.
					shown, _, _ := splitURLCredentials(p.URL)
					rows.Items = append(rows.Items, Row{
						Cells: []string{n, shown, p.Auth, def},
						JSON:  jsonObject("name", n, "url", shown, "auth", p.Auth, "default", def),
					})
				}
				if len(rows.Items) == 0 {
					return Message{Text: "No profiles are saved. Run \"cdb connect <url>\" to create one."}, nil
				}
				return rows, nil

			case "add":
				name, serverURL := inv.Arg(1), inv.Arg(2)
				if name == "" || serverURL == "" {
					return nil, Usagef("profiles", "usage: profiles add <name> <url>")
				}
				if err := checkProfileName("profiles", name); err != nil {
					return nil, err
				}
				profile, secret, err := profileToAdd(ctx, s, inv, name, serverURL)
				if err != nil {
					return nil, err
				}
				// The URL argument may carry userinfo. storeProfile splits it
				// out, so neither the config file nor the message below can
				// hold a password: it is the same write path "connect --save"
				// uses, deliberately, rather than a second one to keep in step.
				stored, err := storeProfile(name, profile, secret)
				if err != nil {
					return nil, err
				}
				return Message{Text: fmt.Sprintf("Saved profile %q for %s. Run \"cdb connect %s\" to use it.", name, stored.URL, name)}, nil

			case "remove":
				name := inv.Arg(1)
				if name == "" {
					return nil, Usagef("profiles", "usage: profiles remove <name>")
				}
				if !cfg.RemoveProfile(name) {
					return nil, Usagef("profiles", "no profile named %q", name)
				}
				if err := cfg.Save(CurrentDeps().ConfigPath); err != nil {
					return nil, err
				}
				// The profile is already gone, so a keyring that will not open
				// is a warning rather than a failure: re-running the command
				// would report "no profile named ..." and never retry the
				// secret.
				store, err := CurrentDeps().SecretStore()
				if err != nil {
					fmt.Fprintf(s.Stderr, "warning: %v\n", err)
				} else if err := store.Remove(name); err != nil && err != config.ErrSecretNotFound {
					fmt.Fprintf(s.Stderr, "warning: could not remove the saved secret: %v\n", err)
				}
				return Message{Text: fmt.Sprintf("Removed profile %q.", name)}, nil

			case "default":
				name := inv.Arg(1)
				if name == "" {
					return nil, Usagef("profiles", "usage: profiles default <name>")
				}
				if _, ok := cfg.Profile(name); !ok {
					return nil, Usagef("profiles", "no profile named %q", name)
				}
				cfg.Default = name
				if err := cfg.Save(CurrentDeps().ConfigPath); err != nil {
					return nil, err
				}
				return Message{Text: fmt.Sprintf("Default profile is now %q.", name)}, nil

			default:
				return nil, Usagef("profiles", "unknown subcommand %q; expected list, add, remove or default", sub)
			}
		},
		Destructive: false,
	}
}

// profileToAdd settles what "profiles add" is about to write: the profile and
// the secret that goes with it.
//
// A URL typed with no user name and password used to be saved as a session
// profile with no secret, which is the one profile shape that cannot connect —
// "cdb connect <name>" then 401s on every command with advice about a password
// nobody was ever asked for. So on a terminal it asks, with connect's own two
// questions, and proves the answers against the server before anything is
// written: a rejected password leaves no profile behind, and the failure is
// the server's own 401, which reads "Login failed for <user> at <host>".
//
// Nothing changes for a URL that carries its own credentials, for --anonymous,
// which says outright that there are none, or for a script: without a terminal
// there is nobody to ask, and the profile is stored exactly as 1.1.0 stored
// it.
func profileToAdd(ctx context.Context, s *session.Session, inv Invocation, name, serverURL string) (config.Profile, string, error) {
	profile := config.Profile{Name: name, URL: serverURL, Auth: "session"}
	_, urlUser, urlSecret := splitURLCredentials(serverURL)
	switch {
	case inv.Bool("anonymous") || s.Prefs.Anonymous:
		profile.Auth = "none"
		return profile, "", nil
	case urlUser != "" || urlSecret != "":
		return profile, "", nil
	case !s.Prefs.Interactive:
		return profile, "", nil
	}
	user, secret, err := promptForCredentials(s)
	if err != nil {
		return config.Profile{}, "", err
	}
	if secret == "" {
		// The same reading connect gives an empty password: "there are none",
		// not "the password is the empty string". Saved as anonymous, it is a
		// profile that connects; saved as a session profile with no secret, it
		// is the profile this asks about in the first place.
		profile.Auth, profile.Username = "none", ""
		return profile, "", nil
	}
	profile.Username = user
	cc, _, err := verifyLogin(ctx, profile, secret)
	if err != nil {
		return config.Profile{}, "", err
	}
	// Verifying a profile is not connecting to it: the session keeps whatever
	// connection it already had.
	_ = cc.Close()
	return profile, secret, nil
}

// SessionCmd returns the session command.
func SessionCmd() Command {
	return Command{
		Name:    "session",
		Summary: "Show the current user, roles and server version",
		Example: `$ cdb session
 FIELD   | VALUE
---------+-----------------------
 server  | http://localhost:5984
 user    | admin
 roles   | _admin
 auth    | cookie
 version | 3.5.2`,
		Usage:       "",
		MinArgs:     0,
		MaxArgs:     0,
		NeedsClient: true,
		Run: func(ctx context.Context, s *session.Session, _ Invocation) (Result, error) {
			info, err := s.Client.ServerInfo(ctx)
			if err != nil {
				return nil, err
			}
			sess, err := s.Client.Session(ctx)
			if err != nil {
				return nil, err
			}
			name := sess.Name
			if name == "" {
				name = "anonymous"
			}
			rows := Rows{Columns: []Column{{Title: "field"}, {Title: "value"}}}
			add := func(k, v string) {
				rows.Items = append(rows.Items, Row{Cells: []string{k, v}, JSON: jsonObject("field", k, "value", v)})
			}
			add("server", s.Client.URL())
			add("user", name)
			add("roles", strings.Join(sess.Roles, ", "))
			add("auth", sess.Method)
			add("version", info.Version)
			add("vendor", info.Vendor)
			add("features", strings.Join(info.Features, ", "))
			if s.Profile != "" {
				add("profile", s.Profile)
			}
			return rows, nil
		},
	}
}
