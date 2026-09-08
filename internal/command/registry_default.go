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
	r.Register(Cat())
	r.Register(Put())
	r.Register(Rm())
	r.Register(Edit())
	r.Register(Attach())
	r.Register(Fetch())
	r.Register(Find())
	r.Register(Query())
	r.Register(Mkdir())
	r.Register(Rmdir())
	r.Register(Cp())
	r.Register(Conflicts())
	r.Register(Resolve())
	return r
}
