package server

import (
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Sign-up and password-reset codes are emailed in the background: the HTTP
// request answers as soon as the code is stored, so a slow or unreachable
// mail relay can never hold a request open until a proxy in front of the API
// gives up (which reached the browser as a CORS-less 502/524, read as
// "Can't reach Nexus").

// Mail delivery states reported by /auth/signup/status.
const (
	mailSending = "sending"
	mailSent    = "sent"
	mailFailed  = "failed"
)

type mailDelivery struct {
	Status   string
	Attempts int
	Queued   time.Time
	Updated  time.Time
}

type mailOutbox struct {
	mu      sync.Mutex
	state   map[string]mailDelivery
	gen     map[string]int
	wg      sync.WaitGroup
	backoff []time.Duration
}

// defaultMailBackoff waits before attempts 2 and 3. Each attempt is itself
// bounded by the sender's timeout (SMTP 20s, Resend 15s).
var defaultMailBackoff = []time.Duration{3 * time.Second, 10 * time.Second}

const mailStateTTL = time.Hour

// mailResendGap is the least time between two code emails to one address,
// so the endpoints cannot be used to flood someone's inbox.
var mailResendGap = 15 * time.Second

// mailQueuedWithin reports whether a code email for key was queued less than
// gap ago.
func (s *Server) mailQueuedWithin(key string, gap time.Duration) bool {
	d, ok := s.mailState(key)
	return ok && time.Since(d.Queued) < gap
}

func (s *Server) outbox() *mailOutbox {
	s.mailOnce.Do(func() {
		if s.mailbox == nil {
			s.mailbox = &mailOutbox{state: map[string]mailDelivery{}, gen: map[string]int{}, backoff: defaultMailBackoff}
		}
	})
	return s.mailbox
}

// queueMail sends in the background with up to len(backoff)+1 attempts and
// records the outcome under key. A newer queueMail for the same key
// supersedes the older one's status.
func (s *Server) queueMail(key, to, subject, body string) {
	o := s.outbox()
	mailer := s.mailer()
	o.mu.Lock()
	o.gen[key]++
	gen := o.gen[key]
	now := time.Now()
	o.state[key] = mailDelivery{Status: mailSending, Queued: now, Updated: now}
	for k, v := range o.state {
		if time.Since(v.Updated) > mailStateTTL {
			delete(o.state, k)
			delete(o.gen, k)
		}
	}
	backoff := o.backoff
	o.mu.Unlock()

	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		attempts := len(backoff) + 1
		for i := 0; i < attempts; i++ {
			if i > 0 {
				time.Sleep(backoff[i-1])
				if !o.current(key, gen) {
					return // a newer code replaced this one
				}
			}
			err := mailer.Send(to, subject, body)
			if err == nil {
				o.set(key, gen, mailSent, i+1)
				if i > 0 {
					s.Log.Printf("mail: %s delivered on attempt %d/%d", key, i+1, attempts)
				}
				return
			}
			s.Log.Printf("mail: %s attempt %d/%d failed: %v", key, i+1, attempts, err)
			if i < attempts-1 {
				o.set(key, gen, mailSending, i+1)
			}
		}
		o.set(key, gen, mailFailed, attempts)
		s.Log.Printf("mail: %s FAILED after %d attempts; check SMTP settings (startup log line \"mail: ...\")", key, attempts)
	}()
}

func (o *mailOutbox) current(key string, gen int) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.gen[key] == gen
}

func (o *mailOutbox) set(key string, gen int, status string, attempts int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.gen[key] != gen {
		return
	}
	d := o.state[key]
	d.Status, d.Attempts, d.Updated = status, attempts, time.Now()
	o.state[key] = d
}

func (s *Server) mailState(key string) (mailDelivery, bool) {
	o := s.outbox()
	o.mu.Lock()
	defer o.mu.Unlock()
	d, ok := o.state[key]
	return d, ok
}

// waitMail blocks until queued sends finish (tests).
func (s *Server) waitMail() { s.outbox().wg.Wait() }

// devMailCodes reports whether a code may be returned in the HTTP response
// because no mailer is configured. Only for a local-dev boot whose public
// app URL (if any) is on this machine: CENTRAL_MEMORY_LOCAL_DEV=1 is also set
// on the production box (its database runs without TLS), and there a missing
// mailer must not turn into sign-up without email verification.
func devMailCodes() bool {
	if strings.TrimSpace(os.Getenv("CENTRAL_MEMORY_LOCAL_DEV")) != "1" {
		return false
	}
	app := strings.TrimSpace(os.Getenv("PUBLIC_APP_URL"))
	if app == "" {
		return true
	}
	u, err := url.Parse(app)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// mailUnavailable is the 503 text when no mailer is configured. It keeps the
// phrase "email delivery is not configured" that clients match on.
const mailUnavailable = "email delivery is not configured on this server"

// signupMailKey and resetMailKey name delivery records per address.
func signupMailKey(email string) string { return "signup:" + email }
func resetMailKey(email string) string  { return "reset:" + email }
