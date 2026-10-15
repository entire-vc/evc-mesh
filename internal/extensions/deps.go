package extensions

// Deps are the stores every builtin provider reads settings from.
type Deps struct {
	Conns ConnectionStore
	Inst  InstanceStore
}
