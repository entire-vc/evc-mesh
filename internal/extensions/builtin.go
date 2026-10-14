package extensions

// Deps are the stores every builtin provider reads settings from.
type Deps struct {
	Conns ConnectionStore
	Inst  InstanceStore
}

// BuiltinProviders returns the builtin providers wired to d. Providers moved
// onto the registry (W1.5+) are added here, so main registers them and the
// status/runtime parity test (status_test.go) picks them up automatically.
func BuiltinProviders(d Deps) []Provider {
	return nil
}
