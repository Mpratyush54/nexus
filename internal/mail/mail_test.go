package mail

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a tiny SMTP server on loopback. mode: "ok" accepts the message,
// "hang" accepts the TCP connection and never says anything, "reject"
// refuses the recipient.
type fakeSMTP struct {
	ln   net.Listener
	mode string
	mu   sync.Mutex
	data strings.Builder
}

func startFakeSMTP(t *testing.T, mode string) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, mode: mode}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) port() string {
	_, p, _ := net.SplitHostPort(f.ln.Addr().String())
	return p
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	if f.mode == "hang" {
		// Hold the connection open without a greeting, like a black-holed relay.
		time.Sleep(5 * time.Second)
		return
	}
	r := bufio.NewReader(c)
	say := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
	say("220 fake ESMTP")
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if inData {
			if line == ".\r\n" {
				inData = false
				say("250 queued")
				continue
			}
			f.mu.Lock()
			f.data.WriteString(line)
			f.mu.Unlock()
			continue
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			say("250-fake")
			say("250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH"):
			say("235 ok")
		case strings.HasPrefix(cmd, "MAIL FROM"):
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO"):
			if f.mode == "reject" {
				say("550 no such user")
			} else {
				say("250 ok")
			}
		case cmd == "DATA":
			inData = true
			say("354 go ahead")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func (f *fakeSMTP) message() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data.String()
}

func TestSMTPSendSucceeds(t *testing.T) {
	f := startFakeSMTP(t, "ok")
	s := &SMTP{Host: "127.0.0.1", Port: f.port(), User: "u", Password: "p", From: "Nexus <noreply@example.com>"}
	if err := s.Send("maya@example.com", "Your code", "Your code is 123456\n.\nend"); err != nil {
		t.Fatalf("send: %v", err)
	}
	msg := f.message()
	for _, want := range []string{"To: maya@example.com\r\n", "Subject: Your code\r\n", "Your code is 123456\r\n", "..\r\n"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q:\n%s", want, msg)
		}
	}
}

func TestSMTPSendFailsFastWhenRelayHangs(t *testing.T) {
	f := startFakeSMTP(t, "hang")
	s := &SMTP{Host: "127.0.0.1", Port: f.port(), Password: "p", From: "noreply@example.com", Timeout: 300 * time.Millisecond}
	start := time.Now()
	err := s.Send("maya@example.com", "Your code", "123456")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("send took %v; the deadline did not apply", took)
	}
}

func TestSMTPSendFailsFastWhenUnreachable(t *testing.T) {
	// A closed port refuses at once; the error must come back, not hang.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	s := &SMTP{Host: "127.0.0.1", Port: port, Password: "p", From: "noreply@example.com", DialTimeout: 500 * time.Millisecond}
	if err := s.Send("maya@example.com", "x", "y"); err == nil || !strings.Contains(err.Error(), "connect") {
		t.Fatalf("err = %v", err)
	}
}

func TestSMTPSendReportsRejection(t *testing.T) {
	f := startFakeSMTP(t, "reject")
	s := &SMTP{Host: "127.0.0.1", Port: f.port(), Password: "p", From: "noreply@example.com"}
	if err := s.Send("nobody@example.com", "x", "y"); err == nil || !strings.Contains(err.Error(), "RCPT") {
		t.Fatalf("err = %v", err)
	}
}

func TestSMTPRejectsHeaderInjection(t *testing.T) {
	s := &SMTP{Host: "127.0.0.1", Port: "1", Password: "p"}
	if err := s.Send("a@example.com\r\nBcc: x@example.com", "x", "y"); err == nil {
		t.Fatal("expected an error for a line break in To")
	}
}

func TestDescribeNeverPrintsSecrets(t *testing.T) {
	got := Describe(&SMTP{Host: "smtp.example.com", Port: "587", User: "user@example.com", Password: "s3cret"})
	if got != "mail: smtp host=smtp.example.com port=587 auth=yes configured" {
		t.Fatalf("describe = %q", got)
	}
	if strings.Contains(Describe(&SMTP{Host: "h", Port: "587"}), "s3cret") {
		t.Fatal("leak")
	}
	if !strings.Contains(Describe(&SMTP{Host: "h", Port: "587"}), "NOT configured") {
		t.Fatal("missing not-configured state")
	}
	if got := Describe(&SMTP{Host: "h", Port: "587", Password: "x"}); !strings.Contains(got, "auth=no") || !strings.Contains(got, "WITHOUT a username") {
		t.Fatalf("empty user not flagged: %q", got)
	}
	if !strings.Contains(Describe(&LogSender{}), "NOT configured") {
		t.Fatal("log sender should read as not configured")
	}
}
