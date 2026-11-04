package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/actorctx"
)

type nrStub struct {
	NativeReviewStore
	reserveErr error
	called     bool
	packet     *domain.NativePacket
	err        error // returned by every store method but Reserve
	calls      []string
}

func (s *nrStub) CreateAuthority(context.Context, uuid.UUID, domain.NativeAuthorityRequest) (uuid.UUID, error) {
	s.calls = append(s.calls, "authority")
	return uuid.New(), s.err
}

func (s *nrStub) RecordDelivery(context.Context, uuid.UUID, string, string, uuid.UUID) error {
	s.calls = append(s.calls, "delivery")
	return s.err
}

func (s *nrStub) RevokeAuthority(context.Context, uuid.UUID, uuid.UUID, string) error {
	s.calls = append(s.calls, "revoke")
	return s.err
}

func (s *nrStub) GetReservation(context.Context, uuid.UUID, uuid.UUID) (*domain.NativeReservation, error) {
	s.calls = append(s.calls, "get-reservation")
	return &domain.NativeReservation{Consumed: true}, s.err
}

func (s *nrStub) IssueReceipt(context.Context, uuid.UUID, domain.NativeIssueRequest) (*domain.NativePacket, error) {
	s.calls = append(s.calls, "issue")
	return s.packet, s.err
}

func (s *nrStub) ReadReceipt(context.Context, uuid.UUID, uuid.UUID) (*domain.NativePacket, error) {
	s.calls = append(s.calls, "read")
	return s.packet, s.err
}

func (s *nrStub) Reserve(context.Context, uuid.UUID, domain.NativeReserveRequest) (*domain.NativeReservation, error) {
	s.called = true
	return nil, s.reserveErr
}

func nrCall(t *testing.T, h echo.HandlerFunc, actor uuid.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if actor != uuid.Nil {
		req = req.WithContext(actorctx.WithActor(req.Context(), actor, domain.ActorTypeAgent))
	}
	rec := httptest.NewRecorder()
	require.NoError(t, h(echo.New().NewContext(req, rec)))
	return rec
}

func TestNativeReviewHandler_ReserveGuards(t *testing.T) {
	s := &nrStub{}
	h := NewNativeReviewHandler(s)
	rec := nrCall(t, h.Reserve, uuid.Nil, `{}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.False(t, s.called, "unauthenticated never reaches the store")

	rec = nrCall(t, h.Reserve, uuid.New(), `{"schema":"native-review-admission/v1","issuer_actor_id":"`+uuid.NewString()+`"}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), "schema_invalid", "a request cannot name an issuer")
	require.False(t, s.called)

	s.reserveErr = domain.NativeRefuse("commit_uncertain", "x")
	// complete, valid-shaped request reaches the store; uncertain commit is 202, not a retryable 5xx
	u := uuid.NewString()
	h64 := strings.Repeat("a", 64)
	body := `{"schema":"native-review-admission/v1","authority_id":"` + u + `","receipt_id":"` + u + `","receipt_sha256":"` + h64 + `",` +
		`"scope":{"domain":"t12-successor-synthetic","namespace":"` + u + `","test":true,"criteria_sha256":"` + h64 + `","context_sha256":"` + h64 + `"},` +
		`"pins":{"source_repo":"r","source_sha":"` + strings.Repeat("a", 40) + `","package_sha256":"` + h64 + `","manifest_sha256":"` + h64 + `","config_sha256":"` + h64 + `"},` +
		`"receiver":{"task":"` + u + `","attempt":"` + u + `","session":"` + u + `","generation":1},` +
		`"native_registration":"` + h64 + `","native_event_id":"e","request_id":"` + u + `"}`
	rec = nrCall(t, h.Reserve, uuid.New(), body)
	require.True(t, s.called)
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Contains(t, rec.Body.String(), "commit_uncertain")
}

// The HTTP body is the flat signed packet itself, no envelope and no extra field:
// the ordinary publisher forwards it unchanged and the Runner admits it as is.
func TestNativeReviewHandler_ReturnsFlatSignedPacket(t *testing.T) {
	file, err := os.ReadFile("../domain/testdata/native_review_a2_fixture_v1.json")
	require.NoError(t, err)
	var doc struct {
		Receipt json.RawMessage `json:"receipt"`
	}
	require.NoError(t, json.Unmarshal(file, &doc))
	var pk domain.NativePacket
	require.NoError(t, domain.StrictDecodeNative(doc.Receipt, &pk))
	want, err := domain.CanonicalJSONBytes(doc.Receipt)
	require.NoError(t, err)

	h := NewNativeReviewHandler(&nrStub{packet: &pk})
	for name, call := range map[string]func() *httptest.ResponseRecorder{
		"issue": func() *httptest.ResponseRecorder {
			return nrCall(t, h.IssueReceipt, uuid.New(), `{"schema":"native-review-admission/v1","enrollment_id":"`+uuid.NewString()+`","review_task_id":"`+uuid.NewString()+
				`","source_task_id":"`+uuid.NewString()+`","verdict":"SHIP","scope":{"domain":"t12-successor-synthetic","namespace":"`+uuid.NewString()+`","test":true,"criteria_sha256":"`+
				strings.Repeat("a", 64)+`","context_sha256":"`+strings.Repeat("a", 64)+`"},"pins":{},"product":{},"receiver":{},"native":{},"body":{}}`)
		},
		"readback": func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			req = req.WithContext(actorctx.WithActor(req.Context(), uuid.New(), domain.ActorTypeAgent))
			rec := httptest.NewRecorder()
			c := echo.New().NewContext(req, rec)
			c.SetParamNames("receipt_id")
			c.SetParamValues(uuid.NewString())
			require.NoError(t, h.GetReceipt(c))
			return rec
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec := call()
			require.Less(t, rec.Code, 300, rec.Body.String())
			got, err := domain.CanonicalJSONBytes(rec.Body.Bytes())
			require.NoError(t, err)
			require.Equal(t, string(want), string(got))
		})
	}
}

func nrParamCall(t *testing.T, h echo.HandlerFunc, actor uuid.UUID, param, value, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if actor != uuid.Nil {
		req = req.WithContext(actorctx.WithActor(req.Context(), actor, domain.ActorTypeAgent))
	}
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.SetParamNames(param)
	c.SetParamValues(value)
	require.NoError(t, h(c))
	return rec
}

// Every endpoint: an unauthenticated call never reaches the store, a malformed
// or unknown-field body is a schema refusal, a bad path id is a 400, a store
// refusal keeps its stable code, and a well-formed call succeeds (positive control).
func TestNativeReviewHandler_EveryEndpointGuards(t *testing.T) {
	good := uuid.NewString()
	h64 := strings.Repeat("a", 64)
	schema := `"schema":"native-review-admission/v1"`
	type ep struct {
		name  string
		fn    func(h *NativeReviewHandler) echo.HandlerFunc
		param string
		body  string
		ok    int
	}
	eps := []ep{
		{"issue", func(h *NativeReviewHandler) echo.HandlerFunc { return h.IssueReceipt }, "", `{` + schema + `}`, http.StatusCreated},
		{"authority", func(h *NativeReviewHandler) echo.HandlerFunc { return h.CreateAuthority }, "", `{` + schema + `}`, http.StatusCreated},
		{"delivery", func(h *NativeReviewHandler) echo.HandlerFunc { return h.RecordDelivery }, "",
			`{` + schema + `,"native_registration":"` + h64 + `","native_event_id":"e","receipt_id":"` + good + `"}`, http.StatusCreated},
		{"revoke", func(h *NativeReviewHandler) echo.HandlerFunc { return h.RevokeAuthority }, "authority_id", `{` + schema + `,"reason":"r"}`, http.StatusOK},
		{"reservation", func(h *NativeReviewHandler) echo.HandlerFunc { return h.GetReservation }, "authority_id", `{}`, http.StatusOK},
		{"receipt", func(h *NativeReviewHandler) echo.HandlerFunc { return h.GetReceipt }, "receipt_id", `{}`, http.StatusOK},
		{"reserve", func(h *NativeReviewHandler) echo.HandlerFunc { return h.Reserve }, "", `{` + schema + `}`, http.StatusOK},
	}
	for _, e := range eps {
		t.Run(e.name, func(t *testing.T) {
			s := &nrStub{packet: &domain.NativePacket{}}
			h := NewNativeReviewHandler(s)
			call := func(actor uuid.UUID, value, body string) *httptest.ResponseRecorder {
				return nrParamCall(t, e.fn(h), actor, e.param, value, body)
			}
			require.Equal(t, http.StatusUnauthorized, call(uuid.Nil, good, e.body).Code)
			require.Empty(t, s.calls, "unauthenticated never reaches the store")
			if e.param != "" {
				require.Equal(t, http.StatusBadRequest, call(uuid.New(), "not-a-uuid", e.body).Code)
			}
			if e.name != "reservation" && e.name != "receipt" {
				rec := call(uuid.New(), good, `{"unknown_field":1}`)
				require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
				require.Contains(t, rec.Body.String(), "schema_invalid")
				rec = call(uuid.New(), good, ``)
				require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
			}
			if e.name == "reserve" {
				return // the reserve store path is covered by the commit_uncertain test
			}
			require.Equal(t, e.ok, call(uuid.New(), good, e.body).Code, "positive control")
			s.err = domain.NativeRefuse("idempotency_payload_mismatch", "x")
			rec := call(uuid.New(), good, e.body)
			require.Equal(t, http.StatusConflict, rec.Code)
			require.Contains(t, rec.Body.String(), "idempotency_payload_mismatch")
			s.err = domain.NativeRefuse("issuer_not_enrolled", "x")
			require.Equal(t, http.StatusUnprocessableEntity, call(uuid.New(), good, e.body).Code)
		})
	}
	t.Run("oversize body", func(t *testing.T) {
		h := NewNativeReviewHandler(&nrStub{})
		rec := nrCall(t, h.Reserve, uuid.New(), `{"schema":"`+strings.Repeat("a", 1<<20+10)+`"}`)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	})
	t.Run("unexpected store error is not a refusal", func(t *testing.T) {
		s := &nrStub{err: errors.New("boom")}
		h := NewNativeReviewHandler(s)
		rec := nrParamCall(t, h.GetReservation, uuid.New(), "authority_id", good, `{}`)
		require.GreaterOrEqual(t, rec.Code, 500)
	})
}
