package refext

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWKSPath is where Mesh publishes the keys of the iframe context token.
const JWKSPath = "/api/v1/.well-known/jwks.json"

// jwksTTL is how long a fetched key set is used before it is fetched again.
// A token with an unknown kid triggers an earlier refetch (keys rotate).
const jwksTTL = 5 * time.Minute

// jwksMinRefetch is the shortest interval between two fetches of the key set.
const jwksMinRefetch = 30 * time.Second

// Context is what Mesh tells a screen about the person who opened it.
type Context struct {
	UserID      string
	Email       string
	WorkspaceID string
	ProjectID   string // task.panel only
	TaskID      string // task.panel only
	Roles       []string
}

// ContextVerifier checks the 60-second context token Mesh puts on a screen
// URL (docs/extensions/ui-iframe.md): ES256, key by kid from the JWKS,
// iss = the Mesh origin, aud = the extension id, exp not passed.
type ContextVerifier struct {
	MeshOrigin  string // e.g. https://mesh.example.com, no trailing slash
	ExtensionID string
	HTTP        *http.Client

	now func() time.Time

	mu        sync.Mutex
	keys      map[string]*ecdsa.PublicKey
	fetchedAt time.Time
}

func (v *ContextVerifier) clock() time.Time {
	if v.now != nil {
		return v.now()
	}
	return time.Now()
}

// Verify returns the context of a valid token.
func (v *ContextVerifier) Verify(ctx context.Context, token string) (*Context, error) {
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return v.key(ctx, kid)
	},
		jwt.WithValidMethods([]string{"ES256"}),
		jwt.WithIssuer(v.MeshOrigin),
		jwt.WithAudience(v.ExtensionID),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(v.clock),
	)
	if err != nil {
		return nil, err
	}
	str := func(k string) string { s, _ := claims[k].(string); return s }
	c := &Context{
		UserID: str("sub"), Email: str("email"), WorkspaceID: str("workspace_id"),
		ProjectID: str("project_id"), TaskID: str("task_id"),
	}
	if roles, ok := claims["roles"].([]any); ok {
		for _, r := range roles {
			if s, ok := r.(string); ok {
				c.Roles = append(c.Roles, s)
			}
		}
	}
	if c.UserID == "" || c.WorkspaceID == "" {
		return nil, errors.New("token lacks sub or workspace_id")
	}
	return c, nil
}

func (v *ContextVerifier) key(ctx context.Context, kid string) (*ecdsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	age := v.clock().Sub(v.fetchedAt)
	if k := v.keys[kid]; k != nil && age < jwksTTL {
		return k, nil
	}
	// An unknown kid refetches, but not more than once per jwksMinRefetch: a
	// stream of forged kids must not turn into a stream of requests to Mesh.
	if v.keys != nil && age < jwksMinRefetch {
		return nil, fmt.Errorf("unknown key %q", kid)
	}
	if err := v.fetchLocked(ctx); err != nil {
		return nil, err
	}
	if k := v.keys[kid]; k != nil {
		return k, nil
	}
	return nil, fmt.Errorf("unknown key %q", kid)
}

func (v *ContextVerifier) fetchLocked(ctx context.Context) error {
	client := v.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.MeshOrigin+JWKSPath, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch jwks: status %d", resp.StatusCode)
	}
	var doc struct {
		Keys []struct {
			Kty, Crv, Alg, Kid, X, Y string
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("decode jwks: %w", err)
	}
	keys := map[string]*ecdsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" || k.Kid == "" {
			continue
		}
		x, xerr := base64.RawURLEncoding.DecodeString(k.X)
		y, yerr := base64.RawURLEncoding.DecodeString(k.Y)
		if xerr != nil || yerr != nil || len(x) != 32 || len(y) != 32 {
			continue
		}
		// Uncompressed point 0x04||X||Y; the parser rejects points off the curve.
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	v.keys, v.fetchedAt = keys, v.clock()
	return nil
}

// screen serves the two iframe screens. The page is framed only by Mesh
// (frame-ancestors) and renews its token through mesh.ready before it expires.
func (s *Server) screen(w http.ResponseWriter, r *http.Request) {
	ctx, err := s.UI.Verify(r.Context(), r.URL.Query().Get("token"))
	if err != nil {
		http.Error(w, "invalid context token", http.StatusUnauthorized)
		return
	}
	if r.URL.Path == PathUITask && ctx.TaskID == "" {
		http.Error(w, "task screen needs a task", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Security-Policy", "frame-ancestors "+s.UI.MeshOrigin)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = screenPage.Execute(w, map[string]any{
		"Ctx": ctx, "MeshOrigin": s.UI.MeshOrigin, "Roles": strings.Join(ctx.Roles, ", "),
	})
}

var screenPage = template.Must(template.New("screen").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>Reference extension</title></head>
<body>
<h1>Hello{{if .Ctx.Email}}, {{.Ctx.Email}}{{end}}</h1>
<p>Workspace {{.Ctx.WorkspaceID}}{{if .Roles}} ({{.Roles}}){{end}}</p>
{{if .Ctx.TaskID}}<p>Task {{.Ctx.TaskID}} in project {{.Ctx.ProjectID}}</p>{{end}}
<script>
  const mesh = {{.MeshOrigin}};
  let renew;
  window.addEventListener("message", (e) => {
    if (e.origin !== mesh || e.source !== window.parent || !e.data || e.data.type !== "mesh.context") return;
    // Send e.data.token with every call to this service; ask for a new one before it expires.
    clearTimeout(renew);
    const left = Date.parse(e.data.expires_at) - Date.now();
    renew = setTimeout(() => window.parent.postMessage({ type: "mesh.ready" }, mesh), Math.max(left - 10000, 1000));
  });
  window.parent.postMessage({ type: "mesh.ready" }, mesh);
  window.parent.postMessage({ type: "mesh.resize", height: document.body.scrollHeight }, mesh);
</script>
</body></html>
`))
