package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
)

// gate-intake: the host-local operator command for gate-intake principals
// (docs/gate-intake.md). It runs instead of the server when the first argument
// is "gate-intake", with the environment of mesh-api.service:
//
//	mesh-api gate-intake issue  --operator <uuid> --name <name> --workspace <uuid> --project <uuid>
//	                            --assignee-agent <uuid> --label kind:ops --label spark-gate
//	                            [--priority high] --schema contenthub-spark-gate-wake/v1 --token-file <new path>
//	mesh-api gate-intake rotate --operator <uuid> --name <name> --token-file <new path> [--grace 1h]
//	mesh-api gate-intake revoke --operator <uuid> --name <name> --reason "<why>"
//	mesh-api gate-intake show   --name <name>
//
// A token is written only to --token-file, created new with mode 0600 (the
// command refuses an existing file), and is never printed. There is no public
// API that issues one.

const gateIntakeUsage = "usage: mesh-api gate-intake issue|rotate|revoke|show [flags] (see docs/gate-intake.md)"

type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// gateIntakeCLI is the command with its database handed in, so a test drives
// the same code as the binary.
type gateIntakeCLI struct {
	repo   *postgres.GateIntakeRepo
	stdout io.Writer
}

// runGateIntakeMain is the binary's entry point: it opens the database from
// the service environment and runs the command. Returns the exit code.
func runGateIntakeMain(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, gateIntakeUsage)
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
	cli := &gateIntakeCLI{repo: postgres.NewGateIntakeRepo(db), stdout: os.Stdout}
	if err := cli.run(ctx, args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func (cli *gateIntakeCLI) run(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New(gateIntakeUsage)
	}
	cmd := args[0]
	switch cmd {
	case "issue", "rotate", "revoke", "show":
	default:
		return fmt.Errorf("unknown command %q; %s", cmd, gateIntakeUsage)
	}
	fs := flag.NewFlagSet("gate-intake "+cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	operator := fs.String("operator", "", "operator agent UUID (issue, rotate, revoke)")
	name := fs.String("name", "", "principal name")
	workspace := fs.String("workspace", "", "workspace UUID (issue)")
	project := fs.String("project", "", "project UUID (issue)")
	assignee := fs.String("assignee-agent", "", "assignee agent UUID (issue)")
	var labels stringList
	fs.Var(&labels, "label", "card label, repeatable (issue)")
	priority := fs.String("priority", string(domain.PriorityHigh), "card priority (issue)")
	schema := fs.String("schema", "", "request schema (issue)")
	tokenFile := fs.String("token-file", "", "new file to write the token to, mode 0600 (issue, rotate)")
	grace := fs.Duration("grace", domain.GateIntakeDefaultGrace, "how long the previous token keeps working (rotate)")
	reason := fs.String("reason", "", "why (revoke)")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%v; %s", err, gateIntakeUsage)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q; %s", fs.Arg(0), gateIntakeUsage)
	}
	if *name == "" {
		return errors.New("--name is required")
	}
	op := uuid.Nil
	if cmd != "show" {
		var err error
		op, err = uuid.Parse(*operator)
		if err != nil || op == uuid.Nil {
			return errors.New("--operator must be the operator's UUID")
		}
	}
	switch cmd {
	case "issue":
		b := postgres.GateIntakeBinding{Name: *name, Labels: labels, Priority: domain.Priority(*priority), Schema: *schema}
		for flagName, dst := range map[string]*uuid.UUID{"workspace": &b.WorkspaceID, "project": &b.ProjectID, "assignee-agent": &b.AssigneeAgentID} {
			v := map[string]string{"workspace": *workspace, "project": *project, "assignee-agent": *assignee}[flagName]
			id, err := uuid.Parse(v)
			if err != nil || id == uuid.Nil {
				return fmt.Errorf("--%s must be a UUID", flagName)
			}
			*dst = id
		}
		if len(labels) == 0 {
			return errors.New("at least one --label is required")
		}
		return cli.withNewToken(*tokenFile, func(hash, prefix string) error {
			p, action, err := cli.repo.Issue(ctx, op, b, hash, prefix)
			if err != nil {
				return err
			}
			return cli.print(map[string]any{"result": action, "principal": p, "token_file": *tokenFile})
		})
	case "rotate":
		if *grace < 0 {
			return errors.New("--grace must not be negative")
		}
		return cli.withNewToken(*tokenFile, func(hash, prefix string) error {
			p, err := cli.repo.Rotate(ctx, op, *name, hash, prefix, *grace)
			if err != nil {
				return err
			}
			return cli.print(map[string]any{"result": "rotate", "principal": p, "token_file": *tokenFile})
		})
	case "revoke":
		if strings.TrimSpace(*reason) == "" {
			return errors.New("--reason is required")
		}
		p, err := cli.repo.Revoke(ctx, op, *name, *reason)
		if err != nil {
			return err
		}
		return cli.print(map[string]any{"result": "revoke", "principal": p})
	case "show":
		p, err := cli.repo.PrincipalByName(ctx, *name)
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("no principal %s", *name)
		}
		n, err := cli.repo.ReceiptCount(ctx, p.ID)
		if err != nil {
			return err
		}
		audit, err := cli.repo.Audit(ctx, p.ID)
		if err != nil {
			return err
		}
		return cli.print(map[string]any{"principal": p, "receipts": n, "audit": audit})
	}
	return fmt.Errorf("unknown command %q; %s", cmd, gateIntakeUsage)
}

// withNewToken creates the token file first (new, 0600, written and synced),
// then runs store with the token's hash and public prefix. If store fails the
// file is removed, so a token on disk always is the one in the database.
func (cli *gateIntakeCLI) withNewToken(path string, store func(hash, prefix string) error) error {
	if path == "" {
		return errors.New("--token-file is required")
	}
	token, err := domain.NewGateIntakeToken()
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

func (cli *gateIntakeCLI) print(v any) error {
	enc := json.NewEncoder(cli.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
