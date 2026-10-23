package gitlab

import (
	"context"
	"errors"
	"testing"

	"github.com/entire-vc/evc-mesh/internal/extensions"
	"github.com/entire-vc/evc-mesh/internal/extensions/secretstore"
)

type downKeep struct{}

func (downKeep) PutValue(context.Context, secretstore.Ref, string) (string, error) {
	return "", errors.New("connection refused")
}
func (downKeep) GetValue(context.Context, string) (string, error) {
	return "", errors.New("connection refused")
}

// A token held by an unreachable Keep must surface as Degraded, never as an
// empty token that looks like "no token configured".
func TestDecodeKeepDownIsDegraded(t *testing.T) {
	prev := secretstore.SetDefault(&secretstore.Router{Builtin: secretstore.Builtin{}, Keep: secretstore.Keep{Client: downKeep{}}})
	t.Cleanup(func() { secretstore.SetDefault(prev) })

	c := decode(extensions.Resolved{Enabled: true, Settings: map[string]any{"base_url": "https://gl", "token": "keep:abc"}})
	if c.Degraded == "" || c.Token != "" {
		t.Fatalf("want degraded and no token, got %+v", c)
	}
}
