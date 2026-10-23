// Package builtin lists the builtin extension providers. It sits above the
// extension packages (they import internal/extensions, so internal/extensions
// cannot import them back). Each transfer card (W1.6+) adds one line.
package builtin

import (
	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/agentchat"
	"github.com/entire-vc/evc-mesh/internal/extensions/github"
	"github.com/entire-vc/evc-mesh/internal/extensions/gitlab"
	"github.com/entire-vc/evc-mesh/internal/extensions/runner"
	"github.com/entire-vc/evc-mesh/internal/extensions/spark"
	teamrelay "github.com/entire-vc/evc-mesh/internal/extensions/team-relay"
	"github.com/entire-vc/evc-mesh/internal/extensions/telegram"
)

// Providers returns the builtin providers wired to d. main registers them and
// the status/runtime parity test picks them up automatically.
func Providers(d extensions.Deps) []extensions.Provider {
	return []extensions.Provider{
		agentchat.New(d),
		github.New(d),
		gitlab.New(d),
		runner.New(d),
		spark.New(d),
		telegram.New(d),
		teamrelay.New(d),
	}
}
