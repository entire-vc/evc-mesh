package service

import (
	"context"
	"errors"
	"fmt"
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

	envelopeFrom, headerFrom, err := s.parseFrom()
	if err != nil {
		return fmt.Errorf("email_service.SendInvite: %w", err)
	}

	subject := fmt.Sprintf("You've been invited to %s on Mesh", workspaceName)
	body := buildInviteEmail(toEmail, workspaceName, inviteURL)

	msg := "MIME-version: 1.0;\nContent-Type: text/html; charset=\"UTF-8\";\r\n"
	msg += fmt.Sprintf("From: %s\r\n", headerFrom)
	msg += fmt.Sprintf("To: %s\r\n", toEmail)
	msg += fmt.Sprintf("Subject: %s\r\n", subject)
	msg += "\r\n" + body

	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	var auth smtp.Auth
	if s.cfg.User != "" {
		auth = smtp.PlainAuth("", s.cfg.User, s.cfg.Pass, s.cfg.Host)
	}

	if err := sendMailFunc(addr, auth, envelopeFrom, []string{toEmail}, []byte(msg)); err != nil {
		return fmt.Errorf("email_service.SendInvite: %w", err)
	}
	return nil
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
