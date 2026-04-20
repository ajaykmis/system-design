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

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", h)
}

// SaveRefreshToken stores a hashed refresh token in the database.
func (s *Store) SaveRefreshToken(ctx context.Context, userID, token string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, hashToken(token), expiresAt,
	)
	return err
}

// ValidateRefreshToken checks if a refresh token exists and is not revoked/expired.
func (s *Store) ValidateRefreshToken(ctx context.Context, userID, token string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM refresh_tokens
			WHERE user_id = $1 AND token_hash = $2 AND revoked = false AND expires_at > NOW()
		)`,
		userID, hashToken(token),
	).Scan(&exists)
	return exists, err
}

// RevokeRefreshToken marks a refresh token as revoked.
func (s *Store) RevokeRefreshToken(ctx context.Context, userID, token string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked = true WHERE user_id = $1 AND token_hash = $2`,
		userID, hashToken(token),
	)
	return err
}

// RevokeAllUserTokens revokes all refresh tokens for a user (logout everywhere).
func (s *Store) RevokeAllUserTokens(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked = true WHERE user_id = $1`,
		userID,
	)
	return err
}

// UserExists checks if a user ID is valid.
func (s *Store) UserExists(ctx context.Context, userID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)`,
		userID,
	).Scan(&exists)
	return exists, err
}
