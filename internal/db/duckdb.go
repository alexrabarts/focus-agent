package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/marcboeker/go-duckdb"
)

type DB struct {
	*sql.DB
}

// Init creates and initializes the DuckDB database
func Init(dbPath string) (*DB, error) {
	// Expand home directory
	if dbPath[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		dbPath = filepath.Join(home, dbPath[2:])
	}

	// Create directory if it doesn't exist
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	// Open database with DuckDB driver
	sqlDB, err := sql.Open("duckdb", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Configure DuckDB settings for performance
	settings := []string{
		"SET memory_limit='1GB'",
		"SET threads TO 4",
		"SET default_order='ASC'",
	}

	for _, setting := range settings {
		if _, err := sqlDB.Exec(setting); err != nil {
			return nil, fmt.Errorf("failed to set DuckDB setting %s: %w", setting, err)
		}
	}

	return &DB{sqlDB}, nil
}

// RunMigrations executes all SQL migration files
func RunMigrations(db *DB) error {
	// Get migration files
	migrationDir := getMigrationDir()
	files, err := filepath.Glob(filepath.Join(migrationDir, "*.sql"))
	if err != nil {
		return fmt.Errorf("failed to find migration files: %w", err)
	}

	// Execute each SQL migration
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("failed to read migration %s: %w", file, err)
		}

		if _, err := db.Exec(string(content)); err != nil {
			return fmt.Errorf("failed to execute migration %s: %w", file, err)
		}
	}

	// Run structured Go migrations
	if err := RunStructuredMigrations(db); err != nil {
		return fmt.Errorf("failed to run structured migrations: %w", err)
	}

	return nil
}

// getMigrationDir finds the migrations directory
func getMigrationDir() string {
	// Try relative to current directory
	if _, err := os.Stat("migrations"); err == nil {
		return "migrations"
	}

	// Try relative to executable
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Join(filepath.Dir(exe), "..", "..", "migrations")
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
	}

	// Default
	return "migrations"
}

// Transaction helper
func (db *DB) WithTx(fn func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}