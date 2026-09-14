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

// resolveTarget settles which target a connection is for: the one the caller
// named, or the one --profile/--url put on the session.
//
// The flags are resolved here because this is where every target is resolved.
// They used to be read only by the front-end's lazy auto-connect, which
// "connect" never takes — it opens its own connection — so "cdb connect --url
// http://elsewhere" dialled the default profile and reported success for a
// server the operator never named (issue #36).
//
// "connect" is also the only command that takes a target of its own, so it is
// the only place the two can disagree. docs/reference/connect.md says what
// --url overrides — the profile and CDB_URL — and says nothing about the
// argument, so a disagreement is reported rather than resolved by an invented
// precedence.
//
// Between the two flags, --url wins: its help text has always called it
// "server URL, overriding the profile and CDB_URL", and a profile named by
// --profile is still a profile. The front-end used to prefer --profile without
// saying so anywhere, which is the same silent drop #36 is about.
func resolveTarget(s *session.Session, nameOrURL string) (string, error) {
	flag, which := s.Prefs.URL, "--url"
	if flag == "" {
		flag, which = s.Prefs.Profile, "--profile"
	}
	if flag == "" || flag == nameOrURL {
		return nameOrURL, nil
	}
	if nameOrURL != "" {
		// Both values are named, because either of them may be a profile name
		// rather than a URL and the operator cannot otherwise tell which of
		// the two the message means. Both go through RedactURL first: either
		// may carry userinfo.
		return "", Usagef("connect", "%s names %q but the argument names %q; pass one of them",
			which, couch.RedactURL(flag), couch.RedactURL(nameOrURL))
	}
	return flag, nil
}

// authOverride carries "connect --auth" and "--roles" into profile
// resolution. The kind decides which credential is asked for and which
// transport is built, so it has to arrive before the profile is dialled rather
// than after -- which is why it travels as a parameter instead of being
// applied to the resolved profile by the caller.
type authOverride struct {
	Kind  string
	Roles []string
}

// openProfile does the work of Open, with no command-line override.
func openProfile(ctx context.Context, s *session.Session, nameOrURL string) (connection, error) {
	return openProfileWith(ctx, s, nameOrURL, authOverride{})
}

// openProfileWith is openProfile with the kind and roles the operator named on
// the command line. The returned profile's Name is "" when the caller passed a
// bare URL.
func openProfileWith(ctx context.Context, s *session.Session, nameOrURL string, over authOverride) (connection, error) {
	d := CurrentDeps()
	nameOrURL, err := resolveTarget(s, nameOrURL)
	if err != nil {
		return connection{}, err
	}
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
	// urlUserOnly records "a server URL was named that carries a user name and
	// no password". The URL branch below learns it; the code after the keyring
	// acts on it, the same way bare is learned in one place and acted on in
	// another.
	urlUserOnly := false

	switch {
	case strings.HasPrefix(nameOrURL, "http://"), strings.HasPrefix(nameOrURL, "https://"):
		// Userinfo in the URL is a credential: net/http turns it into a Basic
		// auth header. Record the user name it carries so the connection
		// reports who it is, instead of calling an authenticated session
		// anonymous.
		clean, urlUser, urlSecret := splitURLCredentials(nameOrURL)
		urlUserOnly = urlUser != "" && urlSecret == ""
		switch {
		case urlUserOnly:
			// A URL that names a user and no password is a request to log in
			// as that user, not a request to connect anonymously and remember
			// a name. The password comes from CDB_PASSWORD, then the keyring,
			// then the prompt — the order every other credential follows. The
			// userinfo is cut off the URL, because the credential now travels
			// as a session login rather than as net/http's Basic header.
			profile = config.Profile{Name: "", URL: clean, Auth: "session", Username: urlUser}
		default:
			// Both halves present: net/http turns the userinfo into a Basic
			// header, which is how this has always worked. Neither half: an
			// anonymous URL, and `bare` below decides whether to ask.
			profile = config.Profile{Name: "", URL: nameOrURL, Auth: "none", Username: urlUser}
		}
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
	// A named kind beats the profile and the environment both: it is the most
	// specific thing the operator said, and the same precedence --url has over
	// CDB_URL.
	if over.Kind != "" {
		profile.Auth = over.Kind
		// Only the kinds that have no password to be asked for clear the
		// bare-URL prompt below. "--auth session" on a credential-free URL
		// still has to be asked: clearing it there would dial with an empty
		// password and turn an answerable question into a 401.
		switch over.Kind {
		case string(couch.AuthProxy), string(couch.AuthIAM), string(couch.AuthNone):
			bare = false
		}
	}
	// Roles are proxy's alone: couch.New sends X-Auth-CouchDB-Roles under that
	// kind and no other, so a --roles that rode along with a session or jwt
	// connection would be saved as a claim nothing ever makes.
	if len(over.Roles) > 0 && profile.Auth == string(couch.AuthProxy) {
		profile.Roles = over.Roles
	}
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
	secret, fromEnv := env.SecretFor(profile.Auth)
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

	// #44: a session profile that knows who it is and has no password left to
	// try. CDB_PASSWORD and the keyring have both had their turn above; the
	// operator is the last place to look. Only the password is asked for — the
	// user name is not in doubt, and promptForCredentials would ask for it
	// again with "admin" offered as the default.
	if secret == "" && profile.Auth == string(couch.AuthSession) && profile.Username != "" &&
		s.Prefs.Interactive && !s.Prefs.Anonymous {
		sec, perr := readSecret(s, "Password for "+profile.Username)
		if perr != nil {
			return connection{}, perr
		}
		secret = sec
		bare = false
	}

	// And nothing supplied one: --anonymous was passed, or there is no
	// terminal to ask on. A session login with an empty password can only
	// 401, which would turn "cdb --url http://alice@host ls /" from a working
	// anonymous connection into a failure. Connect the way the URL alone used
	// to, with the user name still recorded, and let the anonymous notice say
	// what happened.
	// fellBack records that the URL's user name was dropped. The connection is
	// as anonymous as the bare-URL one below, so it gets the same notice:
	// silence is how "cdb --url http://alice@host ls /" ends up looking like a
	// logged-in session that is not one. --anonymous asked for this, so it is
	// still quiet.
	fellBack := false
	if secret == "" && urlUserOnly && profile.Auth == string(couch.AuthSession) {
		profile.Auth = string(couch.AuthNone)
		fellBack = !s.Prefs.Anonymous
	}

	// A proxy or IAM profile with no secret anywhere has its own questions:
	// neither kind takes a password, so the bare-URL prompt below would ask
	// the wrong ones. They are the same questions "profiles add" asks, so they
	// are asked in the same place — promptForAuthKind, which also writes the
	// user name and the roles a proxy operator types back into the profile.
	// Without a terminal, nothing is asked: the connection fails with the
	// server's own answer, which the proxy sentence turns into something
	// actionable.
	if secret == "" && s.Prefs.Interactive && !s.Prefs.Anonymous {
		switch profile.Auth {
		case string(couch.AuthProxy), string(couch.AuthIAM):
			sec, perr := promptForAuthKind(s, profile.Auth, &profile)
			if perr != nil {
				return connection{}, perr
			}
			secret = sec
			bare = false
		}
	}

	// A server URL with no credentials anywhere is the trap I3 named: a stock
	// CouchDB answers GET / and GET /_session to an anonymous client with 200,
	// so the connection reports success and then 401s on every command with a
	// sentence about a password nobody supplied. It is handled here rather
	// than in the "connect" command because this is the door every subcommand
	// and the shell's own startup go through — "cdb --url http://host ls /" is
	// the common case, not "cdb connect".
	notice := fellBack
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
	cc, info, err := attemptLogin(ctx, profile, secret, profile.ProxyHash)
	// Only an unpinned proxy profile has a second thing to try. A pinned hash
	// is the answer to the question the probe asks, so asking it again would
	// send the token the operator already ruled out.
	if err == nil || profile.Auth != string(couch.AuthProxy) || profile.ProxyHash != "" || !proxyRejected(err) {
		return cc, info, err
	}
	// The server would not act on the SHA-256 token. That is either a secret
	// that disagrees or a server too old to verify anything but HMAC-SHA1 --
	// CouchDB gained hash_algorithms in 3.3.2 -- and the two are
	// indistinguishable from the answer, since both are an anonymous session.
	// So the other digest is tried once, on a client built for it: the token
	// is computed at construction, and rewriting a live transport would leave
	// the client's own view of itself wrong for the replication endpoints that
	// read it back.
	return attemptLogin(ctx, profile, secret, couch.ProxyHashSHA1)
}

// proxyRejected reports whether err is the 401 a proxy login that the server
// did not act on produces -- the synthetic one below, or a real one from the
// server. Anything else (a refused connection, a 500) is not a reason to try
// the other digest.
func proxyRejected(err error) bool {
	ce, ok := couch.AsError(err)
	return ok && ce.Status == http.StatusUnauthorized && ce.Auth == couch.AuthProxy
}

// attemptLogin is one verification attempt, with proxyHash naming the digest a
// proxy token is computed with ("" meaning the default).
func attemptLogin(ctx context.Context, profile config.Profile, secret, proxyHash string) (*couch.Client, couch.ServerInfo, error) {
	cc, err := couch.New(couch.Config{
		URL:            profile.URL,
		Auth:           couch.AuthKind(profile.Auth),
		Username:       profile.Username,
		Secret:         secret,
		Roles:          profile.Roles,
		ProxyHash:      proxyHash,
		IAMURL:         profile.IAMURL,
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
		// A token 3.5 will not even parse is refused here, before the session
		// check below is reached, so the kind is recorded on this answer too.
		// There is no version to test for the pre-3.1 hint yet.
		return nil, couch.ServerInfo{}, tagAuthKind(err, profile.Auth, "")
	}
	// Spec 6.1: the connection is verified with GET /_session, which is what
	// proves the credentials were accepted; GET / answers for anyone.
	sess, err := cc.Session(ctx)
	if err != nil {
		_ = cc.Close()
		return nil, couch.ServerInfo{}, tagAuthKind(err, profile.Auth, info.Version)
	}
	// A proxy token the server will not accept does not fail: the request is
	// simply anonymous, or -- with a cookie in play -- somebody else. Both are
	// checked, because "a name came back" is not the same as "the name cdb
	// asked for came back".
	if profile.Auth == string(couch.AuthProxy) && (sess.Method != "proxy" || sess.Name != profile.Username) {
		_ = cc.Close()
		e := couch.NewError(http.StatusUnauthorized, "unauthorized",
			"The server did not act on the proxy credentials.", "authenticate",
			couch.UnauthorizedTarget(profile.Username, cc.Host()))
		e.Auth = couch.AuthProxy
		return nil, couch.ServerInfo{}, e
	}
	// Cloudant answers _session with "authenticated":"iam" for a key it
	// accepted. The user name is the service id, which cdb never configured
	// and cannot check, so the method is the whole test.
	if profile.Auth == string(couch.AuthIAM) && sess.Method != "iam" {
		_ = cc.Close()
		e := couch.NewError(http.StatusUnauthorized, "unauthorized",
			"The server did not authenticate the IAM token.", "authenticate",
			couch.UnauthorizedTarget("", cc.Host()))
		e.Auth = couch.AuthIAM
		return nil, couch.ServerInfo{}, e
	}
	// A server with no admins, or a JWT it declines to honour, answers
	// GET /_session with "name": null and a 200. Asking for authentication and
	// silently getting none is a failure, not a connection.
	if sess.Name == "" && profile.Auth != string(couch.AuthNone) && profile.Auth != string(couch.AuthIAM) {
		_ = cc.Close()
		if profile.Auth == string(couch.AuthJWT) {
			// The server did not act on the token: below 3.1 there is no JWT
			// handler to act on it, and above it a token it declines is
			// treated the same way — the request is simply anonymous.
			e := couch.NewError(http.StatusUnauthorized, "unauthorized",
				"The server rejected the token.", "authenticate", "server "+cc.Host())
			e.Auth = couch.AuthJWT
			e.Hint = jwtVersionHint(info.Version)
			return nil, couch.ServerInfo{}, e
		}
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
	// The digest the probe settled on travels back with the profile, so
	// "--save" writes down which one this server verifies instead of making
	// every later connection discover it again.
	if profile.Auth == string(couch.AuthProxy) {
		profile.ProxyHash = cc.ProxyHash()
	}
	if !supportedVersion(info.Version) {
		fmt.Fprintf(s.Stderr, "warning: this server reports CouchDB %s; cdb supports 3.0 through 3.5.\n", info.Version)
	}
	if profile.InsecureTLS {
		fmt.Fprintln(s.Stderr, "warning: TLS certificate verification is disabled for this connection.")
	}
	s.Attach(cc, attachAs)
	return connection{Profile: profile, Secret: secret, Info: info}, nil
}

// validAuthKind reports whether s is one of the kinds couch.New acts on.
func validAuthKind(s string) bool { return config.ValidAuthKind(s) }

// authOverrideFrom reads --auth and --roles off an invocation, rejecting a
// kind cdb does not have a transport for before any network work is done. cmd
// is the command the operator typed, so "profiles add --auth bogus" sends them
// to the help for "profiles" rather than to connect's.
func authOverrideFrom(inv Invocation, cmd string) (authOverride, error) {
	kind := inv.String("auth")
	if kind != "" && !validAuthKind(kind) {
		return authOverride{}, Usagef(cmd, "%q is not an authentication kind; expected session, jwt, proxy, iam or none", kind)
	}
	return authOverride{Kind: kind, Roles: splitRoles(inv.String("roles"))}, nil
}

// splitRoles turns a comma-separated role list into a slice, dropping empty
// entries so "a,,b" and a trailing comma behave as typed rather than claiming
// an empty role.
func splitRoles(raw string) []string {
	var out []string
	for _, r := range strings.Split(raw, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// supportedVersion reports whether v is CouchDB 3.0 through 3.5.
func supportedVersion(v string) bool {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 || parts[0] != "3" {
		return false
	}
	switch parts[1] {
	case "0", "1", "2", "3", "4", "5":
		return true
	}
	return false
}

// jwtVersionHint is the clause added to a rejected-token message when the
// server is too old to have a JWT handler at all: CouchDB gained
// jwt_authentication_handler in 3.1, so 3.0 ignores a bearer token and answers
// as if the request carried no credentials.
// tagAuthKind records the profile's authentication kind on a refusal that does
// not name one, so the JWT and IAM sentences are used instead of advice about a
// password neither kind has. CouchDB 3.5 answers a structurally invalid bearer
// token with 400 bad_request rather than 401, so both statuses are tagged.
func tagAuthKind(err error, auth, version string) error {
	var ce *couch.Error
	if !errors.As(err, &ce) || (ce.Status != http.StatusUnauthorized && ce.Status != http.StatusBadRequest) {
		return err
	}
	switch auth {
	case string(couch.AuthJWT):
		ce.Auth = couch.AuthJWT
		ce.Hint = jwtVersionHint(version)
	case string(couch.AuthIAM):
		ce.Auth = couch.AuthIAM
	}
	return err
}

func jwtVersionHint(version string) string {
	if version != "3.0" && !strings.HasPrefix(version, "3.0.") {
		return ""
	}
	return "JWT authentication needs CouchDB 3.1 or later; this server is " + version + "."
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
			fs.String("auth", "", "authentication kind: session, jwt, proxy, iam or none")
			fs.String("roles", "", "roles to claim under proxy authentication, comma-separated")
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
			// Read the override before the branch: the walk-through is as
			// entitled to --auth and --roles as openProfile is, and a kind cdb
			// has no transport for is a usage error on both paths, raised
			// before anybody is asked to type a URL.
			over, err := authOverrideFrom(inv, "connect")
			if err != nil {
				return nil, err
			}
			var conn connection
			guided := false
			// --url and --profile are a target, so they belong in this guard
			// as much as the argument does: a first run is exactly when an
			// operator passes one, and walking them through a question they
			// have already answered on the command line drops the flag the
			// same way #36 did. openProfile handles both, including asking a
			// bare --url for credentials.
			if arg == "" && s.Prefs.URL == "" && s.Prefs.Profile == "" && !hasAnyProfile() && s.Prefs.Interactive {
				// Spec 6.1: ask, verify, and only then offer to save. Nothing
				// reaches config.toml or the keyring until the server has
				// accepted the answers, so a mistyped password leaves no
				// half-made profile behind.
				p, secret, promptErr := promptForProfile(s, over)
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
				conn, err = openProfileWith(ctx, s, arg, over)
			}
			if err != nil {
				return nil, err
			}
			who := s.Client.Username()
			if who == "" {
				// An IAM connection has no username to report — the
				// credential is an API key — but the account does have a
				// name, and _session is where it comes back as the service
				// id. Asking for it only when there is nothing else to print
				// keeps the extra round trip off every other login, and a
				// server that will not answer still leaves "anonymous",
				// which is the truth for a genuinely anonymous connection.
				if info, serr := s.Client.Session(ctx); serr == nil {
					who = info.Name
				}
			}
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
		Flags: func(fs *pflag.FlagSet) {
			fs.String("auth", "", "authentication kind: session, jwt, proxy, iam or none")
			fs.String("roles", "", "roles to claim under proxy authentication, comma-separated")
		},
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
	over, err := authOverrideFrom(inv, "profiles")
	if err != nil {
		return config.Profile{}, "", err
	}
	if over.Kind != "" {
		profile.Auth = over.Kind
		if profile.Auth == string(couch.AuthProxy) {
			profile.Roles = over.Roles
		}
	}
	if profile.Auth != string(couch.AuthSession) {
		// Only a session profile has a user name and a password to ask for.
		// Every other kind has its own question — a bearer token, an IAM API
		// key, the proxy's three — so they go through the shared per-kind
		// prompt rather than the two questions below, which would take an API
		// key at "Username" and write it into config.toml.
		if !s.Prefs.Interactive || s.Prefs.Anonymous {
			return profile, "", nil
		}
		secret, perr := promptForAuthKind(s, profile.Auth, &profile)
		if perr != nil {
			return config.Profile{}, "", perr
		}
		// Every kind that collected a credential is proved before it is
		// written down. A profile saved unverified is a password in the
		// keyring and a line in config.toml that nobody has tried, and the
		// operator finds out at the next command rather than at the prompt
		// they are still standing at. "none" collected nothing, so there is
		// nothing to prove.
		if profile.Auth != string(couch.AuthNone) {
			// The probe is a copy, because one field on it comes from the
			// environment and must not be written into the saved profile:
			// CDB_IAM_URL points the token exchange at a non-default endpoint
			// (a Cloudant Dedicated instance, or a stub in a test), and
			// without it the verification of an iam profile would call IBM's
			// public endpoint whatever the operator's shell says. Env.Apply
			// does the same layering on the connect path; profileToAdd does
			// not go through it.
			probe := profile
			if probe.Auth == string(couch.AuthIAM) && probe.IAMURL == "" {
				probe.IAMURL = config.LoadEnv(CurrentDeps().LookupEnv).IAMURL
			}
			cc, _, verr := verifyLogin(ctx, probe, secret)
			if verr != nil {
				return config.Profile{}, "", verr
			}
			if profile.Auth == string(couch.AuthProxy) {
				// A proxy profile also has a digest to settle: write down the
				// one the server actually verified.
				profile.ProxyHash = cc.ProxyHash()
			}
			_ = cc.Close()
		}
		return profile, secret, nil
	}
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
