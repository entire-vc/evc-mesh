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
