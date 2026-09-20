package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"central-memory/internal/mail"
	"central-memory/internal/store"

	"github.com/jackc/pgx/v5/pgconn"
)

// registerAuthExtraRoutes wires Phase 1 auth/profile/token endpoints (issue #161).
func (s *Server) registerAuthExtraRoutes() {
	s.Mux.HandleFunc("POST /auth/signup/send-code", s.handleSignupSendCode)
	s.Mux.HandleFunc("POST /auth/signup/resend", s.handleSignupResend)
	s.Mux.HandleFunc("POST /auth/signup/cancel", s.handleSignupCancel)
	s.Mux.HandleFunc("POST /auth/signup/status", s.handleSignupStatus)
	s.Mux.HandleFunc("POST /auth/signup", s.handleSignup)
	s.Mux.HandleFunc("POST /auth/password/forgot", s.handlePasswordForgot)
	s.Mux.HandleFunc("POST /auth/password/reset", s.handlePasswordReset)
	s.Mux.HandleFunc("GET /users/me", s.requireAuth(s.handleUsersMeGet))
	s.Mux.HandleFunc("PUT /users/me", s.requireAuth(s.handleUsersMePut))
	s.Mux.HandleFunc("PUT /users/me/password", s.requireAuth(s.handleUsersMePassword))
	s.Mux.HandleFunc("GET /users/me/usage", s.requireAuth(s.handleUsersMeUsage))
	s.Mux.HandleFunc("GET /auth/tokens", s.requireAuth(s.handleAuthTokensList))
	s.Mux.HandleFunc("POST /auth/tokens", s.requireAuth(s.handleAuthTokensCreate))
	s.Mux.HandleFunc("DELETE /auth/tokens/{tokenId}", s.requireAuth(s.handleAuthTokensRevoke))
}

type signupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Email    string `json:"email"`
	Code     string `json:"code"`
}

const signupOTPTTL = 15 * time.Minute
const signupOTPMaxAttempts = 8

func (s *Server) mailer() mail.Sender {
	if s.Mail != nil {
		return s.Mail
	}
	s.Mail = mail.FromEnv()
	return s.Mail
}

func (s *Server) handleSignupSendCode(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	username := strings.TrimSpace(req.Username)
	password := req.Password
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if username == "" || strings.TrimSpace(password) == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	if email == "" || !looksLikeEmail(email) {
		writeError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	if len(password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if s.login == nil {
		s.login = newRateGate(1, 5)
	}
	if !s.login.allow("signup-otp:" + clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many signup attempts")
		return
	}
	if !s.login.allow("signup-otp-mail:" + email) {
		writeError(w, http.StatusTooManyRequests, "too many codes for this email — try again shortly")
		return
	}
	if s.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}

	if _, err := s.Accounts.GetByEmail(r.Context(), email); err == nil {
		writeError(w, http.StatusConflict, "username or email already taken")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "could not check email")
		return
	}
	if _, err := s.Accounts.GetByUsername(r.Context(), username); err == nil {
		writeError(w, http.StatusConflict, "username or email already taken")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "could not check username")
		return
	}

	hash, err := HashPassword(password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	code, err := mintOTP6()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not mint code")
		return
	}
	codeHash := hashOTPCode(code)
	expires := time.Now().UTC().Add(signupOTPTTL)
	if err := s.Accounts.UpsertSignupOTP(r.Context(), email, username, hash, codeHash, expires); err != nil {
		writeError(w, http.StatusInternalServerError, "could not store verification code")
		return
	}

	body := fmt.Sprintf(
		"Your Nexus verification code is %s\n\nIt expires in %d minutes. If you did not request this, ignore this email.\n",
		code, int(signupOTPTTL.Minutes()),
	)
	mailer := s.mailer()
	localDev := strings.TrimSpace(os.Getenv("CENTRAL_MEMORY_LOCAL_DEV")) == "1"
	if !mailer.Configured() && !localDev {
		writeError(w, http.StatusServiceUnavailable, "email delivery is not configured")
		return
	}
	if err := mailer.Send(email, "Your Nexus verification code", body); err != nil {
		s.Log.Printf("signup otp mail failed for %s: %v", email, err)
		writeError(w, http.StatusBadGateway, "could not send verification email")
		return
	}

	out := map[string]any{
		"ok":         true,
		"email":      email,
		"expires_in": int(signupOTPTTL.Seconds()),
	}
	// Local/dev without a real mailer: surface the code so signup still works.
	if !mailer.Configured() || localDev {
		out["dev_code"] = code
		s.Log.Printf("signup otp for %s: %s (dev/local)", email, code)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSignupResend re-emails a code for an existing pending signup (other device / lost code).
// Only email is required — username/password stay as stored from the original send-code.
func (s *Server) handleSignupResend(w http.ResponseWriter, r *http.Request) {
	var req emailOnlyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !looksLikeEmail(email) {
		writeError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	if s.login == nil {
		s.login = newRateGate(1, 5)
	}
	if !s.login.allow("signup-resend:"+clientIP(r)) || !s.login.allow("signup-otp-mail:"+email) {
		writeError(w, http.StatusTooManyRequests, "too many codes for this email — try again shortly")
		return
	}
	if s.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}

	pending, err := s.Accounts.GetSignupOTP(r.Context(), email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "no pending signup for this email — start signup again")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load verification")
		return
	}
	// Allow refresh even if the prior code expired — pending row still holds password.
	code, err := mintOTP6()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not mint code")
		return
	}
	expires := time.Now().UTC().Add(signupOTPTTL)
	if err := s.Accounts.RefreshSignupOTPCode(r.Context(), email, hashOTPCode(code), expires); err != nil {
		writeError(w, http.StatusInternalServerError, "could not refresh verification code")
		return
	}

	body := fmt.Sprintf(
		"Your Nexus verification code is %s\n\nIt expires in %d minutes. If you did not request this, ignore this email.\n",
		code, int(signupOTPTTL.Minutes()),
	)
	mailer := s.mailer()
	localDev := strings.TrimSpace(os.Getenv("CENTRAL_MEMORY_LOCAL_DEV")) == "1"
	if !mailer.Configured() && !localDev {
		writeError(w, http.StatusServiceUnavailable, "email delivery is not configured")
		return
	}
	if err := mailer.Send(email, "Your Nexus verification code", body); err != nil {
		s.Log.Printf("signup resend mail failed for %s: %v", email, err)
		writeError(w, http.StatusBadGateway, "could not send verification email")
		return
	}

	out := map[string]any{
		"ok":         true,
		"email":      email,
		"username":   pending.Username,
		"expires_in": int(signupOTPTTL.Seconds()),
	}
	if !mailer.Configured() || localDev {
		out["dev_code"] = code
		s.Log.Printf("signup otp resend for %s: %s (dev/local)", email, code)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	username := strings.TrimSpace(req.Username)
	password := req.Password
	email := strings.ToLower(strings.TrimSpace(req.Email))
	code := strings.TrimSpace(req.Code)
	if email == "" || !looksLikeEmail(email) {
		writeError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	if code == "" || len(code) < 4 {
		writeError(w, http.StatusBadRequest, "email verification code is required")
		return
	}
	if s.login == nil {
		s.login = newRateGate(1, 5)
	}
	if !s.login.allow("signup:" + clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many signup attempts")
		return
	}
	if s.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}

	pending, err := s.Accounts.GetSignupOTP(r.Context(), email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "request a verification code first")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load verification")
		return
	}
	if time.Now().UTC().After(pending.ExpiresAt) {
		_ = s.Accounts.DeleteSignupOTP(r.Context(), email)
		writeError(w, http.StatusBadRequest, "verification code expired — request a new one")
		return
	}
	if pending.Attempts >= signupOTPMaxAttempts {
		_ = s.Accounts.DeleteSignupOTP(r.Context(), email)
		writeError(w, http.StatusTooManyRequests, "too many incorrect codes — request a new one")
		return
	}
	if !secureEqualFold(pending.CodeHash, hashOTPCode(code)) {
		n, _ := s.Accounts.IncrementSignupOTPAttempts(r.Context(), email)
		if n >= signupOTPMaxAttempts {
			_ = s.Accounts.DeleteSignupOTP(r.Context(), email)
		}
		writeError(w, http.StatusUnauthorized, "incorrect verification code")
		return
	}
	// Optional client fields: when resuming after abort, email+code alone is enough.
	if username != "" && !strings.EqualFold(pending.Username, username) {
		writeError(w, http.StatusBadRequest, "username does not match the pending verification")
		return
	}
	if strings.TrimSpace(password) != "" {
		if !VerifyPassword(password, pending.PasswordHash) {
			writeError(w, http.StatusBadRequest, "password does not match the pending verification — restart signup or start over")
			return
		}
	}

	u, err := s.Accounts.Create(r.Context(), store.UserParams{
		Username: pending.Username,
		Email:    email,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "username or email already taken")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not create user: "+err.Error())
		return
	}
	if err := s.Accounts.SetPasswordHash(r.Context(), u.ID, pending.PasswordHash); err != nil {
		writeError(w, http.StatusInternalServerError, "could not set password")
		return
	}
	_ = s.Accounts.DeleteSignupOTP(r.Context(), email)

	token, err := s.Auth.GenerateUser(u.ID, u.Username, DefaultTokenTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":    token,
		"username": u.Username,
		"user_id":  u.ID,
	})
}

type emailOnlyRequest struct {
	Email string `json:"email"`
}

func (s *Server) handleSignupCancel(w http.ResponseWriter, r *http.Request) {
	var req emailOnlyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !looksLikeEmail(email) {
		writeError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	if s.login == nil {
		s.login = newRateGate(1, 5)
	}
	if !s.login.allow("signup-cancel:" + clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	if s.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}
	_ = s.Accounts.DeleteSignupOTP(r.Context(), email)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cancelled": true})
}

func (s *Server) handleSignupStatus(w http.ResponseWriter, r *http.Request) {
	var req emailOnlyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !looksLikeEmail(email) {
		writeError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	if s.login == nil {
		s.login = newRateGate(1, 5)
	}
	if !s.login.allow("signup-status:" + clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	if s.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}
	pending, err := s.Accounts.GetSignupOTP(r.Context(), email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{"pending": false})
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load verification")
		return
	}
	secs := int(time.Until(pending.ExpiresAt).Seconds())
	expired := secs <= 0
	if secs < 0 {
		secs = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pending":    true,
		"expired":    expired,
		"email":      pending.Email,
		"username":   pending.Username,
		"expires_in": secs,
	})
}

type passwordForgotRequest struct {
	Email string `json:"email"`
}

type passwordResetRequest struct {
	Email       string `json:"email"`
	Code        string `json:"code"`
	NewPassword string `json:"new_password"`
}

func (s *Server) handlePasswordForgot(w http.ResponseWriter, r *http.Request) {
	var req passwordForgotRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !looksLikeEmail(email) {
		writeError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	if s.login == nil {
		s.login = newRateGate(1, 5)
	}
	if !s.login.allow("pw-forgot:"+clientIP(r)) || !s.login.allow("pw-forgot-mail:"+email) {
		writeError(w, http.StatusTooManyRequests, "too many reset attempts — try again shortly")
		return
	}
	if s.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}

	// Always return the same shape to avoid email enumeration.
	okOut := map[string]any{
		"ok":         true,
		"email":      email,
		"expires_in": int(signupOTPTTL.Seconds()),
	}

	u, err := s.Accounts.GetByEmail(r.Context(), email)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "could not look up account")
			return
		}
		writeJSON(w, http.StatusOK, okOut)
		return
	}

	code, err := mintOTP6()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not mint code")
		return
	}
	expires := time.Now().UTC().Add(signupOTPTTL)
	if err := s.Accounts.UpsertPasswordResetOTP(r.Context(), email, u.ID, hashOTPCode(code), expires); err != nil {
		writeError(w, http.StatusInternalServerError, "could not store reset code")
		return
	}

	body := fmt.Sprintf(
		"Your Nexus password reset code is %s\n\nIt expires in %d minutes. If you did not request this, ignore this email.\n",
		code, int(signupOTPTTL.Minutes()),
	)
	mailer := s.mailer()
	localDev := strings.TrimSpace(os.Getenv("CENTRAL_MEMORY_LOCAL_DEV")) == "1"
	if !mailer.Configured() && !localDev {
		writeError(w, http.StatusServiceUnavailable, "email delivery is not configured")
		return
	}
	if err := mailer.Send(email, "Reset your Nexus password", body); err != nil {
		s.Log.Printf("password reset mail failed for %s: %v", email, err)
		writeError(w, http.StatusBadGateway, "could not send reset email")
		return
	}
	if !mailer.Configured() || localDev {
		okOut["dev_code"] = code
		s.Log.Printf("password reset otp for %s: %s (dev/local)", email, code)
	}
	writeJSON(w, http.StatusOK, okOut)
}

func (s *Server) handlePasswordReset(w http.ResponseWriter, r *http.Request) {
	var req passwordResetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	code := strings.TrimSpace(req.Code)
	newPass := req.NewPassword
	if email == "" || !looksLikeEmail(email) {
		writeError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	if code == "" || len(code) < 4 {
		writeError(w, http.StatusBadRequest, "reset code is required")
		return
	}
	if len(newPass) < 8 {
		writeError(w, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}
	if s.login == nil {
		s.login = newRateGate(1, 5)
	}
	if !s.login.allow("pw-reset:" + clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many reset attempts")
		return
	}
	if s.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}

	pending, err := s.Accounts.GetPasswordResetOTP(r.Context(), email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "request a reset code first")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load reset code")
		return
	}
	if time.Now().UTC().After(pending.ExpiresAt) {
		_ = s.Accounts.DeletePasswordResetOTP(r.Context(), email)
		writeError(w, http.StatusBadRequest, "reset code expired — request a new one")
		return
	}
	if pending.Attempts >= signupOTPMaxAttempts {
		_ = s.Accounts.DeletePasswordResetOTP(r.Context(), email)
		writeError(w, http.StatusTooManyRequests, "too many incorrect codes — request a new one")
		return
	}
	if !secureEqualFold(pending.CodeHash, hashOTPCode(code)) {
		n, _ := s.Accounts.IncrementPasswordResetOTPAttempts(r.Context(), email)
		if n >= signupOTPMaxAttempts {
			_ = s.Accounts.DeletePasswordResetOTP(r.Context(), email)
		}
		writeError(w, http.StatusUnauthorized, "incorrect reset code")
		return
	}

	hash, err := HashPassword(newPass)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	if err := s.Accounts.SetPasswordHash(r.Context(), pending.UserID, hash); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update password")
		return
	}
	_ = s.Accounts.DeletePasswordResetOTP(r.Context(), email)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleUsersMeGet(w http.ResponseWriter, r *http.Request) {
	if s.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}
	u, err := s.Accounts.GetByID(r.Context(), authSubject(r))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load user")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":                u.ID,
		"username":          u.Username,
		"email":             u.Email,
		"settings":          u.Settings,
		"created_at":        u.CreatedAt,
		"is_platform_admin": s.userIsPlatformAdmin(r.Context(), u.ID, u.Username),
	})
}

type usersMePutRequest struct {
	Email    *string         `json:"email"`
	Settings json.RawMessage `json:"settings"`
}

func (s *Server) handleUsersMePut(w http.ResponseWriter, r *http.Request) {
	if s.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}
	var req usersMePutRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var settingsPtr *string
	if req.Settings != nil {
		raw := strings.TrimSpace(string(req.Settings))
		if raw != "" && raw != "null" {
			settingsPtr = &raw
		}
	}
	u, err := s.Accounts.UpdateProfile(r.Context(), authSubject(r), req.Email, settingsPtr)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "email already taken")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not update user")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

type passwordChangeRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *Server) handleUsersMePassword(w http.ResponseWriter, r *http.Request) {
	if s.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}
	var req passwordChangeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.CurrentPassword) == "" || strings.TrimSpace(req.NewPassword) == "" {
		writeError(w, http.StatusBadRequest, "current_password and new_password are required")
		return
	}
	if len(req.NewPassword) < 8 {
		writeError(w, http.StatusBadRequest, "new_password must be at least 8 characters")
		return
	}
	hash, err := s.Accounts.GetPasswordHashByID(r.Context(), authSubject(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load password")
		return
	}
	if hash == "" || !VerifyPassword(req.CurrentPassword, hash) {
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	next, err := HashPassword(req.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	if err := s.Accounts.SetPasswordHash(r.Context(), authSubject(r), next); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update password")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleUsersMeUsage(w http.ResponseWriter, r *http.Request) {
	if s.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "user authentication not configured")
		return
	}
	stats, err := s.Accounts.GetUsage(r.Context(), authSubject(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load usage")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleAuthTokensList(w http.ResponseWriter, r *http.Request) {
	if s.Tokens == nil {
		writeError(w, http.StatusServiceUnavailable, "api tokens not configured")
		return
	}
	items, err := s.Tokens.ListByUser(r.Context(), authSubject(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list tokens")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

type createTokenRequest struct {
	Name    string   `json:"name"`
	Scopes  []string `json:"scopes"`
	AgentID string   `json:"agent_id"`
}

func (s *Server) handleAuthTokensCreate(w http.ResponseWriter, r *http.Request) {
	if s.Tokens == nil {
		writeError(w, http.StatusServiceUnavailable, "api tokens not configured")
		return
	}
	var req createTokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	agentID := strings.TrimSpace(req.AgentID)
	raw, prefix, hash, err := mintAPITokenSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not mint token")
		return
	}
	scopes := req.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	tok, err := s.Tokens.Create(r.Context(), authSubject(r), name, prefix, hash, scopes, agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create token")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      raw, // shown once
		"id":         tok.ID,
		"name":       tok.Name,
		"prefix":     tok.Prefix,
		"scopes":     tok.Scopes,
		"agent_id":   tok.AgentID,
		"created_at": tok.CreatedAt.Format(time.RFC3339Nano),
	})
}

func (s *Server) handleAuthTokensRevoke(w http.ResponseWriter, r *http.Request) {
	if s.Tokens == nil {
		writeError(w, http.StatusServiceUnavailable, "api tokens not configured")
		return
	}
	tokenID := strings.TrimSpace(r.PathValue("tokenId"))
	if tokenID == "" {
		writeError(w, http.StatusBadRequest, "tokenId is required")
		return
	}
	if err := s.Tokens.Revoke(r.Context(), authSubject(r), tokenID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "token not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not revoke token")
		return
	}
	if s.hub != nil {
		s.hub.DropByToken(tokenID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": tokenID, "revoked": true})
}

func mintAPITokenSecret() (raw, prefix, hash string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", err
	}
	raw = "nxs_" + base64.RawURLEncoding.EncodeToString(buf)
	prefix = raw
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	sum := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(sum[:])
	return raw, prefix, hash, nil
}

func hashAPITokenSecret(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func looksLikeEmail(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || !strings.Contains(value, "@") {
		return false
	}
	at := strings.LastIndex(value, "@")
	if at <= 0 || at == len(value)-1 {
		return false
	}
	domain := value[at+1:]
	return strings.Contains(domain, ".")
}

func mintOTP6() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	n := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	if n < 0 {
		n = -n
	}
	return fmt.Sprintf("%06d", n%1000000), nil
}

func hashOTPCode(code string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(code)))
	return hex.EncodeToString(sum[:])
}

func secureEqualFold(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
