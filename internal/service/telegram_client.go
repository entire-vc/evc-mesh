package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// TelegramAPIBaseURL is the default Telegram Bot API endpoint. Tests point
// TelegramClient at an httptest.Server instead via NewTelegramClientWithBaseURL,
// so the real network is never touched by CI.
const TelegramAPIBaseURL = "https://api.telegram.org"

// TelegramUpdate is the subset of a Telegram Bot API update this service reads.
type TelegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *TelegramMessage `json:"message"`
}

// TelegramMessage is the subset of a Telegram message this service reads.
type TelegramMessage struct {
	MessageID int64         `json:"message_id"`
	Text      string        `json:"text"`
	Chat      TelegramChat  `json:"chat"`
	From      *TelegramUser `json:"from"`
}

// TelegramChat identifies the chat a message arrived in — the only field the
// bot needs, since delivery addresses a chat_id, never a username (see
// telegram_bot_manager.go for why).
type TelegramChat struct {
	ID int64 `json:"id"`
}

// TelegramUser is the sender of an incoming message.
type TelegramUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// TelegramUnreachableError reports that the Telegram Bot API could not be
// reached at all — the request never came back with an HTTP response, so
// nothing is known about the token, the chat, or the bot.
//
// It exists because the underlying failure says "context deadline exceeded",
// and a self-hoster reading that in a log will go looking at their token,
// their bot, their chat id and their clock long before they think to check
// whether the api container is allowed to make outbound HTTPS calls at all —
// which, on the instance this was diagnosed on, was the entire problem. The
// message names the host that could not be reached and the two things that fix
// it, so the log line is the answer rather than the start of a search.
type TelegramUnreachableError struct {
	// Host is the API endpoint that could not be reached, without the bot
	// token that appears in the full request URL.
	Host string
	// Method is the Bot API method that was being called.
	Method string
	// Err is the transport failure, with the bot token redacted.
	Err error
}

func (e *TelegramUnreachableError) Error() string {
	return fmt.Sprintf(
		"telegram %s: cannot reach %s — check that this host has outbound HTTPS (443) access to it, or set HTTPS_PROXY for the api container; verify with: curl -sS --max-time 10 %s (underlying error: %v)",
		e.Method, e.Host, e.Host, e.Err)
}

func (e *TelegramUnreachableError) Unwrap() error { return e.Err }

// redactToken removes the bot token from a transport error before it is
// logged. http.Client failures are *url.Error, whose message embeds the full
// request URL — and the Bot API puts the token in the path, so every
// connection failure was printing a live credential into the log.
func redactToken(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), token, "<bot-token-redacted>")
	if escaped := url.PathEscape(token); escaped != token {
		msg = strings.ReplaceAll(msg, escaped, "<bot-token-redacted>")
	}
	return errors.New(msg)
}

// TelegramAPIError wraps a non-OK response from the Telegram Bot API.
type TelegramAPIError struct {
	StatusCode  int
	ErrorCode   int
	Description string
}

func (e *TelegramAPIError) Error() string {
	return fmt.Sprintf("telegram API error (http %d, code %d): %s", e.StatusCode, e.ErrorCode, e.Description)
}

// Forbidden reports whether the bot was blocked or kicked by the recipient —
// this chat_id will never accept another message until they unblock it, so a
// caller sees this to stop delivering here rather than retry forever.
func (e *TelegramAPIError) Forbidden() bool {
	return e.StatusCode == http.StatusForbidden || e.ErrorCode == http.StatusForbidden
}

// TelegramClient talks to the Telegram Bot API on behalf of a single bot
// token, supplied per-call since a self-hosted instance may run one bot per
// workspace.
type TelegramClient interface {
	// GetMe validates the token and returns the bot's @username. Also serves
	// as the token-validation step when an admin saves a new token.
	GetMe(ctx context.Context, token string) (username string, err error)
	// SendMessage delivers a plain-text message to a chat. No parse_mode is
	// set: the body may carry a task title, comment, or workspace name a
	// workspace member wrote, and plain text means none of Telegram's
	// Markdown/HTML entity syntax needs escaping before it is sent.
	SendMessage(ctx context.Context, token string, chatID int64, text string) error
	// GetUpdates long-polls for new updates starting at offset (0 for "from
	// the beginning"), waiting up to timeoutSeconds for one to arrive. The
	// caller's context should allow at least that long.
	GetUpdates(ctx context.Context, token string, offset int64, timeoutSeconds int) ([]TelegramUpdate, error)
}

type httpTelegramClient struct {
	httpClient *http.Client
	baseURL    string
}

// NewTelegramClient creates a TelegramClient against the real Telegram Bot
// API. It sets no client-wide timeout — GetUpdates is a long poll and
// SendMessage/GetMe are quick, so callers bound each call with their own
// context instead of one timeout trying to fit both shapes.
func NewTelegramClient() TelegramClient {
	return &httpTelegramClient{httpClient: &http.Client{}, baseURL: TelegramAPIBaseURL}
}

// NewTelegramClientWithBaseURL points the client at a different base URL —
// used by tests to substitute an httptest.Server for the real network.
func NewTelegramClientWithBaseURL(baseURL string) TelegramClient {
	return &httpTelegramClient{httpClient: &http.Client{}, baseURL: baseURL}
}

func (c *httpTelegramClient) methodURL(token, method string) string {
	return fmt.Sprintf("%s/bot%s/%s", c.baseURL, url.PathEscape(token), method)
}

type tgAPIResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
}

func (c *httpTelegramClient) call(ctx context.Context, token, method string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal telegram %s request: %w", method, err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(token, method), reqBody)
	if err != nil {
		return fmt.Errorf("build telegram %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// No HTTP response came back at all: DNS, TCP, TLS, proxy or timeout.
		// Every one of those is "this host cannot talk to Telegram", which is
		// a different problem from anything the API itself can report.
		return &TelegramUnreachableError{Host: c.baseURL, Method: method, Err: redactToken(err, token)}
	}
	defer func() { _ = resp.Body.Close() }()

	var apiResp tgAPIResponse
	if decErr := json.NewDecoder(resp.Body).Decode(&apiResp); decErr != nil {
		return fmt.Errorf("telegram %s: decode response: %w", method, decErr)
	}
	if !apiResp.OK {
		return &TelegramAPIError{StatusCode: resp.StatusCode, ErrorCode: apiResp.ErrorCode, Description: apiResp.Description}
	}
	if out != nil && len(apiResp.Result) > 0 {
		if unErr := json.Unmarshal(apiResp.Result, out); unErr != nil {
			return fmt.Errorf("telegram %s: unmarshal result: %w", method, unErr)
		}
	}
	return nil
}

func (c *httpTelegramClient) GetMe(ctx context.Context, token string) (string, error) {
	var result struct {
		Username string `json:"username"`
	}
	if err := c.call(ctx, token, "getMe", nil, &result); err != nil {
		return "", err
	}
	if result.Username == "" {
		return "", errors.New("telegram getMe: bot has no username")
	}
	return result.Username, nil
}

func (c *httpTelegramClient) SendMessage(ctx context.Context, token string, chatID int64, text string) error {
	body := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	}
	return c.call(ctx, token, "sendMessage", body, nil)
}

func (c *httpTelegramClient) GetUpdates(ctx context.Context, token string, offset int64, timeoutSeconds int) ([]TelegramUpdate, error) {
	body := map[string]any{
		"offset":          offset,
		"timeout":         timeoutSeconds,
		"allowed_updates": []string{"message"},
	}
	var result []TelegramUpdate
	if err := c.call(ctx, token, "getUpdates", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}
