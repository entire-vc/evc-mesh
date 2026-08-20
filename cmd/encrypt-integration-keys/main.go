// Command encrypt-integration-keys rewrites plaintext credentials in
// project_integrations.agent_key through pkg/encryption, so that rows written
// before encryption was configured — or written by direct SQL, which never
// reaches the application layer at all — end up in the same encrypted form as
// anything the API stores today.
//
// Why a one-off tool rather than a migration: the AES key lives in the process
// environment, not in Postgres, so no SQL migration can perform this rewrite.
//
// The rewrite is verified before it is committed. For every row the tool
// encrypts the stored value, decrypts the result, and requires the round trip
// to reproduce the original byte-for-byte; a single mismatch aborts the whole
// transaction. That matters because some of these credentials are live: a
// backfill that silently corrupted one would take a working integration down
// with an authentication error that points at the remote party.
//
// Usage:
//
//	encrypt-integration-keys --dry-run    # report only, no writes
//	encrypt-integration-keys              # rewrite, in one transaction
//	encrypt-integration-keys --project <uuid>
//
// Credential values are never printed — only lengths and shapes.
package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	_ "github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

type row struct {
	id        string
	projectID string
	intType   string
	stored    string
}

func main() {
	dryRun := flag.Bool("dry-run", false, "report what would change without writing")
	projectID := flag.String("project", "", "restrict to a single project UUID (default: all)")
	flag.Parse()

	if err := run(*dryRun, *projectID); err != nil {
		log.Fatalf("FAILED: %v", err)
	}
}

func run(dryRun bool, projectFilter string) error {
	if err := requireKey(); err != nil {
		return err
	}

	db, err := sql.Open("postgres", buildDSN())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = db.Close() }()
	if pingErr := db.Ping(); pingErr != nil {
		return fmt.Errorf("ping db: %w", pingErr)
	}

	return backfill(db, dryRun, projectFilter)
}

// requireKey refuses the run when encryption is not actually available. Without
// this the tool would re-encrypt every row to itself — Encrypt degrades to a
// passthrough — and report a successful backfill.
func requireKey() error {
	state, _ := encryption.Status()
	if state != encryption.KeyOK {
		return fmt.Errorf("%s is %s — refusing to run: without a usable key this tool "+
			"would rewrite every row to itself and report success", encryption.EnvKey, state)
	}
	return nil
}

// backfill is separated from run so it can be driven against a mock database:
// the round-trip check and the abort paths are the parts worth testing, and
// they are the parts hardest to reach through a real connection.
func backfill(db *sql.DB, dryRun bool, projectFilter string) error {
	rows, err := load(db, projectFilter)
	if err != nil {
		return err
	}

	var pending []row
	alreadyEncrypted := 0
	for _, r := range rows {
		if encryption.IsEncrypted(r.stored) {
			alreadyEncrypted++
			continue
		}
		pending = append(pending, r)
	}

	fmt.Printf("rows with a credential: %d\n", len(rows))
	fmt.Printf("  already encrypted   : %d\n", alreadyEncrypted)
	fmt.Printf("  to encrypt          : %d\n", len(pending))

	if len(pending) == 0 {
		fmt.Println("nothing to do")
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, r := range pending {
		sealed, err := encryption.Encrypt(r.stored)
		if err != nil {
			return fmt.Errorf("row %s: encrypt: %w", r.id, err)
		}
		// Prove the value survives the round trip BEFORE it replaces the only
		// copy that exists. Some of these credentials are live.
		back, err := encryption.Decrypt(sealed)
		if err != nil {
			return fmt.Errorf("row %s: verify decrypt: %w", r.id, err)
		}
		if back != r.stored {
			return errors.New("row " + r.id + ": round trip did not reproduce the original value — aborting, no rows changed")
		}
		if !encryption.IsEncrypted(sealed) {
			return errors.New("row " + r.id + ": produced value is not marked encrypted — aborting")
		}

		fmt.Printf("  %s project=%s type=%s: %d chars -> %d chars (%s)\n",
			r.id, r.projectID, r.intType, len(r.stored), len(sealed), encryption.Prefix+"…")

		if dryRun {
			continue
		}
		res, err := tx.Exec(
			`UPDATE project_integrations SET agent_key = $1 WHERE id = $2 AND agent_key = $3`,
			sealed, r.id, r.stored)
		if err != nil {
			return fmt.Errorf("row %s: update: %w", r.id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("row %s: rows affected: %w", r.id, err)
		}
		if n != 1 {
			// The WHERE clause pins the pre-image, so 0 here means the row was
			// changed by someone else between the read and the write.
			return fmt.Errorf("row %s: expected 1 row updated, got %d — concurrent modification, aborting", r.id, n)
		}
	}

	if dryRun {
		fmt.Println("\nDRY RUN — no rows written. Every value above round-tripped successfully.")
		return nil
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	fmt.Printf("\nencrypted %d row(s)\n", len(pending))
	return verify(db, projectFilter)
}

// verify re-reads through a fresh query and asserts that no unencrypted
// credential is left behind, so the exit code reflects the end state rather
// than the fact that the UPDATEs did not error.
func verify(db *sql.DB, projectFilter string) error {
	rows, err := load(db, projectFilter)
	if err != nil {
		return err
	}
	left := 0
	for _, r := range rows {
		if !encryption.IsEncrypted(r.stored) {
			left++
		}
	}
	if left != 0 {
		return fmt.Errorf("post-check: %d row(s) still hold a plaintext credential", left)
	}
	fmt.Printf("post-check: all %d row(s) with a credential are encrypted\n", len(rows))
	return nil
}

func load(db *sql.DB, projectFilter string) ([]row, error) {
	q := `SELECT id, project_id, type, agent_key FROM project_integrations
	       WHERE agent_key IS NOT NULL AND agent_key <> ''`
	args := []any{}
	if projectFilter != "" {
		q += ` AND project_id = $1`
		args = append(args, projectFilter)
	}
	q += ` ORDER BY created_at`

	res, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("select: %w", err)
	}
	defer func() { _ = res.Close() }()

	var out []row
	for res.Next() {
		var r row
		if err := res.Scan(&r.id, &r.projectID, &r.intType, &r.stored); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, r)
	}
	return out, res.Err()
}

func buildDSN() string {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn
	}
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		getenv("DB_HOST", "localhost"),
		getenv("DB_PORT", "5437"),
		getenv("DB_USER", "mesh"),
		getenv("DB_PASSWORD", "mesh"),
		getenv("DB_NAME", "mesh"),
		getenv("DB_SSL_MODE", "disable"),
	)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
