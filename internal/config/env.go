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
	InsecureTLS    *bool
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
	if e.User != "" {
		p.Username = e.User
	}
	if e.InsecureTLS != nil {
		p.InsecureTLS = *e.InsecureTLS
	}
	switch {
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

// Secret returns the environment-supplied secret, preferring CDB_TOKEN so it
// agrees with Apply: whichever one selects the auth kind is also the one
// handed over as the credential.
func (e Env) Secret() (string, bool) {
	if e.Token != "" {
		return e.Token, true
	}
	if e.Password != "" {
		return e.Password, true
	}
	return "", false
}
