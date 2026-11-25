// Package mail sends transactional email (signup OTP) via Resend or SMTP.
package mail

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	netmail "net/mail"
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

// SMTP sends via net/smtp: implicit TLS on port 465, otherwise STARTTLS
// when the server offers it (always before AUTH).
//
// Every step is bounded: the dial by DialTimeout and the whole exchange by
// Timeout, so an unreachable or stalled relay fails in seconds instead of
// holding the caller until a proxy gives up.
type SMTP struct {
	Host, Port, User, Password, From string
	// DialTimeout bounds the TCP connect (default 10s).
	DialTimeout time.Duration
	// Timeout bounds the whole conversation, dial included (default 20s).
	Timeout time.Duration
	// TLSConfig overrides the TLS settings (tests).
	TLSConfig *tls.Config
}

const (
	defaultSMTPDialTimeout = 10 * time.Second
	defaultSMTPTimeout     = 20 * time.Second
)

func (s *SMTP) Configured() bool {
	return strings.TrimSpace(s.Host) != "" && strings.TrimSpace(s.Password) != ""
}

func (s *SMTP) Send(to, subject, textBody string) error {
	if strings.ContainsAny(to, "\r\n") || strings.ContainsAny(subject, "\r\n") {
		return errors.New("smtp: header value contains a line break")
	}
	dialTimeout := s.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = defaultSMTPDialTimeout
	}
	total := s.Timeout
	if total <= 0 {
		total = defaultSMTPTimeout
	}
	deadline := time.Now().Add(total)
	if d := time.Until(deadline); d < dialTimeout {
		dialTimeout = d
	}
	addr := net.JoinHostPort(s.Host, s.Port)
	tlsCfg := s.TLSConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	}

	dialer := &net.Dialer{Timeout: dialTimeout}
	var conn net.Conn
	var err error
	if s.Port == "465" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp: connect %s: %w", addr, err)
	}
	// One deadline for the whole exchange: greeting, STARTTLS, AUTH, DATA.
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return fmt.Errorf("smtp: set deadline: %w", err)
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp: greeting from %s: %w", addr, err)
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok && s.Port != "465" {
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtp: starttls: %w", err)
		}
	}
	if s.User != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("smtp: server does not offer AUTH")
		}
		if err := c.Auth(smtp.PlainAuth("", s.User, s.Password, s.Host)); err != nil {
			return fmt.Errorf("smtp: auth: %w", err)
		}
	}

	fromAddr := strings.TrimSpace(s.From)
	if parsed, err := netmail.ParseAddress(fromAddr); err == nil && parsed.Address != "" {
		fromAddr = parsed.Address
	}
	if err := c.Mail(fromAddr); err != nil {
		return fmt.Errorf("smtp: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: DATA: %w", err)
	}
	// The DATA writer turns each "\n" into CRLF and dot-stuffs the body.
	msg := "From: " + s.From + "\n" +
		"To: " + to + "\n" +
		"Subject: " + subject + "\n" +
		"Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\n" +
		"MIME-Version: 1.0\n" +
		"Content-Type: text/plain; charset=UTF-8\n" +
		"\n" + textBody + "\n"
	if _, err := io.WriteString(w, msg); err != nil {
		w.Close()
		return fmt.Errorf("smtp: write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: end of message: %w", err)
	}
	return c.Quit()
}

// Describe says which sender is in use, for a startup log line. It never
// includes credentials.
func Describe(s Sender) string {
	switch m := s.(type) {
	case *Resend:
		if m.Configured() {
			return "mail: resend API configured"
		}
		return "mail: resend API not configured (no API key)"
	case *SMTP:
		state := "configured"
		if !m.Configured() {
			state = "NOT configured (SMTP_PASSWORD is empty)"
		} else if strings.TrimSpace(m.User) == "" {
			state = "configured WITHOUT a username (SMTP_USER is empty; most relays will refuse)"
		}
		auth := "no"
		if strings.TrimSpace(m.User) != "" {
			auth = "yes"
		}
		return fmt.Sprintf("mail: smtp host=%s port=%s auth=%s %s", m.Host, m.Port, auth, state)
	default:
		return "mail: NOT configured (no RESEND_API_KEY or SMTP_HOST): sign-up and password-reset codes cannot be emailed"
	}
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
