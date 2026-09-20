package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SignupOTP is a pending email-verified signup.
type SignupOTP struct {
	Email        string
	Username     string
	PasswordHash string
	CodeHash     string
	Attempts     int
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

// UpsertSignupOTP stores or replaces a pending signup verification.
func (s *UserStore) UpsertSignupOTP(ctx context.Context, email, username, passwordHash, codeHash string, expiresAt time.Time) error {
	email = strings.ToLower(strings.TrimSpace(email))
	username = strings.TrimSpace(username)
	if email == "" || username == "" || passwordHash == "" || codeHash == "" {
		return errors.New("store: signup otp fields required")
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO signup_otps (email, username, password_hash, code_hash, attempts, expires_at, created_at)
		VALUES ($1, $2, $3, $4, 0, $5, now())
		ON CONFLICT (email) DO UPDATE SET
			username = EXCLUDED.username,
			password_hash = EXCLUDED.password_hash,
			code_hash = EXCLUDED.code_hash,
			attempts = 0,
			expires_at = EXCLUDED.expires_at,
			created_at = now()`,
		email, username, passwordHash, codeHash, expiresAt)
	if err != nil {
		return fmt.Errorf("store: upsert signup otp: %w", err)
	}
	return nil
}

// GetSignupOTP loads a pending signup by email.
func (s *UserStore) GetSignupOTP(ctx context.Context, email string) (*SignupOTP, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, errors.New("store: email is required")
	}
	var row SignupOTP
	err := s.db.QueryRow(ctx, `
		SELECT email, username, password_hash, code_hash, attempts, expires_at, created_at
		FROM signup_otps WHERE email = $1`, email).Scan(
		&row.Email, &row.Username, &row.PasswordHash, &row.CodeHash, &row.Attempts, &row.ExpiresAt, &row.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: signup otp %q: %w", email, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get signup otp: %w", err)
	}
	return &row, nil
}

// IncrementSignupOTPAttempts bumps failed verify attempts; returns new count.
func (s *UserStore) IncrementSignupOTPAttempts(ctx context.Context, email string) (int, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var n int
	err := s.db.QueryRow(ctx, `
		UPDATE signup_otps SET attempts = attempts + 1 WHERE email = $1
		RETURNING attempts`, email).Scan(&n)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("store: signup otp %q: %w", email, ErrNotFound)
		}
		return 0, fmt.Errorf("store: increment signup otp: %w", err)
	}
	return n, nil
}

// DeleteSignupOTP removes a pending signup row.
func (s *UserStore) DeleteSignupOTP(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	_, err := s.db.Exec(ctx, `DELETE FROM signup_otps WHERE email = $1`, email)
	if err != nil {
		return fmt.Errorf("store: delete signup otp: %w", err)
	}
	return nil
}

// RefreshSignupOTPCode rotates the verification code for an existing pending signup
// (same username/password) — used when continuing from another device.
func (s *UserStore) RefreshSignupOTPCode(ctx context.Context, email, codeHash string, expiresAt time.Time) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || codeHash == "" {
		return errors.New("store: signup otp refresh fields required")
	}
	tag, err := s.db.Exec(ctx, `
		UPDATE signup_otps
		SET code_hash = $2, attempts = 0, expires_at = $3
		WHERE email = $1`, email, codeHash, expiresAt)
	if err != nil {
		return fmt.Errorf("store: refresh signup otp: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: signup otp %q: %w", email, ErrNotFound)
	}
	return nil
}
