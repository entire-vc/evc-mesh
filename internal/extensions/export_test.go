package extensions

import "time"

// SetClock replaces the health-cache clock in tests.
func SetClock(r *Registry, now func() time.Time) { r.health.now = now }
