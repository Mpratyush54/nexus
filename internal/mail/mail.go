// Package mail sends transactional email (signup OTP) via Resend or SMTP.
package mail

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"os"
	"strings"
	"time"
)

// Sender delivers plain-text (and optional HTML) messages.
type Sender interface {
	Send(to, subject, textBody string) error
	Configured() bool
}

// FromEnv picks Resend, then SMTP, then a log-only sender for local/dev.
func FromEnv() Sender {
	if key := strings.TrimSpace(os.Getenv("RESEND_API_KEY")); key != "" {
		from := firstNonEmpty(os.Getenv("MAIL_FROM"), "Nexus <noreply@pratyushes.dev>")
		return &Resend{APIKey: key, From: from, HTTP: &http.Client{Timeout: 15 * time.Second}}
	}
	host := strings.TrimSpace(os.Getenv("SMTP_HOST"))
	if host != "" {
		return &SMTP{
			Host:     host,
			Port:     firstNonEmpty(os.Getenv("SMTP_PORT"), "587"),
			User:     strings.TrimSpace(os.Getenv("SMTP_USER")),
			Password: os.Getenv("SMTP_PASSWORD"),
			From:     firstNonEmpty(os.Getenv("MAIL_FROM"), os.Getenv("SMTP_FROM"), "Nexus <noreply@pratyushes.dev>"),
		}
	}
	return &LogSender{}
}

// Resend uses https://api.resend.com/emails
type Resend struct {
	APIKey string
	From   string
	HTTP   *http.Client
}

func (r *Resend) Configured() bool { return strings.TrimSpace(r.APIKey) != "" }

func (r *Resend) Send(to, subject, textBody string) error {
	payload, _ := json.Marshal(map[string]any{
		"from":    r.From,
		"to":      []string{to},
		"subject": subject,
		"text":    textBody,
	})
	req, err := http.NewRequest(http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := r.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("resend: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// SMTP sends via net/smtp (STARTTLS on 587 when auth is set).
type SMTP struct {
	Host, Port, User, Password, From string
}

func (s *SMTP) Configured() bool {
	return strings.TrimSpace(s.Host) != "" && strings.TrimSpace(s.Password) != ""
}

func (s *SMTP) Send(to, subject, textBody string) error {
	addr := s.Host + ":" + s.Port
	msg := []byte("From: " + s.From + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" + textBody + "\r\n")
	if s.User != "" {
		auth := smtp.PlainAuth("", s.User, s.Password, s.Host)
		return smtp.SendMail(addr, auth, s.From, []string{to}, msg)
	}
	return smtp.SendMail(addr, nil, s.From, []string{to}, msg)
}

// LogSender writes to stderr (local / when mail is not configured).
type LogSender struct{}

func (LogSender) Configured() bool { return false }

func (LogSender) Send(to, subject, textBody string) error {
	fmt.Fprintf(os.Stderr, "[mail] to=%s subject=%q\n%s\n", to, subject, textBody)
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
