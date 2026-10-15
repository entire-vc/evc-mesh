// Package builtin lists the builtin extension providers. It sits above the
// extension packages (they import internal/extensions, so internal/extensions
// cannot import them back). Each transfer card (W1.6+) adds one line.
package builtin

import (
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/spark"
)

// Providers returns the builtin providers wired to d. main registers them and
// the status/runtime parity test picks them up automatically.
func Providers(d extensions.Deps) []extensions.Provider {
	return []extensions.Provider{
		spark.New(d),
	}
}
