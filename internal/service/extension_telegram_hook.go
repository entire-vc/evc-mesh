package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/pkg/apierror"
)

// TelegramSaveHook is what saving a Telegram connection needs beyond the
// generic settings write: the token is checked with getMe (which also fills
// bot_username), and the poller is restarted to match the new state.
type TelegramSaveHook struct {
	client TelegramClient
	bots   *TelegramBotManager
	gate   TelegramGate
}

func NewTelegramSaveHook(client TelegramClient, bots *TelegramBotManager, gate TelegramGate) *TelegramSaveHook {
	return &TelegramSaveHook{client: client, bots: bots, gate: gate}
}

func (h *TelegramSaveHook) Prepare(ctx context.Context, settings, sent map[string]any) error {
	token, _ := settings["bot_token"].(string)
	if _, fresh := sent["bot_token"]; !fresh || token == "" {
		return nil // a kept secret: nothing new to check
	}
	if h.client == nil || (h.gate != nil && !h.gate(ctx)) {
		return apierror.BadRequestWithDetails("Telegram is not available on this instance", "the Telegram extension is switched off")
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	name, err := h.client.GetMe(c, token)
	if err != nil {
		return apierror.BadRequestWithDetails("invalid Telegram bot token", err.Error())
	}
	settings["bot_username"] = name
	return nil
}

func (h *TelegramSaveHook) Saved(ctx context.Context, id uuid.UUID) {
	if h.bots != nil {
		h.bots.Reload(ctx, id)
	}
}

func (h *TelegramSaveHook) Removed(_ context.Context, id uuid.UUID) {
	if h.bots != nil {
		h.bots.Stop(id)
	}
}
