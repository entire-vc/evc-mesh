package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/auth"
	"github.com/entire-vc/evc-mesh/internal/domain"
	mw "github.com/entire-vc/evc-mesh/internal/middleware"
	"github.com/entire-vc/evc-mesh/internal/repository/postgres"
	"github.com/entire-vc/evc-mesh/internal/service"
)

type e2eSigner struct{ key ed25519.PrivateKey }

func (s e2eSigner) Sign(_ context.Context, m []byte) ([]byte, error) {
	return ed25519.Sign(s.key, m), nil
}

func e2eSHA(c string) string {
	out := ""
	for len(out) < 64 {
		out += c
	}
	return out[:64]
}

// Real credentials end to end: agents are registered through the real service
// (real API keys), requests pass the real DualAuth middleware into the real
// handler and PostgreSQL repository. The actor the store sees is the one the key
// authenticates, never anything the body says.
func TestNativeReviewE2E_RealAuthIssueReadback(t *testing.T) {
	db := oauthE2ETestDB(t)
	userRepo := postgres.NewUserRepo(db)
	workspaceRepo := postgres.NewWorkspaceRepo(db)
	agentSvc := service.NewAgentService(postgres.NewAgentRepo(db), postgres.NewActivityLogRepo(db), workspaceRepo, userRepo)
	authSvc := auth.NewService(userRepo, postgres.NewRefreshTokenRepo(db), workspaceRepo, postgres.NewWorkspaceMemberRepo(db), "native-review-e2e-test-jwt-secret")

	suffix := uuid.NewString()[:8]
	user, _, err := authSvc.Register(context.Background(), "nr-"+suffix+"@native-review-e2e.example.com", "Correct-Horse-Battery-Staple-1", "nr-"+suffix)
	require.NoError(t, err)
	wss, err := workspaceRepo.ListForUser(context.Background(), user.ID)
	require.NoError(t, err)
	require.NotEmpty(t, wss)
	ws := wss[0].ID

	type cred struct {
		id  uuid.UUID
		key string
	}
	reg := func(name string) cred {
		out, rerr := agentSvc.Register(context.Background(), service.RegisterAgentInput{WorkspaceID: ws, Name: name, AgentType: domain.AgentTypeCustom})
		require.NoError(t, rerr)
		return cred{out.Agent.ID, out.APIKey}
	}
	operator, issuer, publisher, consumer, authIssuer, author, stranger := reg("nr-op"), reg("nr-issuer"), reg("nr-pub"), reg("nr-cons"), reg("nr-auth"), reg("nr-author"), reg("nr-stranger")

	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	repo := postgres.NewNativeReviewRepo(db, nil, e2eSigner{priv})
	body := []byte("SHIP exact head")
	authBytes, rootBytes := []byte("original authority bytes"), []byte("original root disposition")
	enr := &postgres.NativeEnrollment{
		WorkspaceID: ws, ProjectID: uuid.New(), IssuerActorID: issuer.id, PublisherActorID: publisher.id, TransportSourceID: uuid.New(),
		ConsumerActorID: consumer.id, AuthorityIssuerActorID: authIssuer.id, ResultAuthorActorID: author.id, OwnerActorID: uuid.New(),
		AuthorityID: uuid.New(), IssuerKey: base64.StdEncoding.EncodeToString(pub),
		ReceiverTask: uuid.New(), ReceiverAttempt: uuid.New(), ReceiverSession: uuid.New(), ReceiverGeneration: 1,
		NativeStage: "review-receipt", NativePackageSHA: e2eSHA("3"),
		ReviewTaskID: uuid.New(), SourceTaskID: uuid.New(), ReviewerFamily: "codex", AuthorFamily: "claude",
		ScopeNamespace: uuid.New(), ScopeTest: true, CriteriaSHA: e2eSHA("1"), ContextSHA: e2eSHA("2"),
		SourceRepo: "evc-mesh", SourceSHA: "0123456789abcdef0123456789abcdef01234567", PackageSHA: e2eSHA("3"), ManifestSHA: e2eSHA("4"), ConfigSHA: e2eSHA("5"),
		ProductTaskID: uuid.New(), ProductObservationID: e2eSHA("6")[:32], ProductSessionID: e2eSHA("7")[:32],
		AuthorityBytes: authBytes, RootDispositionBytes: rootBytes,
		AuthoritySHA: domain.NativeDigest(authBytes), RootDispositionSHA: domain.NativeDigest(rootBytes),
	}
	enr.NativeRegistration, err = domain.DeriveNativeRegistration(
		domain.NativeBinding{Producer: enr.TransportSourceID.String(), Task: enr.SourceTaskID.String(), Stage: enr.NativeStage, Package: enr.NativePackageSHA, Test: true},
		domain.NativeReceiver{Task: enr.ReceiverTask, Attempt: enr.ReceiverAttempt, Session: enr.ReceiverSession, Generation: 1})
	require.NoError(t, err)
	_, err = repo.CreateEnrollment(context.Background(), enr, operator.id)
	require.NoError(t, err)

	e := echo.New()
	e.HTTPErrorHandler = NewHTTPErrorHandler(e.DefaultHTTPErrorHandler)
	api := e.Group("/api/v1")
	api.Use(mw.DualAuth(authSvc, agentSvc, nil))
	h := NewNativeReviewHandler(repo)
	api.POST("/native-review/receipts", h.IssueReceipt)
	api.GET("/native-review/receipts/:receipt_id", h.GetReceipt)
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)

	req := domain.NativeIssueRequest{
		Schema: domain.NativeReviewSchema, EnrollmentID: enr.ID, ReviewTaskID: enr.ReviewTaskID, SourceTaskID: enr.SourceTaskID, Verdict: domain.NativeVerdictShip,
		Scope:    domain.NativeScope{Domain: domain.NativeReviewScopeDomain, Namespace: enr.ScopeNamespace, Test: true, CriteriaSHA: enr.CriteriaSHA, ContextSHA: enr.ContextSHA},
		Pins:     domain.NativePins{SourceRepo: enr.SourceRepo, SourceSHA: enr.SourceSHA, PackageSHA: enr.PackageSHA, ManifestSHA: enr.ManifestSHA, ConfigSHA: enr.ConfigSHA},
		Product:  domain.NativeProduct{OwningTaskID: enr.ProductTaskID, ObservationID: enr.ProductObservationID, SessionID: enr.ProductSessionID, AuthoritySHA: enr.AuthoritySHA, RootDispSHA: enr.RootDispositionSHA},
		Receiver: domain.NativeReceiver{Task: enr.ReceiverTask, Attempt: enr.ReceiverAttempt, Session: enr.ReceiverSession, Generation: 1},
		Native: domain.NativeLink{Registration: enr.NativeRegistration, Binding: domain.NativeBinding{
			Producer: enr.TransportSourceID.String(), Task: enr.SourceTaskID.String(), Stage: enr.NativeStage, Package: enr.NativePackageSHA, Test: true}},
		Body: domain.NativeBody{Encoding: "base64", Bytes: base64.StdEncoding.EncodeToString(body), SHA256: domain.NativeDigest(body)},
	}
	raw, err := json.Marshal(req)
	require.NoError(t, err)

	do := func(method, path, key string, payload []byte) (int, []byte) {
		r, rerr := http.NewRequest(method, srv.URL+path, bytes.NewReader(payload))
		require.NoError(t, rerr)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("X-Agent-Key", key)
		}
		c := &http.Client{Timeout: 30 * time.Second}
		resp, derr := c.Do(r)
		require.NoError(t, derr)
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}
	const issuePath = "/api/v1/native-review/receipts"

	// negative controls first: nothing is written for any of these
	for name, key := range map[string]string{"no credential": "", "forged key": "agk_forged_deadbeef"} {
		code, _ := do(http.MethodPost, issuePath, key, raw)
		require.Equal(t, http.StatusUnauthorized, code, name)
	}
	code, out := do(http.MethodPost, issuePath, publisher.key, raw) // a real credential that is not the enrolled issuer
	require.Equal(t, http.StatusUnprocessableEntity, code, string(out))
	require.Contains(t, string(out), "issuer_not_enrolled")
	require.Zero(t, countRows(t, db, enr.ID))

	// positive control: the enrolled issuer's real key issues the signed packet
	code, out = do(http.MethodPost, issuePath, issuer.key, raw)
	require.Equal(t, http.StatusCreated, code, string(out))
	var pk domain.NativePacket
	require.NoError(t, domain.StrictDecodeNative(out, &pk))
	require.NoError(t, pk.Verify(enr.IssuerKey), "the packet verifies under the enrolled key")
	require.Equal(t, enr.AuthorityID, pk.AuthorityID)
	require.Equal(t, 1, countRows(t, db, enr.ID))

	// a second issue for the same enrollment is a conflict, not a second receipt
	code, out = do(http.MethodPost, issuePath, issuer.key, raw)
	require.Equal(t, http.StatusUnprocessableEntity, code, string(out))
	require.Equal(t, 1, countRows(t, db, enr.ID))

	// readback: enrolled actors get the identical bytes, a stranger gets nothing
	readPath := "/api/v1/native-review/receipts/" + pk.ReceiptID.String()
	code, again := do(http.MethodGet, readPath, publisher.key, nil)
	require.Equal(t, http.StatusOK, code)
	var pk2 domain.NativePacket
	require.NoError(t, domain.StrictDecodeNative(again, &pk2))
	c1, err := pk.Canonical()
	require.NoError(t, err)
	c2, err := pk2.Canonical()
	require.NoError(t, err)
	require.Equal(t, c1, c2, "readback is byte-identical to the issued packet")
	code, out = do(http.MethodGet, readPath, stranger.key, nil)
	require.NotEqual(t, http.StatusOK, code, string(out))
	require.Contains(t, string(out), "receipt_not_found")
	code, _ = do(http.MethodGet, readPath, "", nil)
	require.Equal(t, http.StatusUnauthorized, code)
}

func countRows(t *testing.T, db interface {
	Get(dest any, query string, args ...any) error
}, enrollment uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, db.Get(&n, `SELECT count(*) FROM native_review_receipts WHERE enrollment_id=$1`, enrollment))
	return n
}
