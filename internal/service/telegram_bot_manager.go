package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/domain"
	"github.com/entire-vc/evc-mesh/pkg/encryption"
)

// TelegramIntegrationConfig is the shape stored in IntegrationConfig.Config
// for provider="telegram". BotToken is always the AES-256-GCM ciphertext
// from pkg/encryption (or a graceful-degradation plaintext copy when
// MESH_INTEGRATION_ENCRYPTION_KEY is unset) — never the bare token — so this
// struct is safe to round-trip through the database and, with BotToken
// stripped, through an API response.
type TelegramIntegrationConfig struct {
	BotToken    string `json:"bot_token,omitempty"`
	BotUsername string `json:"bot_username,omitempty"`
}

// TelegramPreferenceConfig is the shape stored in NotificationPreference.Config
// for channel="telegram".
//
// ChatID is the only field delivery actually trusts: it is written once, by
// a successful /start, and never by the settings-page save path. Username is
// what the subscriber typed in Notification Settings — display only, plus an
// optional second check during binding — and BindToken/BindExpiresAt exist
// only between "the user asked to connect" and "they clicked the link";
// binding clears both.
type TelegramPreferenceConfig struct {
	Username      string     `json:"telegram_username,omitempty"`
	ChatID        int64      `json:"telegram_chat_id,omitempty"`
	BindToken     string     `json:"telegram_bind_token,omitempty"`
	BindExpiresAt *time.Time `json:"telegram_bind_expires_at,omitempty"`
}

// GenerateTelegramBindToken returns a fresh one-time token for the
// t.me/<bot>?start=<token> link. 24 random bytes hex-encoded — unguessable,
// and distinct in shape from a UUID so it is obviously not another kind of id.
func GenerateTelegramBindToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate telegram bind token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// decodeTelegramIntegration extracts a usable (decrypted token, bot
// username) pair from an integration config, or ok=false for every "there is
// no bot to talk to" case: nil, inactive, wrong provider, no token stored, or
// a decryption failure. Callers treat all of these identically — the same
// silent no-op EmailAvailable's false / s.emailSvc.Enabled()'s false get.
func decodeTelegramIntegration(cfg *domain.IntegrationConfig) (token, botUsername string, ok bool) {
	if cfg == nil || !cfg.IsActive || cfg.Provider != domain.IntegrationProviderTelegram {
		return "", "", false
	}
	var parsed TelegramIntegrationConfig
	if len(cfg.Config) > 0 {
		if err := json.Unmarshal(cfg.Config, &parsed); err != nil {
			return "", "", false
		}
	}
	if parsed.BotToken == "" {
		return "", "", false
	}
	plain, err := encryption.Decrypt(parsed.BotToken)
	if err != nil || plain == "" {
		return "", "", false
	}
	return plain, parsed.BotUsername, true
}

// telegramIntegrationSource is the slice of repository.IntegrationRepository
// the bot manager needs: which integrations to poll, and their current state
// (re-read on every poll iteration, so a token change or deactivation is
// picked up without an explicit restart signal for that path).
type telegramIntegrationSource interface {
	ListActiveByProvider(ctx context.Context, provider domain.IntegrationProvider) ([]domain.IntegrationConfig, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.IntegrationConfig, error)
}

// projectLookup is the slice of *postgres.ProjectRepo the Telegram channel
// needs: a project's display name for the message header (GetByID), and, for
// the bind welcome message, the projects a user belongs to in one workspace.
type projectLookup interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Project, error)
	ListForUserInWorkspace(ctx context.Context, workspaceID, userID uuid.UUID) ([]domain.Project, error)
}

// workspaceNameLookup is the slice of *postgres.WorkspaceRepo the Telegram
// channel needs: a workspace's display name for the message header and the
// welcome message's project list.
type workspaceNameLookup interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Workspace, error)
}

// TelegramBotManager owns one long-poll goroutine per active Telegram
// integration and handles the /start binding flow.
//
// Polling (getUpdates), rather than a webhook, was chosen per the product
// spec: simpler for a self-hosted instance sitting behind a reverse proxy —
// no public endpoint or secret path to provision per workspace.
type TelegramBotManager struct {
	client       TelegramClient
	integrations telegramIntegrationSource
	notifRepo    notificationRepository
	users        userEmailLookup
	projects     projectLookup
	workspaces   workspaceNameLookup

	mu      sync.Mutex
	rootCtx context.Context
	cancels map[uuid.UUID]context.CancelFunc
}

// NewTelegramBotManager creates a TelegramBotManager. Call Start once, in its
// own goroutine, to begin polling every active Telegram integration; the
// manager is otherwise idle (EnsureRunning/Stop/Reload become no-ops) until
// Start has run.
func NewTelegramBotManager(
	client TelegramClient,
	integrations telegramIntegrationSource,
	notifRepo notificationRepository,
	users userEmailLookup,
	projects projectLookup,
	workspaces workspaceNameLookup,
) *TelegramBotManager {
	return &TelegramBotManager{
		client:       client,
		integrations: integrations,
		notifRepo:    notifRepo,
		users:        users,
		projects:     projects,
		workspaces:   workspaces,
		cancels:      make(map[uuid.UUID]context.CancelFunc),
	}
}

// Start loads every active Telegram integration and begins polling each one.
// Blocks until ctx is cancelled, at which point every poller it started stops
// too (they are all derived from ctx). Meant to run in its own goroutine for
// the process lifetime, the same shape as the other background loops in
// cmd/api/main.go (checkout reaper, activity tracker, ...).
func (m *TelegramBotManager) Start(ctx context.Context) {
	m.mu.Lock()
	m.rootCtx = ctx
	m.mu.Unlock()

	integrations, err := m.integrations.ListActiveByProvider(ctx, domain.IntegrationProviderTelegram)
	if err != nil {
		log.Printf("[telegram] failed to load active integrations at startup: %v", err)
	} else {
		for i := range integrations {
			m.EnsureRunning(integrations[i].ID)
		}
	}

	<-ctx.Done()
}

// EnsureRunning starts a poller for integrationID if one is not already
// running. Safe to call repeatedly (e.g. after every save in the workspace
// settings UI) — a second call while one is already running is a no-op.
//
// Before Start has run (rootCtx unset — true only in tests that exercise
// bind logic directly, never in the running server) this is a no-op rather
// than a panic, so callers do not need to know whether the manager has been
// started.
func (m *TelegramBotManager) EnsureRunning(integrationID uuid.UUID) {
	m.mu.Lock()
	root := m.rootCtx
	if root == nil {
		m.mu.Unlock()
		return
	}
	if _, running := m.cancels[integrationID]; running {
		m.mu.Unlock()
		return
	}
	pollCtx, cancel := context.WithCancel(root)
	m.cancels[integrationID] = cancel
	m.mu.Unlock()

	go m.pollLoop(pollCtx, integrationID)
}

// Stop cancels integrationID's poller, if one is running. Called when an
// integration is deactivated or deleted so its getUpdates loop does not keep
// holding Telegram's long-poll connection open for a bot nobody can reach
// through the product anymore.
func (m *TelegramBotManager) Stop(integrationID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cancel, ok := m.cancels[integrationID]; ok {
		cancel()
		delete(m.cancels, integrationID)
	}
}

// Reload re-reads integrationID's current state and starts or stops its
// poller to match — the hook the integration handler calls after every
// Configure/Update/Delete on a Telegram integration, so a token change or a
// toggle takes effect immediately instead of waiting for the next process
// restart.
func (m *TelegramBotManager) Reload(ctx context.Context, integrationID uuid.UUID) {
	m.Stop(integrationID)

	cfg, err := m.integrations.GetByID(ctx, integrationID)
	if err != nil || cfg == nil {
		return
	}
	if _, _, ok := decodeTelegramIntegration(cfg); !ok {
		return
	}
	m.EnsureRunning(integrationID)
}

// pollLoop runs getUpdates in a long-poll cycle until ctx is cancelled or the
// integration stops being usable (deleted, deactivated, token cleared) —
// checked at the top of every iteration, so those changes take effect within
// one poll cycle without any other signal reaching this goroutine.
func (m *TelegramBotManager) pollLoop(ctx context.Context, integrationID uuid.UUID) {
	var offset int64
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	const longPollSeconds = 55

	for {
		if ctx.Err() != nil {
			return
		}

		cfg, err := m.integrations.GetByID(ctx, integrationID)
		if err != nil || cfg == nil {
			return
		}
		token, _, ok := decodeTelegramIntegration(cfg)
		if !ok {
			return
		}

		pollCtx, cancel := context.WithTimeout(ctx, time.Duration(longPollSeconds+10)*time.Second)
		updates, err := m.client.GetUpdates(pollCtx, token, offset, longPollSeconds)
		cancel()

		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[telegram] getUpdates failed for integration %s: %v", integrationID, err)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second

		for i := range updates {
			if updates[i].UpdateID >= offset {
				offset = updates[i].UpdateID + 1
			}
			m.handleUpdate(ctx, cfg.WorkspaceID, token, updates[i])
		}
	}
}

// handleUpdate reacts to one incoming update. /start is the only command
// this bot understands; everything else — including a stranger just saying
// hello — gets no reply at all, per the product spec ("случайным людям в
// боте — ничего не отвечать").
func (m *TelegramBotManager) handleUpdate(ctx context.Context, workspaceID uuid.UUID, botToken string, u TelegramUpdate) {
	if u.Message == nil || u.Message.From == nil {
		return
	}
	text := strings.TrimSpace(u.Message.Text)
	if text != "/start" && !strings.HasPrefix(text, "/start ") {
		return
	}
	arg := strings.TrimSpace(strings.TrimPrefix(text, "/start"))
	m.handleStart(ctx, workspaceID, botToken, u.Message.Chat.ID, u.Message.From.Username, arg)
}

// handleStart implements the binding flow: a valid, unexpired, unclaimed
// bind token belonging to a telegram preference row in this workspace links
// that row's chat_id to the sender. Anything else — no token, expired token,
// unknown token, or (when the subscriber recorded one) a sender whose
// Telegram username does not match — gets the same neutral reply and leaks
// nothing about why. That symmetry is deliberate: a stranger probing the bot
// cannot distinguish "no such token" from "token expired an hour ago" from
// "right token, wrong account".
func (m *TelegramBotManager) handleStart(ctx context.Context, workspaceID uuid.UUID, botToken string, chatID int64, fromUsername, bindToken string) {
	if bindToken == "" {
		m.replyNeutral(ctx, botToken, chatID)
		return
	}

	prefs, err := m.notifRepo.GetPreferencesByWorkspace(ctx, workspaceID)
	if err != nil {
		log.Printf("[telegram] /start: failed to load preferences for workspace %s: %v", workspaceID, err)
		return
	}

	var match *domain.NotificationPreference
	var matchCfg TelegramPreferenceConfig
	for i := range prefs {
		p := &prefs[i]
		if p.Channel != "telegram" || p.UserID == nil || len(p.Config) == 0 {
			continue
		}
		var cfg TelegramPreferenceConfig
		if unErr := json.Unmarshal(p.Config, &cfg); unErr != nil {
			continue
		}
		if cfg.BindToken == "" || cfg.BindToken != bindToken {
			continue
		}
		if cfg.BindExpiresAt == nil || time.Now().After(*cfg.BindExpiresAt) {
			continue
		}
		if cfg.Username != "" && !strings.EqualFold(strings.TrimPrefix(cfg.Username, "@"), strings.TrimPrefix(fromUsername, "@")) {
			continue
		}
		match = p
		matchCfg = cfg
		break
	}

	if match == nil {
		m.replyNeutral(ctx, botToken, chatID)
		return
	}

	matchCfg.ChatID = chatID
	matchCfg.BindToken = ""
	matchCfg.BindExpiresAt = nil
	encoded, err := json.Marshal(matchCfg)
	if err != nil {
		log.Printf("[telegram] /start: failed to encode preference config: %v", err)
		return
	}
	match.Config = encoded
	if err := m.notifRepo.UpsertPreference(ctx, match); err != nil {
		log.Printf("[telegram] /start: failed to persist chat binding for user %s: %v", *match.UserID, err)
		return
	}

	m.sendWelcome(ctx, botToken, chatID, workspaceID, *match.UserID)
}

// replyNeutral is sent for every /start that does not bind — no token, an
// expired one, or one that belongs to someone else. The wording never
// varies with the reason, so it carries no information about which case
// applies.
func (m *TelegramBotManager) replyNeutral(ctx context.Context, botToken string, chatID int64) {
	const text = "Бот доступен только пользователям Mesh — получите персональную ссылку в настройках уведомлений вашего воркспейса."
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := m.client.SendMessage(sendCtx, botToken, chatID, text); err != nil {
		log.Printf("[telegram] failed to send neutral reply to chat %d: %v", chatID, err)
	}
}

// sendWelcome is sent once, right after a successful bind, listing the
// user's projects in this one workspace (the workspace that owns the bot
// which just bound them — not every workspace they belong to elsewhere).
func (m *TelegramBotManager) sendWelcome(ctx context.Context, botToken string, chatID int64, workspaceID, userID uuid.UUID) {
	displayName := "коллега"
	if user, err := m.users.GetByID(ctx, userID); err == nil && user != nil && user.Name != "" {
		displayName = user.Name
	}

	wsName := ""
	if ws, err := m.workspaces.GetByID(ctx, workspaceID); err == nil && ws != nil {
		wsName = ws.Name
	}

	projectList := "пока нет проектов"
	if projects, err := m.projects.ListForUserInWorkspace(ctx, workspaceID, userID); err == nil && len(projects) > 0 {
		lines := make([]string, len(projects))
		for i := range projects {
			lines[i] = fmt.Sprintf("- %s / %s", wsName, projects[i].Name)
		}
		projectList = strings.Join(lines, "\n")
	}

	text := fmt.Sprintf(
		"Добро пожаловать, %s, ваша учётная запись успешно привязана.\nВот список ваших проектов:\n%s",
		displayName, projectList,
	)
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := m.client.SendMessage(sendCtx, botToken, chatID, text); err != nil {
		log.Printf("[telegram] failed to send welcome message to chat %d: %v", chatID, err)
	}
}

// buildTelegramNotificationText renders a dispatch event in the product's
// required Telegram format:
//
//	{workspace} | {project}
//	{labels, space-separated}      (omitted entirely when there are none)
//
//	{title}
//
//	{body}                          (omitted when there is no body)
func buildTelegramNotificationText(workspaceName, projectName string, labels []string, title, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s | %s\n", workspaceName, projectName)
	if len(labels) > 0 {
		b.WriteString(strings.Join(labels, " "))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(title)
	if body != "" {
		b.WriteString("\n\n")
		b.WriteString(body)
	}
	return b.String()
}
