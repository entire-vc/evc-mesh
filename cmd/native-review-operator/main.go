// Command native-review-operator is the operator-only path for the native review
// trust anchors. There is no public enrollment API: enrollment and revocation
// run host-locally with database access and an explicit operator identity.
//
//	native-review-operator enroll --operator <uuid> --manifest m.json --issuer-pubkey-file issuer.pub [--journal j.jsonl]
//	native-review-operator revoke --operator <uuid> --enrollment <uuid> --reason "..." [--journal j.jsonl]
//
// The issuer public key (the pin) comes from --issuer-pubkey-file, never from the
// manifest. Nothing secret is read or printed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: native-review-operator enroll|revoke [flags]")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	operator := fs.String("operator", "", "operator agent UUID")
	manifest := fs.String("manifest", "", "enrollment manifest (enroll)")
	pubFile := fs.String("issuer-pubkey-file", "", "issuer public key file (enroll)")
	enrollment := fs.String("enrollment", "", "enrollment UUID (revoke)")
	reason := fs.String("reason", "", "revocation reason (revoke)")
	journal := fs.String("journal", "", "append-only JSONL provenance journal")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	op, err := uuid.Parse(*operator)
	if err != nil || op == uuid.Nil {
		return fmt.Errorf("--operator must be a UUID")
	}
	cfg := config.Load()
	db, err := sqlx.Connect("postgres", cfg.Database.DSN())
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close()
	repo := postgres.NewNativeReviewRepo(db, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch args[0] {
	case "enroll":
		return enroll(ctx, repo, op, *manifest, *pubFile, *journal)
	case "revoke":
		id, perr := uuid.Parse(*enrollment)
		if perr != nil || *reason == "" {
			return fmt.Errorf("--enrollment UUID and --reason are required")
		}
		if err := repo.RevokeEnrollment(ctx, id, op, *reason); err != nil {
			return err
		}
		return record(*journal, map[string]any{"op": "revoke", "enrollment_id": id, "operator": op, "reason": *reason})
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func enroll(ctx context.Context, repo *postgres.NativeReviewRepo, op uuid.UUID, manifest, pubFile, journal string) error {
	if manifest == "" || pubFile == "" {
		return fmt.Errorf("--manifest and --issuer-pubkey-file are required")
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		return err
	}
	m, err := postgres.ParseEnrollmentManifest(data)
	if err != nil {
		return err
	}
	key, err := postgres.ReadIssuerPublicKeyFile(pubFile)
	if err != nil {
		return err
	}
	e, err := m.Enrollment(key)
	if err != nil {
		return err
	}
	created, got, err := repo.EnrollAsOperator(ctx, op, e)
	if err != nil {
		return err
	}
	fmt.Printf("enrollment %s created=%t\n", got.ID, created)
	return record(journal, map[string]any{"op": "enroll", "enrollment_id": got.ID, "created": created, "operator": op,
		"issuer_actor_id": got.IssuerActorID, "issuer_key": got.IssuerKey, "authority_id": got.AuthorityID,
		"authority_sha256": got.AuthoritySHA, "root_disposition_sha256": got.RootDispositionSHA})
}

func record(path string, v map[string]any) error {
	if path == "" {
		return nil
	}
	v["at"] = time.Now().UTC().Format(time.RFC3339)
	line, _ := json.Marshal(v)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}
