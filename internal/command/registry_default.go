package command

// Default returns a registry holding every built-in command.
func Default() *Registry {
	r := NewRegistry()
	r.Register(Pwd())
	r.Register(Connect())
	r.Register(Profiles())
	r.Register(SessionCmd())
	r.Register(Cd())
	r.Register(Ls())
	r.Register(Info())
	return r
}
