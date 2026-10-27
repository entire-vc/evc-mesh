package extensions

import (
	"context"
	"fmt"
	"net/http"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// AllowList answers whether the instance allows an extension. The instance
// administrator's decision holds everywhere, not only at connect time: a
// disallowed extension resolves as off and cannot be switched on, while its
// stored connection stays untouched, so allowing it again brings it back.
type AllowList interface {
	Allowed(ctx context.Context, extensionID string) (bool, error)
}

// ErrNotAllowedMessage is the text of the 403 every switch-on point returns.
const ErrNotAllowedMessage = "This extension is not on the allow-list of this instance. An instance administrator has to allow it first."

// RequireAllowed returns a 403 extension_not_allowed when the allow-list
// refuses id. A nil allow-list allows everything.
func RequireAllowed(ctx context.Context, allow AllowList, id string) error {
	if allow == nil {
		return nil
	}
	ok, err := allow.Allowed(ctx, id)
	if err != nil {
		return fmt.Errorf("extension %q: allow-list: %w", id, err)
	}
	if !ok {
		return &apierror.Error{Code: http.StatusForbidden, ErrorCode: "extension_not_allowed", Message: ErrNotAllowedMessage}
	}
	return nil
}

// ApplyAllowList switches a resolution off when the allow-list refuses id:
// Enabled=false, no settings, NotAllowed=true. Source is kept, so a workspace
// that had the extension connected still shows its connection. Nothing
// configured (SourceNone) is not looked up.
func ApplyAllowList(ctx context.Context, allow AllowList, id string, res Resolved) (Resolved, error) {
	if allow == nil || res.Source == SourceNone {
		return res, nil
	}
	ok, err := allow.Allowed(ctx, id)
	if err != nil {
		return Resolved{}, fmt.Errorf("resolve %q: allow-list: %w", id, err)
	}
	if !ok {
		res.Enabled, res.Settings, res.NotAllowed = false, nil, true
	}
	return res, nil
}
