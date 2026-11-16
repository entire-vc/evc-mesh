package catalog

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// CodeRunnerAhead refuses a Runner update or rollback whose target is older
// than the version its controllers already run.
const CodeRunnerAhead = "extension_runner_ahead"

// RunnerVersions reports the Agent Runner versions this instance's controllers
// run now (their fresh reports), in any order. The Runner is installed on its
// host apart from the catalog, so the catalog row may lag behind it.
type RunnerVersions interface {
	ReportedRunnerVersions(ctx context.Context) ([]string, error)
}

// runnerFloor is what the Runner's controllers report for row: the distinct
// semver versions, oldest first, and the newest of them. Any other extension,
// no source or no report gives nothing, and the row's own version stays the
// only floor.
func (in *Installer) runnerFloor(ctx context.Context, row *domain.InstalledExtension) (versions []string, newest string, err error) {
	if in.RunnerVersions == nil || row.ExtensionID != domain.RunnerExtensionID {
		return nil, "", nil
	}
	raw, err := in.RunnerVersions.ReportedRunnerVersions(ctx)
	if err != nil {
		return nil, "", err
	}
	seen := map[string]bool{}
	for _, v := range raw {
		// A version that is not semver cannot be ordered against a release.
		if seen[v] || !semverValid(v) {
			continue
		}
		seen[v] = true
		versions = append(versions, v)
	}
	sort.SliceStable(versions, func(i, j int) bool { return semverNewer(versions[j], versions[i]) })
	if len(versions) > 0 {
		newest = versions[len(versions)-1]
	}
	return versions, newest, nil
}

func semverValid(v string) bool {
	return v != "" && (semverNewer(v, "0.0.0") || v == "0.0.0")
}

// runnerAhead is the refusal for moving the Runner's row to target while a
// controller already runs running, which is newer.
func runnerAhead(action, target, running string) error {
	return apiErr(http.StatusConflict, CodeRunnerAhead,
		fmt.Sprintf("Agent Runner %s is already running; %s to %s would offer an older release", running, action, target))
}
