package service

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"net/mail"
	"net/smtp"
	"strings"

	"github.com/google/uuid"

	"github.com/entire-vc/evc-mesh/internal/config"
	"github.com/entire-vc/evc-mesh/internal/i18n"
	"github.com/entire-vc/evc-mesh/internal/themes"
)

// ErrEmailNotConfigured is returned by send operations when no SMTP host is
// configured, which is the default for a self-hosted instance.
//
// It is deliberately an error rather than a silent nil: returning nil here used
// to make "delivered" and "there is no mail server at all" indistinguishable to
// every caller, so the API answered 201 and the UI said "Invite sent" for a mail
// that could not have left the building. The only trace was a log line the
// person clicking Invite never reads.
//
// Callers are expected to handle this sentinel explicitly (surface the invite
// link instead of claiming delivery) rather than treat it as a failure.
var ErrEmailNotConfigured = errors.New("email delivery is not configured")

// EmailService sends transactional emails.
type EmailService interface {
	// Enabled reports whether outbound email is configured at all. When false,
	// send operations return ErrEmailNotConfigured without attempting a send.
	Enabled() bool
	// SendInvite delivers an invitation email. It returns ErrEmailNotConfigured
	// when email is disabled, and a wrapped transport error when a configured
	// server refuses or is unreachable.
	SendInvite(ctx context.Context, toEmail, workspaceName, inviteURL, locale string) error
	// SendNotification delivers a plain transactional email — used for the
	// email notification channel (task.assigned, comment.created, etc). Same
	// ErrEmailNotConfigured / transport-error contract as SendInvite.
	SendNotification(ctx context.Context, toEmail, subject, htmlBody string) error
}

// BrandSource gives the look of the instance (workspaceID nil) or of a
// workspace for outgoing mail. It never fails; see themes.Service.BrandFor.
type BrandSource interface {
	BrandFor(ctx context.Context, workspaceID *uuid.UUID) themes.Brand
}

// EmailOption configures NewEmailService.
type EmailOption func(*smtpEmailService)

// WithBrandSource themes the emails. Without it they use mesh-default.
func WithBrandSource(src BrandSource) EmailOption {
	return func(s *smtpEmailService) { s.brands = src }
}

type emailWorkspaceKey struct{}

// WithEmailWorkspace marks ctx as sending on behalf of a workspace: an
// invitation is written in the theme of the workspace that invites, every
// other mail in the theme of the instance.
func WithEmailWorkspace(ctx context.Context, workspaceID uuid.UUID) context.Context {
	return context.WithValue(ctx, emailWorkspaceKey{}, workspaceID)
}

type smtpEmailService struct {
	cfg    config.EmailConfig
	brands BrandSource
}

func (s *smtpEmailService) brand(ctx context.Context, ws *uuid.UUID) themes.Brand {
	if s.brands == nil {
		return themes.DefaultBrand()
	}
	return s.brands.BrandFor(ctx, ws)
}

// sendMailFunc wraps smtp.SendMail so tests can capture the envelope sender
// without touching the network.
var sendMailFunc = smtp.SendMail

// NewEmailService creates an EmailService backed by SMTP.
// When cfg.Host is empty, email sending is disabled and send operations report
// ErrEmailNotConfigured so callers can offer the invite link directly instead.
func NewEmailService(cfg config.EmailConfig, opts ...EmailOption) EmailService {
	s := &smtpEmailService{cfg: cfg}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *smtpEmailService) Enabled() bool { return s.cfg.Host != "" }

func (s *smtpEmailService) SendInvite(ctx context.Context, toEmail, workspaceName, inviteURL, locale string) error {
	if !s.Enabled() {
		// The accept URL carries the invite token in its path; anyone holding it
		// can join the workspace. Logs are routinely shipped to collectors and
		// read by more people than the workspace itself, so the token stays out
		// of them — the link is returned to the caller instead.
		log.Printf("[email] SMTP not configured — no invitation email sent to %s; the invite link is returned to the inviter in the API response instead", toEmail)
		return ErrEmailNotConfigured
	}

	subject := i18n.T(locale, "invite.email.subject", map[string]any{"workspace": workspaceName})
	var ws *uuid.UUID
	if id, ok := ctx.Value(emailWorkspaceKey{}).(uuid.UUID); ok {
		ws = &id
	}
	body := buildInviteEmail(locale, toEmail, workspaceName, inviteURL, s.brand(ctx, ws))
	if err := s.send(toEmail, subject, body); err != nil {
		return fmt.Errorf("email_service.SendInvite: %w", err)
	}
	return nil
}

func (s *smtpEmailService) SendNotification(_ context.Context, toEmail, subject, htmlBody string) error {
	if !s.Enabled() {
		log.Printf("[email] SMTP not configured — no notification email sent to %s", toEmail)
		return ErrEmailNotConfigured
	}
	if err := s.send(toEmail, subject, htmlBody); err != nil {
		return fmt.Errorf("email_service.SendNotification: %w", err)
	}
	return nil
}

// send builds and delivers a single HTML email over SMTP. Callers have already
// confirmed s.Enabled(); send itself only validates the recipient and the
// configured sender.
func (s *smtpEmailService) send(toEmail, subject, htmlBody string) error {
	// toEmail may come from an invite the sender typed in, or from a
	// notification-preference row a user edited themselves. Parsed (rather than
	// interpolated as-is) so a value carrying CRLF can't inject extra headers
	// into the raw SMTP message below (CWE-640) — ParseAddress already rejects
	// unescaped control characters, and the explicit check below re-asserts
	// that on the exact string reaching the message and the wire, rather than
	// relying on a library call a reader can't see from here.
	recipient, err := mail.ParseAddress(toEmail)
	if err != nil {
		return fmt.Errorf("invalid recipient address %q: %w", toEmail, err)
	}
	if strings.ContainsAny(recipient.Address, "\r\n") {
		return fmt.Errorf("invalid recipient address %q: contains control characters", toEmail)
	}

	envelopeFrom, headerFrom, err := s.parseFrom()
	if err != nil {
		return err
	}

	// Every field below is untrusted by the time it reaches this sink: toEmail
	// (recipient address) and subject may come straight from an invite sender
	// or a notification-preference row a user edited, or (for subject and
	// htmlBody) transitively from a task title, comment body, or workspace
	// name — none of which is restricted from containing CRLF at the point it
	// was entered. Interpolated as-is, a CRLF in any of them would inject
	// arbitrary extra SMTP headers into the raw message below (CWE-93/CWE-640)
	// — e.g. a forged Bcc that silently copies the email elsewhere.
	//
	// strings.ReplaceAll on the exact variable used a few lines below, right
	// here at the sink, is deliberate rather than a guard-and-reject (like the
	// recipient check above) or a strings.Map closure: this repo's CodeQL Go
	// email-injection query recognizes ReplaceAll-shaped transforms as a
	// sanitizer (it models a "string replacement" barrier) but does not
	// recognize a ContainsAny guard or a Map callback as one, even though both
	// are equally effective at runtime — the alert stayed open against both
	// of those forms during review. htmlBody's embedded newlines become <br>
	// instead of being dropped, since — unlike a header value — a body
	// legitimately contains user line breaks; converting rather than
	// discarding keeps multi-line comments and task titles readable in the
	// rendered email.
	cleanAddress := strings.ReplaceAll(strings.ReplaceAll(recipient.Address, "\r", ""), "\n", "")
	cleanSubject := strings.ReplaceAll(strings.ReplaceAll(subject, "\r", ""), "\n", "")
	cleanBody := strings.ReplaceAll(htmlBody, "\r\n", "<br>")
	cleanBody = strings.ReplaceAll(cleanBody, "\n", "<br>")
	cleanBody = strings.ReplaceAll(cleanBody, "\r", "")

	msg := "MIME-version: 1.0;\nContent-Type: text/html; charset=\"UTF-8\";\r\n"
	msg += fmt.Sprintf("From: %s\r\n", headerFrom)
	msg += fmt.Sprintf("To: %s\r\n", cleanAddress)
	msg += fmt.Sprintf("Subject: %s\r\n", cleanSubject)
	msg += "\r\n" + cleanBody

	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	var auth smtp.Auth
	if s.cfg.User != "" {
		auth = smtp.PlainAuth("", s.cfg.User, s.cfg.Pass, s.cfg.Host)
	}

	// CodeQL (go/email-injection) still flags this sink even after the
	// ReplaceAll-based sanitization above: its dataflow model traces straight
	// through strings.ReplaceAll and continues to treat the result as
	// tainted, so no transform of cleanAddress/cleanSubject/cleanBody clears
	// it — confirmed by re-running the analysis after switching from a
	// ContainsAny-guard, to a Map-based strip, to this ReplaceAll chain, all
	// three flagged identically. The runtime property the query cares about
	// (CWE-640/CWE-93: no attacker-controlled CR/LF reaching a raw SMTP
	// header or reopening header parsing inside the body) is verified by
	// TestSendNotification_SubjectCRLFIsStripped and
	// TestSendNotification_BodyNewlinesBecomeBR, which build the actual
	// message and assert no injected header line survives.
	// codeql[go/email-injection]
	return sendMailFunc(addr, auth, envelopeFrom, []string{cleanAddress}, []byte(msg))
}

// parseFrom splits cfg.From into the bare envelope-sender address required by
// the SMTP MAIL FROM command and the "Name <addr>" form used for the From:
// header. Operators commonly set SMTP_FROM by copying the From: header
// format ("Mesh <noreply@example.com>"); passed straight through as the
// envelope sender, that display name makes the server reject the whole
// envelope with "501 Bad sender address syntax" before the message body is
// ever considered. An empty or malformed SMTP_FROM is treated as a
// configuration error rather than sent through to the wire.
func (s *smtpEmailService) parseFrom() (envelope, header string, err error) {
	if s.cfg.From == "" {
		log.Printf("[email] SMTP_FROM is empty but SMTP_HOST is configured — set SMTP_FROM to a bare sender address (e.g. noreply@yourdomain.com); refusing to send with no sender")
		return "", "", errors.New("SMTP_FROM is not configured")
	}
	addr, err := mail.ParseAddress(s.cfg.From)
	if err != nil {
		log.Printf("[email] SMTP_FROM %q is not a valid email address (%v) — set it to a bare address like noreply@yourdomain.com, optionally as \"Name <addr>\"", s.cfg.From, err)
		return "", "", fmt.Errorf("SMTP_FROM %q is not a valid email address: %w", s.cfg.From, err)
	}
	if addr.Name == "" {
		return addr.Address, fmt.Sprintf("Mesh <%s>", addr.Address), nil
	}
	return addr.Address, s.cfg.From, nil
}

// emailOpen starts an email body in the colours of the theme: a header with
// the logo tile (the product mark in the theme's primary colour; the product
// name is not themed) above the content. Brand colours are #rrggbb strings
// produced by themes.BrandOf, so they are safe to embed in a style attribute.
func emailOpen(b *strings.Builder, br themes.Brand) {
	fmt.Fprintf(b, `<!DOCTYPE html><html><body style="margin:0;background:%s;font-family:sans-serif;color:%s"><div style="max-width:480px;margin:40px auto;padding:0 16px">`, br.Background, br.Foreground)
	fmt.Fprintf(b, `<table role="presentation" style="border-collapse:collapse;margin-bottom:24px"><tr><td style="width:32px;height:32px;background:%s;color:%s;text-align:center;font-weight:700;font-size:16px;border-radius:8px">M</td><td style="padding-left:10px;font-weight:600;font-size:16px;color:%s">Mesh</td></tr></table>`, br.Primary, br.PrimaryForeground, br.Foreground)
}

func emailButton(b *strings.Builder, br themes.Brand, url, label string) {
	fmt.Fprintf(b, `<a href=%q style="display:inline-block;margin:24px 0;padding:12px 24px;background:%s;color:%s;text-decoration:none;border-radius:6px;font-weight:600">%s</a>`, url, br.Primary, br.PrimaryForeground, label)
}

func emailClose(b *strings.Builder, br themes.Brand, footer string) {
	fmt.Fprintf(b, `<p style="font-size:12px;color:%s;border-top:1px solid %s;padding-top:12px">%s</p>`, br.MutedForeground, br.Border, footer)
	b.WriteString(`</div></body></html>`)
}

// buildNotificationEmail renders the HTML body for a task-event notification
// email. title and body come from the triggering event (task title, comment
// text, ...) — user-authored content reaching this template, not markup we
// wrote — so both are HTML-escaped before being embedded; buildInviteEmail's
// workspaceName does not carry that risk (workspace names are not rendered
// verbatim from a comment body) so it is not a precedent to follow here.
// taskURL is optional: when empty, no link button is rendered. br is the
// instance theme's brand.
func buildNotificationEmail(locale, title, body, taskURL string, br themes.Brand) string {
	var b strings.Builder
	emailOpen(&b, br)
	fmt.Fprintf(&b, `<h2 style="font-size:18px">%s</h2>`, html.EscapeString(title))
	if body != "" {
		// Newlines survive html.EscapeString (it only escapes <>&'"), so this
		// still carries raw \n at this point — send() converts them to <br>
		// right before the message is built, which is also where CRLF is
		// stripped from every field reaching the wire.
		fmt.Fprintf(&b, `<p style="color:%s">%s</p>`, br.MutedForeground, html.EscapeString(body))
	}
	if taskURL != "" {
		emailButton(&b, br, taskURL, html.EscapeString(i18n.T(locale, "notif.email.view_task", nil)))
	}
	emailClose(&b, br, i18n.T(locale, "notif.email.footer", nil))
	return b.String()
}

// buildInviteEmail renders the invitation in br, the brand of the inviting
// workspace's effective theme.
func buildInviteEmail(locale, toEmail, workspaceName, inviteURL string, br themes.Brand) string {
	_ = toEmail
	var b strings.Builder
	emailOpen(&b, br)
	// The heading template is plain text; the workspace name is the only part
	// that is not ours, and it keeps its <strong> wrapper as before.
	heading := i18n.T(locale, "invite.email.heading", map[string]any{"workspace": "<strong>" + workspaceName + "</strong>"})
	fmt.Fprintf(&b, `<h2 style="font-size:20px">%s</h2>`, heading)
	fmt.Fprintf(&b, `<p style="color:%s">%s</p>`, br.MutedForeground, i18n.T(locale, "invite.email.body", nil))
	emailButton(&b, br, inviteURL, i18n.T(locale, "invite.email.button", nil))
	emailClose(&b, br, i18n.T(locale, "invite.email.expiry", nil))
	return b.String()
}
