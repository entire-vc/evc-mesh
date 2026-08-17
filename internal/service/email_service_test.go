package service

import (
	"context"
	"errors"
	"net/smtp"
	"strings"
	"testing"

	"github.com/entire-vc/evc-mesh/internal/config"
)

func stubSendMail(t *testing.T, fn func(addr string, a smtp.Auth, from string, to []string, msg []byte) error) {
	t.Helper()
	orig := sendMailFunc
	sendMailFunc = fn
	t.Cleanup(func() { sendMailFunc = orig })
}

func TestSendInvite_DisplayNameFromYieldsBareEnvelopeSender(t *testing.T) {
	var gotFrom string
	stubSendMail(t, func(_ string, _ smtp.Auth, from string, _ []string, _ []byte) error {
		gotFrom = from
		return nil
	})

	svc := NewEmailService(config.EmailConfig{Host: "smtp.example.com", Port: 587, From: "Mesh <noreply@example.com>"})
	if err := svc.SendInvite(context.Background(), "user@example.com", "Acme", "https://mesh.example.com/invite/abc"); err != nil {
		t.Fatalf("SendInvite returned error: %v", err)
	}
	if gotFrom != "noreply@example.com" {
		t.Errorf("envelope sender = %q, want bare address %q", gotFrom, "noreply@example.com")
	}
}

func TestSendInvite_BareFromIsDeliveredUnchanged(t *testing.T) {
	var gotFrom string
	stubSendMail(t, func(_ string, _ smtp.Auth, from string, _ []string, _ []byte) error {
		gotFrom = from
		return nil
	})

	svc := NewEmailService(config.EmailConfig{Host: "smtp.example.com", Port: 587, From: "noreply@example.com"})
	if err := svc.SendInvite(context.Background(), "user@example.com", "Acme", "https://mesh.example.com/invite/abc"); err != nil {
		t.Fatalf("SendInvite returned error: %v", err)
	}
	if gotFrom != "noreply@example.com" {
		t.Errorf("envelope sender = %q, want %q", gotFrom, "noreply@example.com")
	}
}

func TestSendInvite_EmptyFromIsConfigErrorNotSilentDrop(t *testing.T) {
	called := false
	stubSendMail(t, func(_ string, _ smtp.Auth, _ string, _ []string, _ []byte) error {
		called = true
		return nil
	})

	svc := NewEmailService(config.EmailConfig{Host: "smtp.example.com", Port: 587, From: ""})
	err := svc.SendInvite(context.Background(), "user@example.com", "Acme", "https://mesh.example.com/invite/abc")
	if err == nil {
		t.Fatal("expected an error for empty SMTP_FROM, got nil")
	}
	if called {
		t.Error("sendMailFunc must not be called when SMTP_FROM is empty")
	}
}

func TestSendInvite_InvalidFromIsConfigError(t *testing.T) {
	called := false
	stubSendMail(t, func(_ string, _ smtp.Auth, _ string, _ []string, _ []byte) error {
		called = true
		return nil
	})

	svc := NewEmailService(config.EmailConfig{Host: "smtp.example.com", Port: 587, From: "not an email address"})
	err := svc.SendInvite(context.Background(), "user@example.com", "Acme", "https://mesh.example.com/invite/abc")
	if err == nil {
		t.Fatal("expected an error for invalid SMTP_FROM, got nil")
	}
	if called {
		t.Error("sendMailFunc must not be called when SMTP_FROM is invalid")
	}
}

func TestSendInvite_TransportErrorStillPropagates(t *testing.T) {
	wantErr := errors.New(`501 "Error: Bad sender address syntax"`)
	stubSendMail(t, func(_ string, _ smtp.Auth, _ string, _ []string, _ []byte) error {
		return wantErr
	})

	svc := NewEmailService(config.EmailConfig{Host: "smtp.example.com", Port: 587, From: "noreply@example.com"})
	err := svc.SendInvite(context.Background(), "user@example.com", "Acme", "https://mesh.example.com/invite/abc")
	if err == nil || !strings.Contains(err.Error(), "Bad sender address syntax") {
		t.Fatalf("expected wrapped transport error, got %v", err)
	}
}

func TestSendInvite_RecipientWithCRLFIsRejected(t *testing.T) {
	called := false
	stubSendMail(t, func(_ string, _ smtp.Auth, _ string, _ []string, _ []byte) error {
		called = true
		return nil
	})

	svc := NewEmailService(config.EmailConfig{Host: "smtp.example.com", Port: 587, From: "noreply@example.com"})
	err := svc.SendInvite(context.Background(), "victim@example.com\r\nBcc: attacker@evil.com", "Acme", "https://mesh.example.com/invite/abc")
	if err == nil {
		t.Fatal("expected an error for a recipient address containing CRLF, got nil")
	}
	if called {
		t.Error("sendMailFunc must not be called for an invalid recipient address")
	}
}

func TestSendInvite_DisabledWhenHostEmpty(t *testing.T) {
	svc := NewEmailService(config.EmailConfig{Host: "", From: "noreply@example.com"})
	if svc.Enabled() {
		t.Fatal("Enabled() should be false when Host is empty")
	}
	err := svc.SendInvite(context.Background(), "user@example.com", "Acme", "https://mesh.example.com/invite/abc")
	if !errors.Is(err, ErrEmailNotConfigured) {
		t.Fatalf("expected ErrEmailNotConfigured, got %v", err)
	}
}

// --- SendNotification --------------------------------------------------------
//
// SendNotification shares its envelope/recipient handling with SendInvite via
// the private send() helper, so it is not re-tested for every case above —
// just the entry points a caller (notificationService.dispatch) actually
// exercises: disabled-is-an-error, a configured send reaches sendMailFunc with
// the right recipient/subject, and a transport error propagates.

func TestSendNotification_DisabledIsConfigErrorNotSilentDrop(t *testing.T) {
	called := false
	stubSendMail(t, func(_ string, _ smtp.Auth, _ string, _ []string, _ []byte) error {
		called = true
		return nil
	})

	svc := NewEmailService(config.EmailConfig{Host: "", From: "noreply@example.com"})
	err := svc.SendNotification(context.Background(), "user@example.com", "Task assigned", "<p>hi</p>")
	if !errors.Is(err, ErrEmailNotConfigured) {
		t.Fatalf("expected ErrEmailNotConfigured, got %v", err)
	}
	if called {
		t.Error("sendMailFunc must not be called when SMTP is not configured")
	}
}

func TestSendNotification_DeliversToTheGivenRecipientWithSubject(t *testing.T) {
	var gotTo []string
	var gotMsg []byte
	stubSendMail(t, func(_ string, _ smtp.Auth, _ string, to []string, msg []byte) error {
		gotTo = to
		gotMsg = msg
		return nil
	})

	svc := NewEmailService(config.EmailConfig{Host: "smtp.example.com", Port: 587, From: "noreply@example.com"})
	err := svc.SendNotification(context.Background(), "user@example.com", "Task assigned", "<p>You were assigned a task</p>")
	if err != nil {
		t.Fatalf("SendNotification returned error: %v", err)
	}
	if len(gotTo) != 1 || gotTo[0] != "user@example.com" {
		t.Errorf("recipients = %v, want [user@example.com]", gotTo)
	}
	if !strings.Contains(string(gotMsg), "Subject: Task assigned") {
		t.Errorf("message missing expected subject line: %s", gotMsg)
	}
	if !strings.Contains(string(gotMsg), "<p>You were assigned a task</p>") {
		t.Errorf("message missing expected body: %s", gotMsg)
	}
}

// TestSendNotification_SubjectCRLFIsStripped: subject comes from a task
// title or comment body a workspace member wrote, with nothing upstream
// forbidding a newline in it. Unstripped, that CRLF would inject arbitrary
// extra SMTP headers into the raw message (CWE-93) — e.g. a forged Bcc
// silently copying the notification elsewhere. Flagged by CodeQL as email
// content injection.
func TestSendNotification_SubjectCRLFIsStripped(t *testing.T) {
	var gotMsg []byte
	stubSendMail(t, func(_ string, _ smtp.Auth, _ string, _ []string, msg []byte) error {
		gotMsg = msg
		return nil
	})

	svc := NewEmailService(config.EmailConfig{Host: "smtp.example.com", Port: 587, From: "noreply@example.com"})
	err := svc.SendNotification(context.Background(), "user@example.com",
		"Task assigned\r\nBcc: attacker@evil.com", "<p>hi</p>")
	if err != nil {
		t.Fatalf("SendNotification returned error: %v", err)
	}
	// The security property is "no injected header line", not "the words never
	// appear" — with the CRLF stripped, "Bcc: ..." is just inert trailing text
	// inside the Subject value, not a new header.
	if strings.Contains(string(gotMsg), "\r\nBcc:") {
		t.Fatalf("subject CRLF was not stripped, message carries an injected header: %s", gotMsg)
	}
	if !strings.Contains(string(gotMsg), "Subject: Task assignedBcc: attacker@evil.com") {
		t.Fatalf("expected the CRLF removed but the rest of the subject kept, got: %s", gotMsg)
	}
}

// TestSendNotification_BodyNewlinesBecomeBR: unlike a header, htmlBody may
// legitimately contain the sender's line breaks (a multi-line comment), so
// send() converts them to <br> instead of stripping them — readable in the
// rendered email, and no raw \r/\n bytes reach the wire either way.
func TestSendNotification_BodyNewlinesBecomeBR(t *testing.T) {
	var gotMsg []byte
	stubSendMail(t, func(_ string, _ smtp.Auth, _ string, _ []string, msg []byte) error {
		gotMsg = msg
		return nil
	})

	svc := NewEmailService(config.EmailConfig{Host: "smtp.example.com", Port: 587, From: "noreply@example.com"})
	err := svc.SendNotification(context.Background(), "user@example.com", "Subject", "line one\r\nline two\nline three")
	if err != nil {
		t.Fatalf("SendNotification returned error: %v", err)
	}
	if strings.Contains(string(gotMsg), "\r\nline two") || strings.Contains(string(gotMsg), "\nline three") {
		t.Fatalf("raw newline reached the message body: %s", gotMsg)
	}
	if !strings.Contains(string(gotMsg), "line one<br>line two<br>line three") {
		t.Fatalf("expected newlines converted to <br>, got: %s", gotMsg)
	}
}

func TestSendNotification_TransportErrorPropagates(t *testing.T) {
	wantErr := errors.New("connection refused")
	stubSendMail(t, func(_ string, _ smtp.Auth, _ string, _ []string, _ []byte) error {
		return wantErr
	})

	svc := NewEmailService(config.EmailConfig{Host: "smtp.example.com", Port: 587, From: "noreply@example.com"})
	err := svc.SendNotification(context.Background(), "user@example.com", "Task assigned", "<p>hi</p>")
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("expected wrapped transport error, got %v", err)
	}
}

// --- buildNotificationEmail ---------------------------------------------------

// TestBuildNotificationEmail_EscapesUserContent: title and body come from the
// triggering event — a task title or a comment's text, both user-authored —
// so a value containing markup must not reach the HTML message unescaped. An
// unescaped comment body would let anyone who can comment on a task inject
// markup into every subscriber's notification email.
func TestBuildNotificationEmail_EscapesUserContent(t *testing.T) {
	html := buildNotificationEmail(`<img src=x onerror=alert(1)>`, `<script>alert(2)</script>`, "https://mesh.example.com/t/abc")
	if strings.Contains(html, "<img src=x") || strings.Contains(html, "<script>") {
		t.Fatalf("event title/body were embedded unescaped: %s", html)
	}
	if !strings.Contains(html, "&lt;img src=x") || !strings.Contains(html, "&lt;script&gt;") {
		t.Fatalf("expected HTML-escaped title/body, got: %s", html)
	}
}

func TestBuildNotificationEmail_OmitsLinkWhenTaskURLEmpty(t *testing.T) {
	html := buildNotificationEmail("Task assigned", "body", "")
	if strings.Contains(html, "View task") {
		t.Fatalf("expected no task link when taskURL is empty, got: %s", html)
	}
}
