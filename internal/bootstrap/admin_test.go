package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/entire-vc/evc-mesh/internal/auth"
)

// harness captures what Admin logged and what it tried to register, so each
// test can assert on the operator-visible outcome rather than on internals.
type harness struct {
	env         map[string]string
	userCount   int
	countErr    error
	registerErr error

	logs       []string
	registered []credentials
}

type credentials struct {
	email    string
	password string
	name     string
}

func (h *harness) deps() Deps {
	return Deps{
		CountUsers: func(context.Context) (int, error) { return h.userCount, h.countErr },
		Register: func(_ context.Context, email, password, name string) error {
			if h.registerErr != nil {
				return h.registerErr
			}
			h.registered = append(h.registered, credentials{email, password, name})
			return nil
		},
		Getenv: func(k string) string { return h.env[k] },
		Logf: func(format string, v ...any) {
			h.logs = append(h.logs, fmt.Sprintf(format, v...))
		},
	}
}

func (h *harness) run() { Admin(context.Background(), h.deps()) }

func (h *harness) output() string { return strings.Join(h.logs, "\n") }

func (h *harness) mustContain(t *testing.T, want string) {
	t.Helper()
	if !strings.Contains(h.output(), want) {
		t.Errorf("expected the log to mention %q.\nGot:\n%s", want, h.output())
	}
}

// --- the four operator-visible situations -----------------------------------

func TestFreshDatabaseWithoutFlagExplainsBothWaysIn(t *testing.T) {
	h := &harness{userCount: 0, env: map[string]string{}}
	h.run()

	if len(h.registered) != 0 {
		t.Fatalf("nothing should be registered without MESH_SEED_ADMIN, got %v", h.registered)
	}
	// The whole point of this branch: never leave the operator without a next step.
	h.mustContain(t, "no users yet")
	h.mustContain(t, "/register")
	h.mustContain(t, "MESH_SEED_ADMIN=true")
}

func TestSeedCreatesAdminAndPrintsGeneratedPasswordOnce(t *testing.T) {
	h := &harness{userCount: 0, env: map[string]string{"MESH_SEED_ADMIN": "true"}}
	h.run()

	if len(h.registered) != 1 {
		t.Fatalf("expected exactly one registration, got %d", len(h.registered))
	}
	got := h.registered[0]
	if got.email != defaultEmail || got.name != defaultName {
		t.Errorf("expected defaults %s/%s, got %s/%s", defaultEmail, defaultName, got.email, got.name)
	}
	// A generated credential that its own validator would reject is a first-boot
	// lockout — this is the pairing that must never drift.
	if err := auth.ValidatePassword(got.password); err != nil {
		t.Errorf("generated password %q fails auth.ValidatePassword: %v", got.password, err)
	}
	h.mustContain(t, "First admin created: "+defaultEmail)
	h.mustContain(t, got.password)
	h.mustContain(t, "shown ONCE")

	if strings.Contains(h.output(), "Admin123") {
		t.Error("the published default password must never appear")
	}
}

func TestSeedUsesSuppliedCredentialsAndDoesNotPrintThem(t *testing.T) {
	h := &harness{userCount: 0, env: map[string]string{
		"MESH_SEED_ADMIN":     "true",
		"MESH_ADMIN_EMAIL":    "ops@example.com",
		"MESH_ADMIN_PASSWORD": "MyChosenPass9",
		"MESH_ADMIN_NAME":     "Ops",
	}}
	h.run()

	if len(h.registered) != 1 {
		t.Fatalf("expected exactly one registration, got %d", len(h.registered))
	}
	got := h.registered[0]
	if got.email != "ops@example.com" || got.password != "MyChosenPass9" || got.name != "Ops" {
		t.Errorf("supplied credentials were not used verbatim: %+v", got)
	}
	// An operator-chosen password is already known to them; echoing it only
	// widens where it leaks.
	if strings.Contains(h.output(), "MyChosenPass9") {
		t.Error("a supplied password must not be echoed to the log")
	}
	h.mustContain(t, "First admin created: ops@example.com")
}

func TestSeedSkippedLoudlyWhenUsersAlreadyExist(t *testing.T) {
	h := &harness{userCount: 3, env: map[string]string{"MESH_SEED_ADMIN": "true"}}
	h.run()

	if len(h.registered) != 0 {
		t.Fatal("the seed must never run against a non-empty database")
	}
	h.mustContain(t, "already has 3 user(s)")
	h.mustContain(t, "zero users")
}

func TestEstablishedInstanceStaysQuiet(t *testing.T) {
	h := &harness{userCount: 3, env: map[string]string{}}
	h.run()

	if len(h.logs) != 0 {
		t.Errorf("a normal restart should log nothing, got:\n%s", h.output())
	}
}

// --- failure paths ----------------------------------------------------------

func TestCountFailureIsReportedNotSwallowed(t *testing.T) {
	h := &harness{countErr: errors.New("connection refused"), env: map[string]string{"MESH_SEED_ADMIN": "true"}}
	h.run()

	if len(h.registered) != 0 {
		t.Fatal("must not attempt a seed when the user count is unknown")
	}
	h.mustContain(t, "could not count users")
	h.mustContain(t, "connection refused")
}

func TestRegisterFailureTellsOperatorHowToRecover(t *testing.T) {
	h := &harness{
		userCount:   0,
		registerErr: errors.New("password too weak"),
		env:         map[string]string{"MESH_SEED_ADMIN": "true", "MESH_ADMIN_PASSWORD": "short"},
	}
	h.run()

	h.mustContain(t, "seed FAILED")
	h.mustContain(t, "password too weak")
	h.mustContain(t, "Nobody can log in yet")
}

func TestFlagMustBeExactlyTrue(t *testing.T) {
	for _, v := range []string{"1", "yes", "TRUE", "True", ""} {
		h := &harness{userCount: 0, env: map[string]string{"MESH_SEED_ADMIN": v}}
		h.run()
		if len(h.registered) != 0 {
			t.Errorf("MESH_SEED_ADMIN=%q should not trigger the seed", v)
		}
	}
}

// --- password generator -----------------------------------------------------

func TestGeneratePasswordAlwaysSatisfiesValidator(t *testing.T) {
	seen := make(map[string]bool)

	for i := 0; i < 200; i++ {
		pw, err := GeneratePassword()
		if err != nil {
			t.Fatalf("GeneratePassword returned an error: %v", err)
		}
		if err := auth.ValidatePassword(pw); err != nil {
			t.Fatalf("generated password %q rejected by auth.ValidatePassword: %v", pw, err)
		}
		if len(pw) != passwordLength {
			t.Fatalf("expected %d characters, got %d (%q)", passwordLength, len(pw), pw)
		}
		if seen[pw] {
			t.Fatalf("duplicate password %q within %d draws — not random", pw, i+1)
		}
		seen[pw] = true
	}
}

func TestGeneratePasswordOmitsAmbiguousCharacters(t *testing.T) {
	const ambiguous = "O0lI1"
	for i := 0; i < 100; i++ {
		pw, err := GeneratePassword()
		if err != nil {
			t.Fatalf("GeneratePassword returned an error: %v", err)
		}
		if strings.ContainsAny(pw, ambiguous) {
			t.Fatalf("password %q contains a character that is easy to misread by hand", pw)
		}
	}
}

func TestGeneratePasswordDoesNotLeadWithAFixedClassPattern(t *testing.T) {
	// Without the shuffle, every password would start upper-lower-digit, which
	// leaks three positions' worth of structure.
	var sawNonUpperFirst bool
	for i := 0; i < 100; i++ {
		pw, err := GeneratePassword()
		if err != nil {
			t.Fatalf("GeneratePassword returned an error: %v", err)
		}
		if pw[0] < 'A' || pw[0] > 'Z' {
			sawNonUpperFirst = true
			break
		}
	}
	if !sawNonUpperFirst {
		t.Error("first character was always uppercase across 100 draws — the shuffle is not working")
	}
}

func TestRandomCharStaysInSet(t *testing.T) {
	const set = "abc123"
	for i := 0; i < 100; i++ {
		c, err := randomChar(set)
		if err != nil {
			t.Fatalf("randomChar returned an error: %v", err)
		}
		if !strings.ContainsRune(set, rune(c)) {
			t.Fatalf("randomChar returned %q, which is outside %q", c, set)
		}
	}
}
