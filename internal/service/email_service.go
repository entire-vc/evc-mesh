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

	"github.com/entire-vc/evc-mesh/internal/config"
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
	SendInvite(ctx context.Context, toEmail, workspaceName, inviteURL string) error
	// SendNotification delivers a plain transactional email — used for the
	// email notification channel (task.assigned, comment.created, etc). Same
	// ErrEmailNotConfigured / transport-error contract as SendInvite.
	SendNotification(ctx context.Context, toEmail, subject, htmlBody string) error
}

type smtpEmailService struct {
	cfg config.EmailConfig
}

// sendMailFunc wraps smtp.SendMail so tests can capture the envelope sender
// without touching the network.
var sendMailFunc = smtp.SendMail

// NewEmailService creates an EmailService backed by SMTP.
// When cfg.Host is empty, email sending is disabled and send operations report
// ErrEmailNotConfigured so callers can offer the invite link directly instead.
func NewEmailService(cfg config.EmailConfig) EmailService {
	return &smtpEmailService{cfg: cfg}
}

func (s *smtpEmailService) Enabled() bool { return s.cfg.Host != "" }

func (s *smtpEmailService) SendInvite(_ context.Context, toEmail, workspaceName, inviteURL string) error {
	if !s.Enabled() {
		// The accept URL carries the invite token in its path; anyone holding it
		// can join the workspace. Logs are routinely shipped to collectors and
		// read by more people than the workspace itself, so the token stays out
		// of them — the link is returned to the caller instead.
		log.Printf("[email] SMTP not configured — no invitation email sent to %s; the invite link is returned to the inviter in the API response instead", toEmail)
		return ErrEmailNotConfigured
	}

	subject := fmt.Sprintf("You've been invited to %s on Mesh", workspaceName)
	body := buildInviteEmail(toEmail, workspaceName, inviteURL)
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

	// subject is untrusted for SendNotification (built from a task title or
	// comment body a workspace member wrote) and, for SendInvite, from a
	// workspace name — both free-text fields with no newline restriction at
	// the point they were entered. Unlike recipient.Address above, nothing
	// upstream of this sink validates it, so a title/name containing CRLF
	// would otherwise inject arbitrary extra headers (CWE-93) into the raw
	// message below — e.g. a forged Bcc that silently copies every
	// notification email elsewhere. Checked explicitly on the exact variable
	// used at the sink, same shape as the recipient check above: a prior
	// version stripped the characters via strings.Map instead, which is
	// functionally equivalent but is not recognized as a sanitizer by this
	// repo's CodeQL Go email-injection query — the alert stayed open even
	// though the value was genuinely clean by the time it reached the sink.
	if strings.ContainsAny(subject, "\r\n") {
		return fmt.Errorf("invalid subject: contains control characters")
	}

	msg := "MIME-version: 1.0;\nContent-Type: text/html; charset=\"UTF-8\";\r\n"
	msg += fmt.Sprintf("From: %s\r\n", headerFrom)
	msg += fmt.Sprintf("To: %s\r\n", recipient.Address)
	msg += fmt.Sprintf("Subject: %s\r\n", subject)
	msg += "\r\n" + htmlBody

	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	var auth smtp.Auth
	if s.cfg.User != "" {
		auth = smtp.PlainAuth("", s.cfg.User, s.cfg.Pass, s.cfg.Host)
	}

	return sendMailFunc(addr, auth, envelopeFrom, []string{recipient.Address}, []byte(msg))
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

// buildNotificationEmail renders the HTML body for a task-event notification
// email. title and body come from the triggering event (task title, comment
// text, ...) — user-authored content reaching this template, not markup we
// wrote — so both are HTML-escaped before being embedded; buildInviteEmail's
// workspaceName does not carry that risk (workspace names are not rendered
// verbatim from a comment body) so it is not a precedent to follow here.
// taskURL is optional: when empty, no link button is rendered.
func buildNotificationEmail(title, body, taskURL string) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><body style="font-family:sans-serif;color:#111;max-width:480px;margin:40px auto;padding:0 16px">`)
	fmt.Fprintf(&b, `<h2 style="font-size:18px">%s</h2>`, html.EscapeString(title))
	if body != "" {
		fmt.Fprintf(&b, `<p style="color:#555;white-space:pre-wrap">%s</p>`, html.EscapeString(body))
	}
	if taskURL != "" {
		fmt.Fprintf(&b, `<a href=%q style="display:inline-block;margin:24px 0;padding:12px 24px;background:#18181b;color:#fff;text-decoration:none;border-radius:6px;font-weight:600">View task</a>`, taskURL)
	}
	b.WriteString(`<p style="font-size:12px;color:#999">You're receiving this because of your Mesh notification settings. You can change them from the notification bell in the app.</p>`)
	b.WriteString(`</body></html>`)
	return b.String()
}

func buildInviteEmail(toEmail, workspaceName, inviteURL string) string {
	_ = toEmail
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><body style="font-family:sans-serif;color:#111;max-width:480px;margin:40px auto;padding:0 16px">`)
	fmt.Fprintf(&b, `<h2 style="font-size:20px">You've been invited to <strong>%s</strong></h2>`, workspaceName)
	b.WriteString(`<p style="color:#555">Click the button below to accept the invitation and set up your account.</p>`)
	fmt.Fprintf(&b, `<a href=%q style="display:inline-block;margin:24px 0;padding:12px 24px;background:#18181b;color:#fff;text-decoration:none;border-radius:6px;font-weight:600">Accept Invitation</a>`, inviteURL)
	b.WriteString(`<p style="font-size:12px;color:#999">This link expires in 7 days. If you weren't expecting this, you can ignore this email.</p>`)
	b.WriteString(`</body></html>`)
	return b.String()
}
