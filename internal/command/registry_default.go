package command

// Default returns a registry holding every built-in command.
func Default() *Registry {
	r := NewRegistry()
	r.Register(Pwd())
	return r
}
