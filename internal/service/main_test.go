package service

import (
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// TestMain overrides two package-level vars that are real work in production but
// pure friction in tests, for the whole package's test run. Both were investigated
// as part of the same underlying symptom: `internal/service` (~1800 tests) running
// right at, and occasionally past, the 180s silence threshold of the CI stall
// watchdog (`scripts/ci/go-test-stall-watchdog.sh`) under `go test -race` — see
// #54bbfbc3 and #6ae6f813.
//
//  1. embedRetryBackoff (memory_service.go) — embedWithRetry's real 1s/2s
//     time.After() backoff, hit by every test that drives a failing embedder
//     through BatchEmbed/BackfillChunks/embedChunked, plus Remember()'s
//     fire-and-forget `go s.embedAndStore(...)` (memory_service.go:718,1873).
//     This is what made "TestBatchEmbed_Chunked_..." appear stuck at
//     memory_service.go:2650 under -race in the original CI failure — it
//     wasn't a deadlock, see #54bbfbc3's task comments for the two re-runs
//     that each caught a DIFFERENT test "hung" at the same silence threshold.
//
//  2. bcryptCost (agent_service.go) and userPasswordBcryptCost
//     (workspace_member_service.go) — the real cost-12/cost-10 bcrypt work
//     factors, hit by ~91 Register/Authenticate call sites plus every
//     AcceptInvite/AddMemberWithCreate test. Measured (#6ae6f813): one
//     isolated test doing exactly one Register + one Authenticate
//     (TestAuthenticate_LegacyRowRejectsAWrongKeyAndBackfillsNothing) took
//     4.4s under `-race`, every run; TestAcceptInvite_UsernameMatchesRegis-
//     tration alone was 5.43s. Together these two cost factors accounted for
//     nearly all of the package's ~190s full -race runtime — after
//     overriding both (on top of #1), the full package (`-race -v -count=1`)
//     dropped to 6.6s, coverage unchanged (72.3% before and after; measured
//     on a clean checkout of origin/main vs this branch).
//
// All three are package-level `var`s, not `const`s/literals, specifically so
// this override can exist; production code paths read the same identifiers
// and are never told apart from test code — nothing in the production build
// ever reassigns them.
func TestMain(m *testing.M) {
	embedRetryBackoff = func(int) time.Duration { return time.Millisecond }
	bcryptCost = bcrypt.MinCost
	userPasswordBcryptCost = bcrypt.MinCost
	os.Exit(m.Run())
}
