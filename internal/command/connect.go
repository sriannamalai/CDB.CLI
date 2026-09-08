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
		return nil, fmt.Errorf("could not open the system keyring: %w. Set CDB_PASSWORD or CDB_TOKEN to connect without saving", d.secretsErr)
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

func promptPassphrase(prompt string) (string, error) {
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

	switch {
	case strings.HasPrefix(nameOrURL, "http://"), strings.HasPrefix(nameOrURL, "https://"):
		profile = config.Profile{Name: "", URL: nameOrURL, Auth: "none"}
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
		} else {
			return connection{}, Usagef("connect", "no profile is saved. Run \"cdb connect <url>\" or \"cdb profiles add\".")
		}
	}

	profile = env.Apply(profile)

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
		if err == nil {
			secret = stored
		}
	}

	profile.Name = profileName
	return dial(ctx, s, profile, profileName, secret)
}

// dial builds a client for an already-resolved profile, proves it works, and
// attaches it to the session. attachAs is the profile name the session should
// report, which is "" for a connection that is not (yet) saved.
func dial(ctx context.Context, s *session.Session, profile config.Profile, attachAs, secret string) (connection, error) {
	cc, err := couch.New(couch.Config{
		URL:         profile.URL,
		Auth:        couch.AuthKind(profile.Auth),
		Username:    profile.Username,
		Secret:      secret,
		InsecureTLS: profile.InsecureTLS,
		CAFile:      profile.CAFile,
		UserAgent:   "cdb",
	})
	if err != nil {
		return connection{}, err
	}
	info, err := cc.ServerInfo(ctx)
	if err != nil {
		_ = cc.Close()
		return connection{}, err
	}
	// Spec 6.1: the connection is verified with GET /_session, which is what
	// proves the credentials were accepted; GET / answers for anyone.
	sess, err := cc.Session(ctx)
	if err != nil {
		_ = cc.Close()
		return connection{}, err
	}
	// A server with no admins, or a JWT it declines to honour, answers
	// GET /_session with "name": null and a 200. Asking for authentication and
	// silently getting none is a failure, not a connection.
	if sess.Name == "" && profile.Auth != string(couch.AuthNone) {
		_ = cc.Close()
		return connection{}, couch.NewError(http.StatusUnauthorized, "unauthorized",
			"Login succeeded anonymously; check the username.", "authenticate", "server "+cc.Host())
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
		Usage:   "[profile | url]",
		MinArgs: 0,
		MaxArgs: 1,
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
				guided = true
				conn, err = dial(ctx, s, p, "", secret)
			} else {
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
// its secret to the keyring. The secret never reaches the config file, and
// neither does a password carried in the URL's userinfo: that moves into the
// keyring too, leaving a profile that reconnects the same way.
func saveConnection(s *session.Session, name string, conn connection) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	profile, secret := conn.Profile, conn.Secret
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
			return err
		}
	}
	profile.Name = name
	cfg.SetProfile(profile)
	if cfg.Default == "" {
		cfg.Default = name
	}
	if err := cfg.Save(CurrentDeps().ConfigPath); err != nil {
		return err
	}
	if store != nil {
		if err := store.Set(name, secret); err != nil {
			fmt.Fprintf(s.Stderr, "warning: could not save the secret in the keyring: %v\n", err)
		}
	}
	s.Profile = name
	return nil
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

// promptForProfile walks an operator through creating the first profile.
func promptForProfile(s *session.Session) (config.Profile, string, error) {
	r := s.Reader()
	ask := func(label, def string) (string, error) {
		if def != "" {
			fmt.Fprintf(s.Stdout, "%s [%s]: ", label, def)
		} else {
			fmt.Fprintf(s.Stdout, "%s: ", label)
		}
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return def, nil
		}
		return line, nil
	}
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

// askYesNo puts a yes/no question to the operator, defaulting to yes. The
// walk-through that calls it runs only when both ends are a real terminal, so
// a read error means there is nobody left to ask: the default stands rather
// than failing a connection that has already succeeded.
func askYesNo(s *session.Session, label string) bool {
	fmt.Fprintf(s.Stdout, "%s [Y/n]: ", label)
	line, err := s.Reader().ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	if err != nil && answer == "" {
		return true
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
		Usage:   "[list | add <name> <url> | remove <name> | default <name>]",
		MinArgs: 0,
		MaxArgs: 3,
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
		Run: func(_ context.Context, s *session.Session, inv Invocation) (Result, error) {
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
				p := config.Profile{Name: name, URL: serverURL, Auth: "session"}
				cfg.SetProfile(p)
				if cfg.Default == "" {
					cfg.Default = name
				}
				if err := cfg.Save(CurrentDeps().ConfigPath); err != nil {
					return nil, err
				}
				return Message{Text: fmt.Sprintf("Saved profile %q for %s. Run \"cdb connect %s\" to use it.", name, serverURL, name)}, nil

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

// SessionCmd returns the session command.
func SessionCmd() Command {
	return Command{
		Name:        "session",
		Summary:     "Show the current user, roles and server version",
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
