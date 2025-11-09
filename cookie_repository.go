package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// CookieRepository defines the interface for cookie storage operations
type CookieRepository interface {
	Get() (string, error)
	Save(cookie string) error
}

// PostgresCookieRepository implements CookieRepository using PostgreSQL storage
type PostgresCookieRepository struct {
	db *sql.DB
}

// GetDB returns the underlying database connection (for sharing with other repositories)
func (r *PostgresCookieRepository) GetDB() *sql.DB {
	return r.db
}

// NewPostgresCookieRepository creates a new PostgreSQL-based cookie repository
func NewPostgresCookieRepository(connString string) (*PostgresCookieRepository, error) {
	// Parse connection string and register driver
	config, err := pgx.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection string: %w", err)
	}

	// Use stdlib to register pgx driver
	db := stdlib.OpenDB(*config)

	repo := &PostgresCookieRepository{db: db}

	// Run migrations
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return repo, nil
}

// Get retrieves the most recent cookie from PostgreSQL
func (r *PostgresCookieRepository) Get() (string, error) {
	var cookieValue string
	var updatedAt time.Time

	query := `SELECT cookie_value, updated_at FROM cookies ORDER BY updated_at DESC LIMIT 1`
	err := r.db.QueryRow(query).Scan(&cookieValue, &updatedAt)

	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("no cookie found in database")
		}
		return "", fmt.Errorf("failed to get cookie: %w", err)
	}

	// Format timestamp and prepend to cookie value
	timestamp := updatedAt.UTC().Format(time.RFC3339)
	cookieWithTimestamp := fmt.Sprintf("TIMESTAMP=%s; %s", timestamp, cookieValue)

	fmt.Printf("Cookie retrieved from PostgreSQL (updated at: %s)\n", timestamp)
	return cookieWithTimestamp, nil
}

// Save stores the cookie to PostgreSQL with a timestamp
func (r *PostgresCookieRepository) Save(cookie string) error {
	// Trim the cookie before saving
	cookie = strings.TrimSpace(cookie)
	cookie = strings.Trim(cookie, " \t\n\r\u0000\u200b\ufeff")

	// Remove any existing TIMESTAMP prefix if present
	if strings.HasPrefix(cookie, "TIMESTAMP=") {
		parts := strings.SplitN(cookie, "; ", 2)
		if len(parts) == 2 {
			cookie = parts[1]
		}
	}

	// Insert or update the cookie (we'll always insert a new row to keep history)
	query := `INSERT INTO cookies (cookie_value, updated_at) VALUES ($1, CURRENT_TIMESTAMP)`
	_, err := r.db.Exec(query, cookie)
	if err != nil {
		return fmt.Errorf("failed to save cookie: %w", err)
	}

	fmt.Println("Cookie saved to PostgreSQL")
	return nil
}

// Close closes the database connection
func (r *PostgresCookieRepository) Close() error {
	return r.db.Close()
}

