package postgres

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

func writeKeyFile(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), mode))
	require.NoError(t, os.Chmod(p, mode))
	return p
}

func TestFileSignerLoad(t *testing.T) {
	dir := t.TempDir()
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = 7
	good := base64.StdEncoding.EncodeToString(seed)
	p := writeKeyFile(t, dir, "k", good+"\n", 0o600)
	s, err := LoadFileSigner(p) // positive control
	require.NoError(t, err)
	sig, err := s.Sign(context.Background(), domain.NativeSigningMessage(nrSHA("a")))
	require.NoError(t, err)
	require.NoError(t, domain.VerifyIssuerSignature(s.PublicKey(), nrSHA("a"), base64.StdEncoding.EncodeToString(sig)))

	bad := map[string]string{
		"loose-mode": writeKeyFile(t, dir, "loose", good, 0o644),
		"short":      writeKeyFile(t, dir, "short", base64.StdEncoding.EncodeToString(seed[:16]), 0o600),
		"not-b64":    writeKeyFile(t, dir, "nb", "!!!!", 0o600),
		"empty":      writeKeyFile(t, dir, "empty", "", 0o600),
		"missing":    filepath.Join(dir, "nope"),
		"dir":        dir,
	}
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(p, link))
	bad["symlink"] = link
	for name, path := range bad {
		_, err := LoadFileSigner(path)
		require.Error(t, err, name)
		require.NotContains(t, err.Error(), good, name)
	}
}

func goodManifest() EnrollmentManifest {
	ab, rb := []byte("original authority"), []byte("original root disposition")
	m := EnrollmentManifest{
		ID: uuid.New(), WorkspaceID: uuid.New(), ProjectID: uuid.New(), IssuerActorID: uuid.New(), PublisherActorID: uuid.New(),
		TransportSourceID: uuid.New(), ConsumerActorID: uuid.New(), AuthorityIssuerActorID: uuid.New(), ResultAuthorActorID: uuid.New(),
		OwnerActorID: uuid.New(), AuthorityID: uuid.New(), ReviewTaskID: uuid.New(), SourceTaskID: uuid.New(), ScopeNamespace: uuid.New(),
		ProductTaskID: uuid.New(), ReceiverGeneration: 1, NativeStage: "review-receipt", ReviewerFamily: "codex", AuthorFamily: "claude",
		AuthorityBytesB64: base64.StdEncoding.EncodeToString(ab), RootDispositionB64: base64.StdEncoding.EncodeToString(rb),
		AuthoritySHA: domain.NativeDigest(ab), RootDispositionSHA: domain.NativeDigest(rb),
	}
	return m
}

func TestParseEnrollmentManifestStrict(t *testing.T) {
	m := goodManifest()
	raw, _ := json.Marshal(m)
	got, err := ParseEnrollmentManifest(raw) // positive control
	require.NoError(t, err)
	_, err = got.Enrollment(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	require.NoError(t, err)

	withKey := append([]byte(`{"issuer_key":"x",`), raw[1:]...)
	for name, in := range map[string][]byte{
		"issuer_key-smuggled": withKey,
		"trailing":            append(append([]byte{}, raw...), []byte(` {}`)...),
		"garbage":             []byte(`nope`),
		"duplicate-key":       append([]byte(`{"project_id":"`+uuid.NewString()+`",`), raw[1:]...),
	} {
		_, perr := ParseEnrollmentManifest(in)
		requireRefusal(t, perr, "manifest_invalid")
		_ = name
	}
	m.ProjectID = uuid.Nil
	raw, _ = json.Marshal(m)
	_, err = ParseEnrollmentManifest(raw)
	requireRefusal(t, err, "manifest_invalid")

	m = goodManifest()
	m.AuthoritySHA = nrSHA("0")
	_, err = m.Enrollment("k")
	requireRefusal(t, err, "manifest_invalid")
}
