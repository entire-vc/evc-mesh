package extensions

// Deps are the stores every builtin provider reads settings from.
type Deps struct {
	Conns ConnectionStore
	Inst  InstanceStore
	// Allow is the instance allow-list; nil allows everything.
	Allow AllowList
}
