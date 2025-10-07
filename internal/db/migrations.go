package db

import (
	"database/sql"
	"fmt"
	"log"
	"time"
)

// MigrationVersion tracks which migrations have been applied
type MigrationVersion struct {
	Version   int
	Name      string
	AppliedAt time.Time
}

// CreateMigrationTable creates the migration tracking table
func CreateMigrationTable(db *DB) error {
	query := `
		CREATE TABLE IF NOT EXISTS migration_versions (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		)
	`
	_, err := db.Exec(query)
	return err
}

// GetAppliedMigrations returns list of applied migrations
func GetAppliedMigrations(db *DB) (map[int]bool, error) {
	// First ensure migration table exists
	if err := CreateMigrationTable(db); err != nil {
		return nil, err
	}

	query := `SELECT version FROM migration_versions`
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}

	return applied, nil
}

// RecordMigration marks a migration as applied
func RecordMigration(db *DB, version int, name string) error {
	query := `INSERT INTO migration_versions (version, name, applied_at) VALUES (?, ?, ?)`
	_, err := db.Exec(query, version, name, time.Now().Unix())
	return err
}

// Migration represents a database migration
type Migration struct {
	Version int
	Name    string
	Up      func(*sql.Tx) error
	Down    func(*sql.Tx) error
}

// GetMigrations returns all migrations in order
func GetMigrations() []Migration {
	return []Migration{
		{
			Version: 1,
			Name:    "initial_schema",
			Up: func(tx *sql.Tx) error {
				// This migration is handled by the SQL file
				// We just record it as applied if tables exist
				var count int
				err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='messages'`).Scan(&count)
				if err != nil {
					return err
				}
				if count == 0 {
					return fmt.Errorf("initial schema not applied")
				}
				return nil
			},
			Down: func(tx *sql.Tx) error {
				// Drop all tables
				tables := []string{
					"messages_fts", "tasks_fts",
					"messages", "threads", "tasks", "docs", "events",
					"prefs", "usage", "llm_cache", "sync_state",
				}
				for _, table := range tables {
					if _, err := tx.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", table)); err != nil {
						return err
					}
				}
				return nil
			},
		},
		// Add future migrations here
	}
}

// RunStructuredMigrations applies migrations using Go code
func RunStructuredMigrations(db *DB) error {
	// Get applied migrations
	applied, err := GetAppliedMigrations(db)
	if err != nil {
		return fmt.Errorf("failed to get applied migrations: %w", err)
	}

	migrations := GetMigrations()

	for _, migration := range migrations {
		if applied[migration.Version] {
			continue
		}

		log.Printf("Applying migration %d: %s", migration.Version, migration.Name)

		// Run migration in transaction
		err := db.WithTx(func(tx *sql.Tx) error {
			if err := migration.Up(tx); err != nil {
				return fmt.Errorf("migration %d failed: %w", migration.Version, err)
			}

			// Record migration as applied
			query := `INSERT INTO migration_versions (version, name, applied_at) VALUES (?, ?, ?)`
			_, err := tx.Exec(query, migration.Version, migration.Name, time.Now().Unix())
			return err
		})

		if err != nil {
			return fmt.Errorf("failed to apply migration %d: %w", migration.Version, err)
		}

		log.Printf("Migration %d applied successfully", migration.Version)
	}

	return nil
}

// RollbackMigration rolls back a specific migration
func RollbackMigration(db *DB, version int) error {
	migrations := GetMigrations()

	for _, migration := range migrations {
		if migration.Version != version {
			continue
		}

		log.Printf("Rolling back migration %d: %s", migration.Version, migration.Name)

		err := db.WithTx(func(tx *sql.Tx) error {
			if err := migration.Down(tx); err != nil {
				return fmt.Errorf("rollback %d failed: %w", migration.Version, err)
			}

			// Remove migration record
			query := `DELETE FROM migration_versions WHERE version = ?`
			_, err := tx.Exec(query, migration.Version)
			return err
		})

		if err != nil {
			return fmt.Errorf("failed to rollback migration %d: %w", migration.Version, err)
		}

		log.Printf("Migration %d rolled back successfully", migration.Version)
		return nil
	}

	return fmt.Errorf("migration %d not found", version)
}

// GetCurrentVersion returns the highest applied migration version
func GetCurrentVersion(db *DB) (int, error) {
	var version int
	query := `SELECT MAX(version) FROM migration_versions`
	err := db.QueryRow(query).Scan(&version)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return version, err
}