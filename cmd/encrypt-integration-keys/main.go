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

// maxUnwrapLayers bounds the loop in unwrapLayers. Real data has never needed
// more than 2 (the 2026-08-20 double-encryption incident, task f824032d); the
// margin exists only so genuinely corrupt data fails loudly instead of
// looping forever.
const maxUnwrapLayers = 4

// unwrapLayers decrypts stored repeatedly until the result is no longer
// itself ciphertext, returning the true plaintext and how many layers were
// stripped to get there.
//
// This exists because a single encryption.IsEncrypted(stored) check — "does
// it carry the enc:v1: prefix?" — cannot distinguish three states that all
// look identical to a prefix-only check: genuine plaintext (0 layers),
// legacy encrypted-but-unprefixed data written before the prefix existed —
// pkg/encryption's "state 3" (1 layer, needs re-wrapping with the prefix),
// and data that was encrypted twice (2+ layers). The 2026-08-20 incident was
// exactly the second case misclassified as the first: a row Upserted through
// the pre-#657 repo code (encrypted, no prefix yet) was mistaken for
// plaintext by the old prefix-only check and encrypted a second time on top
// of its own ciphertext. Task f824032d.
func unwrapLayers(stored string) (plain string, layers int, err error) {
	cur := stored
	for layers = 0; layers < maxUnwrapLayers; layers++ {
		if encryption.IsEncrypted(cur) {
			next, derr := encryption.Decrypt(cur)
			if derr != nil {
				return "", layers, derr
			}
			cur = next
			continue
		}
		// Untagged: Decrypt tries base64-decode + AEAD-open and returns the
		// input unchanged, with no error, when either step fails. That
		// unchanged result is the signal that cur is true plaintext rather
		// than another layer — the untagged branch never errors, so this
		// loop only ever exits via convergence or the layer cap.
		next, _ := encryption.Decrypt(cur)
		if next == cur {
			return cur, layers, nil
		}
		cur = next
	}
	return "", layers, fmt.Errorf(
		"did not converge after %d layers — possible corrupt data or a credential that happens to look like ciphertext",
		maxUnwrapLayers)
}

// pendingRow pairs a row that needs a rewrite with its already-unwrapped
// plaintext, so the write loop below never has to guess which of r.stored /
// r.plain is safe to feed to Encrypt.
type pendingRow struct {
	row
	plain  string
	layers int
}

// backfill is separated from run so it can be driven against a mock database:
// the round-trip check and the abort paths are the parts worth testing, and
// they are the parts hardest to reach through a real connection.
func backfill(db *sql.DB, dryRun bool, projectFilter string) error {
	rows, err := load(db, projectFilter)
	if err != nil {
		return err
	}

	var pending []pendingRow
	alreadyEncrypted := 0
	for _, r := range rows {
		plain, layers, uerr := unwrapLayers(r.stored)
		if uerr != nil {
			return fmt.Errorf("row %s: %w", r.id, uerr)
		}
		// Exactly one layer, and the stored form already carries the
		// current prefix: this row is already in the target shape. Leave
		// it alone — a backfill that rewrites (and re-nonces) rows it
		// doesn't need to touch is a backfill nobody trusts to re-run.
		if layers == 1 && encryption.IsEncrypted(r.stored) {
			alreadyEncrypted++
			continue
		}
		// layers == 0: genuine plaintext, first encryption.
		// layers >= 2: over-encrypted (the f824032d bug shape) — unwrapLayers
		// already recovered the true plaintext underneath; encrypt that,
		// not r.stored.
		pending = append(pending, pendingRow{row: r, plain: plain, layers: layers})
	}

	fmt.Printf("rows with a credential: %d\n", len(rows))
	fmt.Printf("  already encrypted   : %d\n", alreadyEncrypted)
	fmt.Printf("  to fix              : %d\n", len(pending))

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
		sealed, err := encryption.Encrypt(r.plain)
		if err != nil {
			return fmt.Errorf("row %s: encrypt: %w", r.id, err)
		}
		// Prove the value survives the round trip BEFORE it replaces the only
		// copy that exists. Some of these credentials are live.
		back, err := encryption.Decrypt(sealed)
		if err != nil {
			return fmt.Errorf("row %s: verify decrypt: %w", r.id, err)
		}
		if back != r.plain {
			return errors.New("row " + r.id + ": round trip did not reproduce the original value — aborting, no rows changed")
		}
		if !encryption.IsEncrypted(sealed) {
			return errors.New("row " + r.id + ": produced value is not marked encrypted — aborting")
		}

		// Never print r.plain itself — only its shape (length + a short,
		// non-identifying prefix), which is what an operator or an
		// acceptance check needs to confirm the recovered value looks like a
		// real credential rather than leftover ciphertext.
		plainPrefix := r.plain
		if len(plainPrefix) > 9 {
			plainPrefix = plainPrefix[:9]
		}
		fmt.Printf("  %s project=%s type=%s: stored %d chars (%d layer(s)) -> plaintext %d chars, prefix %q -> resealed %d chars\n",
			r.id, r.projectID, r.intType, len(r.stored), r.layers, len(r.plain), plainPrefix, len(sealed))

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

// verify re-reads through a fresh query and asserts every row is in the
// canonical single-encrypted shape — not merely "carries the prefix", which
// a double-encrypted row also does. That stronger check is what makes this
// tool's own re-run the idempotency proof for task f824032d's acceptance
// criterion #3, rather than something argued from reading the diff.
func verify(db *sql.DB, projectFilter string) error {
	rows, err := load(db, projectFilter)
	if err != nil {
		return err
	}
	plaintextLeft, overEncryptedLeft := 0, 0
	for _, r := range rows {
		_, layers, uerr := unwrapLayers(r.stored)
		if uerr != nil {
			return fmt.Errorf("post-check: row %s: %w", r.id, uerr)
		}
		switch {
		case layers == 0:
			plaintextLeft++
		case layers >= 2:
			overEncryptedLeft++
		}
	}
	if plaintextLeft != 0 {
		return fmt.Errorf("post-check: %d row(s) still hold a plaintext credential", plaintextLeft)
	}
	if overEncryptedLeft != 0 {
		return fmt.Errorf("post-check: %d row(s) are still over-encrypted (2+ layers)", overEncryptedLeft)
	}
	fmt.Printf("post-check: all %d row(s) with a credential are correctly single-encrypted\n", len(rows))
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
