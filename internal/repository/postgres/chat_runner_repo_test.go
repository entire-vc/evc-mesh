package postgres

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
)

func TestTruncateUTF8KeepsWholeCharacters(t *testing.T) {
	long := strings.Repeat("я", 400) // 800 bytes
	got := truncateUTF8(long, 500)
	if len(got) > 500 || !utf8.ValidString(got) {
		t.Fatalf("len=%d valid=%v", len(got), utf8.ValidString(got))
	}
	if got != strings.Repeat("я", 250) {
		t.Fatalf("unexpected cut: %d bytes", len(got))
	}
	if short := truncateUTF8("ok", 500); short != "ok" {
		t.Fatalf("short string changed: %q", short)
	}
}

// Without an agent identity the registry refuses before it touches the database.
func TestChatRunnerRegistryRefusesNilAgent(t *testing.T) {
	r := &ChatRunnerRepo{}
	ctx := context.Background()
	if _, _, err := r.RegisterRunner(ctx, uuid.Nil, "x"); err == nil {
		t.Fatal("register without an agent must fail")
	}
	if _, err := r.AuthenticateRunner(ctx, uuid.Nil, ChatRunnerTokenPrefix+"x"); err == nil {
		t.Fatal("authenticate without an agent must fail")
	}
	if err := r.RevokeRunner(ctx, uuid.Nil, uuid.New()); err == nil {
		t.Fatal("revoke without an agent must fail")
	}
	if _, err := r.AuthenticateRunner(ctx, uuid.New(), "not-a-runner-token"); err == nil {
		t.Fatal("a bearer without the runner prefix must fail")
	}
}
