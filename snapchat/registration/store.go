package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

type Store struct {
	db *sql.DB
}

func NewStore(dsn string) (*Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// HashPhone returns a SHA-256 hex digest of the phone number.
func HashPhone(phone string) string {
	h := sha256.Sum256([]byte(phone))
	return fmt.Sprintf("%x", h)
}

// CreateVerification inserts a new verification code and returns the request_id.
func (s *Store) CreateVerification(ctx context.Context, requestID, phone, code, deviceID string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO verification_codes (request_id, phone, code, device_id, expires_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		requestID, phone, code, deviceID, expiresAt,
	)
	return err
}

type VerificationRecord struct {
	RequestID   string
	Phone       string
	Code        string
	Attempts    int
	MaxAttempts int
	ExpiresAt   time.Time
	Verified    bool
}

// GetVerification looks up a verification record by request_id.
func (s *Store) GetVerification(ctx context.Context, requestID string) (*VerificationRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT request_id, phone, code, attempts, max_attempts, expires_at, verified
		 FROM verification_codes WHERE request_id = $1`,
		requestID,
	)
	var v VerificationRecord
	err := row.Scan(&v.RequestID, &v.Phone, &v.Code, &v.Attempts, &v.MaxAttempts, &v.ExpiresAt, &v.Verified)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// IncrementAttempts bumps the attempt counter for a verification.
func (s *Store) IncrementAttempts(ctx context.Context, requestID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE verification_codes SET attempts = attempts + 1 WHERE request_id = $1`,
		requestID,
	)
	return err
}

// MarkVerified marks a verification code as verified.
func (s *Store) MarkVerified(ctx context.Context, requestID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE verification_codes SET verified = true WHERE request_id = $1`,
		requestID,
	)
	return err
}

// CreateUser creates a new user and returns the user ID.
func (s *Store) CreateUser(ctx context.Context, phone string) (string, error) {
	var userID string
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO users (phone, phone_hash) VALUES ($1, $2)
		 ON CONFLICT (phone) DO UPDATE SET updated_at = NOW()
		 RETURNING id`,
		phone, HashPhone(phone),
	).Scan(&userID)
	return userID, err
}

// CountRecentCodes counts how many codes were sent to this phone in the given window.
func (s *Store) CountRecentCodes(ctx context.Context, phone string, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM verification_codes WHERE phone = $1 AND created_at >= $2`,
		phone, since,
	).Scan(&count)
	return count, err
}
