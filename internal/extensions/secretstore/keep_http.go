package secretstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
)

// Errors a Keep answer maps to. Anything else, and every transport failure,
// is treated as "Keep unavailable" and ends in a degraded Resolve.
var (
	ErrKeepNotFound  = errors.New("keep: secret not found")
	ErrKeepForbidden = errors.New("keep: actor has no scope for the secret")
)

// maxKeepBody bounds what is read back from Keep.
const maxKeepBody = 1 << 20

// HTTPKeepClient talks to Keep as the extension's service actor (spec 06 §7):
//
//	POST /orgs/{org}/secrets                 register the reference (first Put of a setting)
//	PUT  /orgs/{org}/secrets/{id}/value      {value} -> {id, version}
//	POST /orgs/{org}/secrets/{id}/resolve    -> {value, version}
//
// 404 = no such secret, 403 = no scope, 503 and transport errors = Keep
// unavailable. The value is never logged and never put in an error.
type HTTPKeepClient struct {
	BaseURL string
	OrgID   uuid.UUID
	// Token returns the actor's JWT (keep_u_, JWKS federation).
	Token func(ctx context.Context) (string, error)
	HTTP  *http.Client
}

func (c *HTTPKeepClient) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 5 * time.Second}
}

// ErrKeepBadID is a secret id that is not the UUID Keep issues. Ids come from
// stored settings, so one is never trusted into a request path.
var ErrKeepBadID = errors.New("keep: malformed secret id")

func parseSecretID(id string) (string, error) {
	u, err := uuid.Parse(id)
	if err != nil {
		return "", ErrKeepBadID
	}
	return u.String(), nil
}

func (c *HTTPKeepClient) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	u, err := url.JoinPath(c.BaseURL, "orgs", c.OrgID.String(), path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return fmt.Errorf("actor token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxKeepBody))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrKeepNotFound
	case resp.StatusCode == http.StatusForbidden:
		return ErrKeepForbidden
	case resp.StatusCode >= 300:
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Placeholders for the catalog fields Keep requires on registration; the exact
// values are confirmed on Keep's side.
const (
	keepClass = "extension-setting"
	keepType  = "token"
)

func (c *HTTPKeepClient) PutValue(ctx context.Context, ref Ref, plain string) (string, error) {
	scopeLevel, scopeID := "org", c.OrgID
	if ref.WorkspaceID != uuid.Nil {
		scopeLevel, scopeID = "ws", ref.WorkspaceID
	}
	name := ref.ExtensionID + "." + ref.Field
	var sec struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "secrets", map[string]string{
		"scope_level":  scopeLevel,
		"scope_id":     scopeID.String(),
		"name":         name,
		"class":        keepClass,
		"type":         keepType,
		"provider_ref": "builtin://" + name,
	}, &sec)
	if err != nil {
		return "", err
	}
	id, err := parseSecretID(sec.ID)
	if err != nil {
		return "", err
	}
	if err := c.do(ctx, http.MethodPut, "secrets/"+id+"/value", map[string]string{"value": plain}, nil); err != nil {
		return "", err
	}
	return id, nil
}

func (c *HTTPKeepClient) GetValue(ctx context.Context, secretID string) (string, error) {
	id, err := parseSecretID(secretID)
	if err != nil {
		return "", err
	}
	var out struct {
		Value string `json:"value"`
	}
	if derr := c.do(ctx, http.MethodPost, "secrets/"+id+"/resolve", struct{}{}, &out); derr != nil {
		return "", derr
	}
	if out.Value == "" {
		return "", errors.New("keep returned an empty value")
	}
	return out.Value, nil
}
