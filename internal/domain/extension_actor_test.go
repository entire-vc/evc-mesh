package domain

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestExtensionActor_Principal(t *testing.T) {
	ws := uuid.New()
	a := &ExtensionActor{ExtensionID: "gitlab", WorkspaceID: ws}
	assert.Equal(t, "ext:gitlab@"+ws.String(), a.Principal())
}
