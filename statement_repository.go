package main

import (
	"database/sql"
	"fmt"
)

// StatementRepository defines the interface for statement notification tracking
type StatementRepository interface {
	IsNotified(key string) (bool, error)
	MarkMultipleAsNotified(keys []string) error
}

// PostgresStatementRepository implements StatementRepository using PostgreSQL storage
type PostgresStatementRepository struct {
	db *sql.DB
}

// NewPostgresStatementRepository creates a new PostgreSQL-based statement repository
func NewPostgresStatementRepository(db *sql.DB) (*PostgresStatementRepository, error) {
	repo := &PostgresStatementRepository{db: db}
	// Migrations are handled by runMigrations in cookie_repository.go
	// No need to run them again here since we share the same database
	return repo, nil
}

// IsNotified checks if a statement has already been notified
func (r *PostgresStatementRepository) IsNotified(key string) (bool, error) {
	var notified bool
	query := `SELECT notified FROM statements WHERE statement_key = $1`
	err := r.db.QueryRow(query, key).Scan(&notified)

	if err != nil {
		if err == sql.ErrNoRows {
			// Statement not found, means it hasn't been notified
			return false, nil
		}
		return false, fmt.Errorf("failed to check if statement is notified: %w", err)
	}

	return notified, nil
}

// MarkMultipleAsNotified marks multiple statements as notified
func (r *PostgresStatementRepository) MarkMultipleAsNotified(keys []string) error {
	if len(keys) == 0 {
		return nil
	}

	// Use INSERT ... ON CONFLICT to upsert
	query := `
		INSERT INTO statements (statement_key, notified, updated_at)
		VALUES ($1, true, CURRENT_TIMESTAMP)
		ON CONFLICT (statement_key) 
		DO UPDATE SET notified = true, updated_at = CURRENT_TIMESTAMP
	`

	for _, key := range keys {
		_, err := r.db.Exec(query, key)
		if err != nil {
			return fmt.Errorf("failed to mark statement as notified: %w", err)
		}
	}

	return nil
}

// generateStatementKey creates a unique key from date, partner, and amount
func generateStatementKey(date, partner, amount string) string {
	return fmt.Sprintf("%s|%s|%s", date, partner, amount)
}

