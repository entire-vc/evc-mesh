// Package bootstrap creates the first admin account on a fresh install.
//
// It lives outside package main so the decision logic can be tested directly:
// this code only ever runs on someone's very first boot, which is the worst
// possible place to discover a bug in it.
package bootstrap

import (
	"context"
	"crypto/rand"
	"math/big"
)

// RegisterFunc creates a user account. It is deliberately narrower than the
// auth service's own Register — bootstrap has no use for the returned user or
// token pair, and the narrow shape keeps this package free of auth/domain types.
type RegisterFunc func(ctx context.Context, email, password, name string) error

// Deps are the effects Admin needs. Injecting them keeps every branch below
// reachable from a test without a database.
type Deps struct {
	CountUsers func(ctx context.Context) (int, error)
	Register   RegisterFunc
	Getenv     func(string) string
	Logf       func(format string, v ...any)
}

const (
	defaultEmail = "admin@localhost"
	defaultName  = "Admin"
)

// Admin creates the first admin user when the database has no users yet.
//
// Its second job matters as much as the first: it is never silent. An empty
// database plus a login screen with no account is the most common self-hosting
// dead end — the operator has no way to tell whether a seed ran, was skipped, or
// was never asked for. Every branch below says what happened and what to do
// next. See docs/self-hosting.md#seeding-the-first-admin.
func Admin(ctx context.Context, d Deps) {
	count, err := d.CountUsers(ctx)
	if err != nil {
		d.Logf("[bootstrap] could not count users (%v) — skipping first-admin seed", err)
		return
	}

	seedRequested := d.Getenv("MESH_SEED_ADMIN") == "true"

	// Established instance: users already exist, so there is nothing to bootstrap.
	if count > 0 {
		if seedRequested {
			d.Logf("[bootstrap] MESH_SEED_ADMIN=true, but the database already has %d user(s) — seed skipped by design.", count)
			d.Logf("[bootstrap] Log in with an existing account. The seed only ever runs on a database with zero users.")
		}
		return
	}

	// Fresh database, but nobody asked for a seed — this is the dead end.
	if !seedRequested {
		d.Logf("[bootstrap] The database has no users yet, so nobody can log in.")
		d.Logf("[bootstrap] Create the first admin in one of two ways:")
		d.Logf("[bootstrap]   1. Open the web UI and register at /register (the first account is yours).")
		d.Logf("[bootstrap]   2. Restart the API with: MESH_SEED_ADMIN=true MESH_ADMIN_EMAIL=you@example.com MESH_ADMIN_PASSWORD='<strong-password>'")
		return
	}

	email := d.Getenv("MESH_ADMIN_EMAIL")
	name := d.Getenv("MESH_ADMIN_NAME")
	password := d.Getenv("MESH_ADMIN_PASSWORD")
	if email == "" {
		email = defaultEmail
	}
	if name == "" {
		name = defaultName
	}

	// No password supplied: generate a strong one rather than falling back to a
	// well-known constant. A published default password on an internet-facing
	// install is a straight-up account takeover, and "we'll change it later"
	// never happens. It is printed exactly once, here.
	generated := false
	if password == "" {
		password, err = GeneratePassword()
		if err != nil {
			d.Logf("[bootstrap] could not generate an admin password (%v) — seed aborted.", err)
			d.Logf("[bootstrap] Set MESH_ADMIN_PASSWORD='<strong-password>' and restart.")
			return
		}
		generated = true
	}

	if err := d.Register(ctx, email, password, name); err != nil {
		d.Logf("[bootstrap] first-admin seed FAILED for %s: %v", email, err)
		d.Logf("[bootstrap] Nobody can log in yet. Fix the cause above and restart, or register at /register in the web UI.")
		return
	}

	if generated {
		d.Logf("[bootstrap] ────────────────────────────────────────────────────────")
		d.Logf("[bootstrap] First admin created: %s", email)
		d.Logf("[bootstrap] Generated password:  %s", password)
		d.Logf("[bootstrap] This password is shown ONCE and is not stored anywhere.")
		d.Logf("[bootstrap] Copy it now, log in, and change it. To pick your own")
		d.Logf("[bootstrap] instead, set MESH_ADMIN_PASSWORD before the first boot.")
		d.Logf("[bootstrap] ────────────────────────────────────────────────────────")
		return
	}

	d.Logf("[bootstrap] First admin created: %s (password taken from MESH_ADMIN_PASSWORD — change it after first login).", email)
}

// passwordLength is long enough that the generated credential is not worth
// attacking even though it is printed to a log the operator may not rotate.
const passwordLength = 24

// GeneratePassword returns a random password that satisfies the same complexity
// rules the auth service enforces on registration (upper, lower, digit), so a
// generated credential can never be rejected by its own validator.
//
// The alphabets omit visually ambiguous characters (O/0, l/1/I) because this
// password gets copied out of a terminal by hand.
func GeneratePassword() (string, error) {
	const (
		upper  = "ABCDEFGHJKLMNPQRSTUVWXYZ"
		lower  = "abcdefghijkmnopqrstuvwxyz"
		digits = "23456789"
	)
	alphabet := upper + lower + digits

	// One guaranteed character per required class, then fill to length.
	out := make([]byte, 0, passwordLength)
	for _, class := range []string{upper, lower, digits} {
		c, err := randomChar(class)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	for len(out) < passwordLength {
		c, err := randomChar(alphabet)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}

	// Shuffle so the first three positions are not a predictable class pattern.
	for i := len(out) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", err
		}
		out[i], out[j.Int64()] = out[j.Int64()], out[i]
	}
	return string(out), nil
}

func randomChar(set string) (byte, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
	if err != nil {
		return 0, err
	}
	return set[n.Int64()], nil
}
