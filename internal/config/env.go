package config

import "strconv"

// Env holds the CDB_* overrides. An override always wins over the config file
// and never touches the keyring.
type Env struct {
	Profile        string
	URL            string
	User           string
	Password       string
	Token          string
	ReplicationURL string
	// IAMKey is CDB_IAM_API_KEY, the IBM Cloud API key. Like every other
	// secret here it bypasses the keyring entirely.
	IAMKey string
	// IAMURL is CDB_IAM_URL, the token endpoint override.
	IAMURL      string
	InsecureTLS *bool
}

// LoadEnv reads the overrides using lookup, which is normally os.LookupEnv.
func LoadEnv(lookup func(string) (string, bool)) Env {
	get := func(k string) string {
		v, _ := lookup(k)
		return v
	}
	e := Env{
		Profile:        get("CDB_PROFILE"),
		URL:            get("CDB_URL"),
		User:           get("CDB_USER"),
		Password:       get("CDB_PASSWORD"),
		Token:          get("CDB_TOKEN"),
		ReplicationURL: get("CDB_REPLICATION_URL"),
		IAMKey:         get("CDB_IAM_API_KEY"),
		IAMURL:         get("CDB_IAM_URL"),
	}
	if raw, ok := lookup("CDB_INSECURE_TLS"); ok && raw != "" {
		if b, err := strconv.ParseBool(raw); err == nil {
			e.InsecureTLS = &b
		}
	}
	return e
}

// Apply layers the overrides onto a profile.
func (e Env) Apply(p Profile) Profile {
	if e.URL != "" {
		p.URL = e.URL
	}
	if e.ReplicationURL != "" {
		p.ReplicationURL = e.ReplicationURL
	}
	if e.IAMURL != "" {
		p.IAMURL = e.IAMURL
	}
	if e.User != "" {
		p.Username = e.User
	}
	if e.InsecureTLS != nil {
		p.InsecureTLS = *e.InsecureTLS
	}
	switch {
	case e.IAMKey != "":
		// The key is both the selector and the credential, so a shell that
		// exports one has said which kind it means as plainly as a profile
		// key would.
		p.Auth = "iam"
	case p.Auth == "proxy" || p.Auth == "iam":
		// A profile that names proxy or IAM authentication keeps it. Neither
		// kind takes its credential from CDB_TOKEN, and CDB_PASSWORD — which a
		// shell may export for an entirely different server — must not turn a
		// proxy profile into a session one behind the operator's back. The
		// secret itself still comes from Secret() below, so CDB_PASSWORD can
		// still supply a proxy profile's shared secret without changing what
		// the profile is.
	case e.Token != "":
		p.Auth = "jwt"
	case e.Password != "":
		p.Auth = "session"
	}
	if p.Auth == "" {
		p.Auth = "session"
	}
	return p
}

// Secret returns the environment-supplied secret when the kind is not yet
// settled, which is how the bare-URL guard in internal/command asks "are there
// credentials in the environment at all".
func (e Env) Secret() (string, bool) { return e.SecretFor("") }

// SecretFor is Secret for a profile whose kind is already known.
//
// CDB_IAM_API_KEY comes first, so the variable that selects "iam" in Apply is
// also the one that supplies the credential. The kind then matters for exactly
// one case: spec §4.1 says CDB_TOKEN is not consulted for "iam". An IAM bearer
// is minted by the client, never typed by a person, so a CDB_TOKEN a shell
// exported for some other server must not be handed to Cloudant as an API key
// — and, worse, must not count as "the environment supplied one" and suppress
// the keyring read that would have found the real key.
func (e Env) SecretFor(kind string) (string, bool) {
	if e.IAMKey != "" {
		return e.IAMKey, true
	}
	if kind == "iam" {
		return "", false
	}
	if e.Token != "" {
		return e.Token, true
	}
	if e.Password != "" {
		return e.Password, true
	}
	return "", false
}
