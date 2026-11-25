package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// lead-accept: the host-local operator command for lead-accept principals
// (docs/lead-accept-context.md). It runs instead of the server when the first
// argument is "lead-accept", with the environment of mesh-api.service:
//
//	mesh-api lead-accept issue  --kind reader --operator <uuid> --name <name>
//	                            --request-file <request.json> --token-file <new path>
//	mesh-api lead-accept issue  --kind publisher --operator <uuid> --name <name> --token-file <new path>
//	mesh-api lead-accept rotate --operator <uuid> --name <name> --token-file <new path> [--grace 1h]
//	mesh-api lead-accept revoke --operator <uuid> --name <name> --reason "<why>"
//	mesh-api lead-accept show   --operator <uuid> --name <name>
//
// A reader is bound, at issue and for good, to the exact request of
// --request-file and to the ContentHub project of the Mesh workspace
// (domain.LeadAcceptWorkspaceID / LeadAcceptEvidenceProjectID); stdout names
// its id and binding_sha256. A token is written only to --token-file, created
// new with mode 0600 (an existing file is refused), and is never printed.
// There is no public API that issues one.

const leadAcceptUsage = "usage: mesh-api lead-accept issue|rotate|revoke|show [flags] (see docs/lead-accept-context.md)"

// leadAcceptCLI is the command with its database and clock handed in, so a
// test drives the same code as the binary.
type leadAcceptCLI struct {
	repo   *postgres.LeadAcceptRepo
	stdout io.Writer
	now    func() time.Time
	// Reader scope; the binary uses the contract's workspace and ContentHub
	// project, a test its own.
	workspaceID       uuid.UUID
	evidenceProjectID uuid.UUID
}

func runLeadAcceptMain(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, leadAcceptUsage)
		return 2
	}
	cfg := config.Load()
	db, err := sqlx.Connect("postgres", cfg.Database.DSN())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: database:", err)
		return 1
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cli := &leadAcceptCLI{repo: postgres.NewLeadAcceptRepo(db), stdout: os.Stdout, now: time.Now,
		workspaceID: domain.LeadAcceptWorkspaceID, evidenceProjectID: domain.LeadAcceptEvidenceProjectID}
	if err := cli.run(ctx, args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func (cli *leadAcceptCLI) run(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New(leadAcceptUsage)
	}
	cmd := args[0]
	switch cmd {
	case "issue", "rotate", "revoke", "show":
	default:
		return fmt.Errorf("unknown command %q; %s", cmd, leadAcceptUsage)
	}
	fs := flag.NewFlagSet("lead-accept "+cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	operator := fs.String("operator", "", "operator agent UUID (all commands)")
	name := fs.String("name", "", "principal name")
	kind := fs.String("kind", "", "reader or publisher (issue)")
	requestFile := fs.String("request-file", "", "request JSON a reader is bound to (issue --kind reader)")
	tokenFile := fs.String("token-file", "", "new file to write the token to, mode 0600 (issue, rotate)")
	grace := fs.Duration("grace", domain.LeadAcceptDefaultGrace, "how long the previous token keeps working (rotate)")
	reason := fs.String("reason", "", "why (revoke)")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%v; %s", err, leadAcceptUsage)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q; %s", fs.Arg(0), leadAcceptUsage)
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	allowed := map[string][]string{
		"issue":  {"operator", "name", "kind", "request-file", "token-file"},
		"rotate": {"operator", "name", "token-file", "grace"},
		"revoke": {"operator", "name", "reason"},
		"show":   {"operator", "name"},
	}[cmd]
	for f := range set {
		ok := false
		for _, a := range allowed {
			ok = ok || a == f
		}
		if !ok {
			return fmt.Errorf("--%s is not a flag of %s", f, cmd)
		}
	}
	if *name == "" {
		return errors.New("--name is required")
	}
	op, parseErr := uuid.Parse(*operator)
	if parseErr != nil || op == uuid.Nil {
		return errors.New("--operator must be the operator's UUID")
	}
	switch cmd {
	case "issue":
		in := postgres.LeadAcceptIssue{Name: *name, Kind: *kind}
		switch *kind {
		case domain.LeadAcceptKindReader:
			if *requestFile == "" {
				return errors.New("--kind reader requires --request-file")
			}
			raw, err := os.ReadFile(*requestFile)
			if err != nil {
				return fmt.Errorf("request file: %w", err)
			}
			req, bad := domain.ParseLeadAcceptRequestJSON(raw)
			if bad != nil {
				return fmt.Errorf("request file is not a valid lead-accept request: %s", describeFields(bad))
			}
			in.Request = req
			in.WorkspaceID = cli.workspaceID
			in.EvidenceProjectID = cli.evidenceProjectID
		case domain.LeadAcceptKindPublisher:
			if *requestFile != "" {
				return errors.New("--request-file is only for --kind reader")
			}
		default:
			return errors.New("--kind must be reader or publisher")
		}
		if !domain.LeadAcceptNameValid(*name) {
			return errors.New("--name must match ^[a-z0-9][a-z0-9-]{1,62}$")
		}
		var out map[string]any
		err := cli.withNewToken(*tokenFile, *kind, func(hash, prefix string) error {
			p, action, err := cli.repo.Issue(ctx, op, in, hash, prefix, cli.now())
			if err != nil {
				return err
			}
			out = map[string]any{"result": action, "id": p.ID, "name": p.Name, "kind": p.Kind,
				"binding_sha256": p.BindingSHA256, "token_prefix": p.TokenPrefix, "token_file": *tokenFile}
			return nil
		})
		if err != nil {
			return err
		}
		return cli.print(out)
	case "rotate":
		if *grace < 0 {
			return errors.New("--grace must not be negative")
		}
		p, err := cli.repo.PrincipalByName(ctx, *name)
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("no principal %s", *name)
		}
		var out map[string]any
		err = cli.withNewToken(*tokenFile, p.Kind, func(hash, prefix string) error {
			rotated, rotateErr := cli.repo.Rotate(ctx, op, *name, hash, prefix, *grace, cli.now())
			if rotateErr != nil {
				return rotateErr
			}
			out = map[string]any{"result": "rotate", "id": rotated.ID, "name": rotated.Name, "kind": rotated.Kind,
				"binding_sha256": rotated.BindingSHA256, "token_prefix": rotated.TokenPrefix,
				"previous_valid_until": rotated.PreviousValidUntil, "token_file": *tokenFile}
			return nil
		})
		if err != nil {
			return err
		}
		return cli.print(out)
	case "revoke":
		if strings.TrimSpace(*reason) == "" {
			return errors.New("--reason is required")
		}
		p, err := cli.repo.Revoke(ctx, op, *name, *reason, cli.now())
		if err != nil {
			return err
		}
		return cli.print(map[string]any{"result": "revoke", "id": p.ID, "name": p.Name, "kind": p.Kind, "revoked_at": p.RevokedAt})
	case "show":
		p, err := cli.repo.Show(ctx, op, *name, cli.now())
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("no principal %s", *name)
		}
		audit, err := cli.repo.Audit(ctx, p.ID)
		if err != nil {
			return err
		}
		out := map[string]any{"principal": p, "audit": audit}
		if p.Kind == domain.LeadAcceptKindPublisher {
			o, err := cli.repo.LatestObservationOf(ctx, p.ID)
			if err != nil {
				return err
			}
			out["latest_observation"] = o
		}
		return cli.print(out)
	}
	return fmt.Errorf("unknown command %q; %s", cmd, leadAcceptUsage)
}

func describeFields(bad map[string]string) string {
	keys := make([]string, 0, len(bad))
	for k := range bad {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+bad[k])
	}
	return strings.Join(parts, "; ")
}

// withNewToken creates the token file first (new, 0600, written and synced),
// then runs store with the token's hash and public prefix. store must return
// only the persistence result; output happens after this function succeeds.
// If store fails the file is removed; once committed it is kept even if later
// stdout fails.
func (cli *leadAcceptCLI) withNewToken(path, kind string, store func(hash, prefix string) error) error {
	if path == "" {
		return errors.New("--token-file is required")
	}
	token, err := domain.NewLeadAcceptToken(kind)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	_, werr := f.WriteString(token)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("token file: %w", werr)
	}
	if err := store(domain.HashGateIntakeToken(token), domain.GateIntakeTokenDisplay(token)); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func (cli *leadAcceptCLI) print(v any) error {
	enc := json.NewEncoder(cli.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
