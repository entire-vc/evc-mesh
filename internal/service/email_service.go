package service

import (
	"context"
	"fmt"
	"log"
	"net/smtp"
	"strings"

	"github.com/entire-vc/evc-mesh/internal/config"
)

// EmailService sends transactional emails.
type EmailService interface {
	SendInvite(ctx context.Context, toEmail, workspaceName, inviteURL string) error
}

type smtpEmailService struct {
	cfg config.EmailConfig
}

// NewEmailService creates an EmailService backed by SMTP.
// When cfg.Host is empty, email sending is disabled; invite URLs are logged instead.
func NewEmailService(cfg config.EmailConfig) EmailService {
	return &smtpEmailService{cfg: cfg}
}

func (s *smtpEmailService) SendInvite(_ context.Context, toEmail, workspaceName, inviteURL string) error {
	if s.cfg.Host == "" {
		log.Printf("[email] SMTP not configured — invite link for %s: %s", toEmail, inviteURL)
		return nil
	}

	subject := fmt.Sprintf("You've been invited to %s on Mesh", workspaceName)
	body := buildInviteEmail(toEmail, workspaceName, inviteURL)

	msg := "MIME-version: 1.0;\nContent-Type: text/html; charset=\"UTF-8\";\r\n"
	msg += fmt.Sprintf("From: Mesh <%s>\r\n", s.cfg.From)
	msg += fmt.Sprintf("To: %s\r\n", toEmail)
	msg += fmt.Sprintf("Subject: %s\r\n", subject)
	msg += "\r\n" + body

	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	var auth smtp.Auth
	if s.cfg.User != "" {
		auth = smtp.PlainAuth("", s.cfg.User, s.cfg.Pass, s.cfg.Host)
	}

	if err := smtp.SendMail(addr, auth, s.cfg.From, []string{toEmail}, []byte(msg)); err != nil {
		return fmt.Errorf("email_service.SendInvite: %w", err)
	}
	return nil
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
