package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// PasswordResetOTP is a pending forgot-password verification.
type PasswordResetOTP struct {
	Email     string
	UserID    string
	CodeHash  string
	Attempts  int
	ExpiresAt time.Time
	CreatedAt time.Time
}

// UpsertPasswordResetOTP stores or replaces a password-reset code.
func (s *UserStore) UpsertPasswordResetOTP(ctx context.Context, email, userID, codeHash string, expiresAt time.Time) error {
	email = strings.ToLower(strings.TrimSpace(email))
	userID = strings.TrimSpace(userID)
	if email == "" || userID == "" || codeHash == "" {
		return errors.New("store: password reset otp fields required")
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO password_reset_otps (email, user_id, code_hash, attempts, expires_at, created_at)
		VALUES ($1, $2::uuid, $3, 0, $4, now())
		ON CONFLICT (email) DO UPDATE SET
			user_id = EXCLUDED.user_id,
			code_hash = EXCLUDED.code_hash,
			attempts = 0,
			expires_at = EXCLUDED.expires_at,
			created_at = now()`,
		email, userID, codeHash, expiresAt)
	if err != nil {
		return fmt.Errorf("store: upsert password reset otp: %w", err)
	}
	return nil
}

// GetPasswordResetOTP loads a pending reset by email.
func (s *UserStore) GetPasswordResetOTP(ctx context.Context, email string) (*PasswordResetOTP, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, errors.New("store: email is required")
	}
	var row PasswordResetOTP
	err := s.db.QueryRow(ctx, `
		SELECT email, user_id::text, code_hash, attempts, expires_at, created_at
		FROM password_reset_otps WHERE email = $1`, email).Scan(
		&row.Email, &row.UserID, &row.CodeHash, &row.Attempts, &row.ExpiresAt, &row.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: password reset otp %q: %w", email, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get password reset otp: %w", err)
	}
	return &row, nil
}

// IncrementPasswordResetOTPAttempts bumps failed attempts; returns new count.
func (s *UserStore) IncrementPasswordResetOTPAttempts(ctx context.Context, email string) (int, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var n int
	err := s.db.QueryRow(ctx, `
		UPDATE password_reset_otps SET attempts = attempts + 1 WHERE email = $1
		RETURNING attempts`, email).Scan(&n)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("store: password reset otp %q: %w", email, ErrNotFound)
		}
		return 0, fmt.Errorf("store: increment password reset otp: %w", err)
	}
	return n, nil
}

// DeletePasswordResetOTP removes a pending reset row.
func (s *UserStore) DeletePasswordResetOTP(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	_, err := s.db.Exec(ctx, `DELETE FROM password_reset_otps WHERE email = $1`, email)
	if err != nil {
		return fmt.Errorf("store: delete password reset otp: %w", err)
	}
	return nil
}
