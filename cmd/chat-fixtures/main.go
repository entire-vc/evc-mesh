// Command chat-fixtures seeds and removes the isolated agent-chat CI fixtures
// in the e2e-ci-sandbox workspace: users A (member), B (member), C (admin),
// V (viewer) and the agents Claude, Codex and Offline.
//
// Passwords come from the environment variables named in
// postgres.ChatFixtureUsers (E2E_CHAT_PASSWORD_A/B/C/V, masked+protected CI
// variables). Their values are never printed; the report lists ids only.
//
// Usage:
//
//	chat-fixtures seed      # idempotent
//	chat-fixtures cleanup   # removes exactly the fixtures, idempotent
//
// DATABASE_URL (or DB_* parts) selects the database. Any workspace other than
// e2e-ci-sandbox is refused before the database is opened.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func main() {
	workspace := flag.String("workspace", postgres.ChatFixturesWorkspaceSlug, "workspace slug (only e2e-ci-sandbox is accepted)")
	flag.Parse()
	if flag.NArg() != 1 || (flag.Arg(0) != "seed" && flag.Arg(0) != "cleanup") {
		log.Fatal("usage: chat-fixtures [--workspace e2e-ci-sandbox] seed|cleanup")
	}
	if err := run(flag.Arg(0), *workspace); err != nil {
		log.Fatalf("FAILED: %v", err)
	}
}

func run(cmd, workspace string) error {
	if workspace != postgres.ChatFixturesWorkspaceSlug {
		return fmt.Errorf("REFUSED: %w: %q", postgres.ErrChatFixturesRefused, workspace)
	}
	passwords := map[string]string{}
	if cmd == "seed" {
		for _, u := range postgres.ChatFixtureUsers {
			passwords[u.Key] = os.Getenv(u.PasswordEnv)
			if passwords[u.Key] == "" {
				return fmt.Errorf("variable %s is not set", u.PasswordEnv)
			}
		}
	}
	dsn, err := buildDSN()
	if err != nil {
		return err
	}
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	if cmd == "cleanup" {
		cleaned, cerr := postgres.CleanupChatFixtures(ctx, db, workspace)
		if cerr != nil {
			return cerr
		}
		fmt.Printf("cleanup workspace=%s removed=%d\n", workspace, cleaned.Removed)
		return nil
	}
	rep, err := postgres.SeedChatFixtures(ctx, db, workspace, passwords)
	if err != nil {
		return err
	}
	fmt.Printf("seed workspace=%s project=%s created=%d\n", workspace, rep.ProjectID, rep.Created)
	for _, u := range postgres.ChatFixtureUsers {
		fmt.Printf("  user %s %-5s id=%s login=%s password_env=%s\n", u.Key, u.Role, rep.Users[u.Key], postgres.ChatFixtureEmail(u.Key), u.PasswordEnv)
	}
	for _, a := range postgres.ChatFixtureAgents {
		fmt.Printf("  agent %-8s id=%s\n", a.Key, rep.Agents[a.Key])
	}
	return nil
}

func buildDSN() (string, error) {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn, nil
	}
	pw := os.Getenv("DB_PASSWORD")
	if pw == "" {
		return "", errors.New("set DATABASE_URL, or DB_HOST/DB_PORT/DB_USER/DB_PASSWORD/DB_NAME")
	}
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		getenv("DB_HOST", "localhost"), getenv("DB_PORT", "5432"), getenv("DB_USER", "mesh"),
		pw, getenv("DB_NAME", "mesh"), getenv("DB_SSL_MODE", "disable")), nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
