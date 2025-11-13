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
			name VARCHAR NOT NULL,
			applied_at BIGINT NOT NULL
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
				err := tx.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_name='messages'`).Scan(&count)
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
		{
			Version: 2,
			Name:    "add_thread_priority_fields",
			Up: func(tx *sql.Tx) error {
				// Check if priority_score column exists
				var count int
				err := tx.QueryRow(`
					SELECT COUNT(*)
					FROM information_schema.columns
					WHERE table_name='threads' AND column_name='priority_score'
				`).Scan(&count)
				if err != nil {
					return fmt.Errorf("failed to check priority_score column: %w", err)
				}

				// Add priority_score column if it doesn't exist
				if count == 0 {
					_, err = tx.Exec(`
						ALTER TABLE threads ADD COLUMN priority_score DOUBLE DEFAULT 0;
					`)
					if err != nil {
						return fmt.Errorf("failed to add priority_score column: %w", err)
					}
				}

				// Check if relevant_to_user column exists
				err = tx.QueryRow(`
					SELECT COUNT(*)
					FROM information_schema.columns
					WHERE table_name='threads' AND column_name='relevant_to_user'
				`).Scan(&count)
				if err != nil {
					return fmt.Errorf("failed to check relevant_to_user column: %w", err)
				}

				// Add relevant_to_user column if it doesn't exist
				if count == 0 {
					_, err = tx.Exec(`
						ALTER TABLE threads ADD COLUMN relevant_to_user BOOLEAN DEFAULT false;
					`)
					if err != nil {
						return fmt.Errorf("failed to add relevant_to_user column: %w", err)
					}
				}

				// Create index on priority_score for sorting
				_, err = tx.Exec(`
					CREATE INDEX IF NOT EXISTS idx_threads_priority ON threads(priority_score DESC);
				`)
				if err != nil {
					return fmt.Errorf("failed to create priority index: %w", err)
				}

				return nil
			},
			Down: func(tx *sql.Tx) error {
				// Note: DuckDB supports DROP COLUMN
				_, err := tx.Exec(`DROP INDEX IF EXISTS idx_threads_priority`)
				return err
			},
		},
		{
			Version: 3,
			Name:    "add_matched_priorities_to_tasks",
			Up: func(tx *sql.Tx) error {
				// Check if matched_priorities column exists
				var count int
				err := tx.QueryRow(`
					SELECT COUNT(*)
					FROM information_schema.columns
					WHERE table_name='tasks' AND column_name='matched_priorities'
				`).Scan(&count)
				if err != nil {
					return fmt.Errorf("failed to check matched_priorities column: %w", err)
				}

				// Add matched_priorities column if it doesn't exist
				if count == 0 {
					_, err = tx.Exec(`
						ALTER TABLE tasks ADD COLUMN matched_priorities VARCHAR DEFAULT NULL;
					`)
					if err != nil {
						return fmt.Errorf("failed to add matched_priorities column: %w", err)
					}
				}

				return nil
			},
			Down: func(tx *sql.Tx) error {
				// Note: DuckDB supports DROP COLUMN
				// For compatibility, we'll leave the column
				return nil
			},
		},
		{
			Version: 5,
			Name:    "create_priorities_table",
			Up: func(tx *sql.Tx) error {
				// Check if priorities table exists
				var count int
				err := tx.QueryRow(`
					SELECT COUNT(*)
					FROM information_schema.tables
					WHERE table_name='priorities'
				`).Scan(&count)
				if err != nil {
					return fmt.Errorf("failed to check priorities table: %w", err)
				}

				// Create priorities table if it doesn't exist
				if count == 0 {
					_, err = tx.Exec(`
						CREATE TABLE priorities (
							id VARCHAR PRIMARY KEY,
							type VARCHAR NOT NULL,
							value VARCHAR NOT NULL,
							active BOOLEAN DEFAULT true,
							created_at BIGINT NOT NULL,
							expires_at BIGINT DEFAULT NULL,
							notes VARCHAR DEFAULT NULL
						);
					`)
					if err != nil {
						return fmt.Errorf("failed to create priorities table: %w", err)
					}

					// Create index for active priorities lookup
					_, err = tx.Exec(`
						CREATE INDEX IF NOT EXISTS idx_priorities_active ON priorities(active, type);
					`)
					if err != nil {
						return fmt.Errorf("failed to create priorities index: %w", err)
					}
				}

				return nil
			},
			Down: func(tx *sql.Tx) error {
				_, err := tx.Exec(`DROP INDEX IF EXISTS idx_priorities_active`)
				if err != nil {
					return err
				}
				_, err = tx.Exec(`DROP TABLE IF EXISTS priorities`)
				return err
			},
		},
		{
			Version: 6,
			Name:    "add_front_integration",
			Up: func(tx *sql.Tx) error {
				// Check if front_metadata table exists
				var count int
				err := tx.QueryRow(`
					SELECT COUNT(*)
					FROM information_schema.tables
					WHERE table_name='front_metadata'
				`).Scan(&count)
				if err != nil {
					return fmt.Errorf("failed to check front_metadata table: %w", err)
				}

				// Create front_metadata table if it doesn't exist
				if count == 0 {
					_, err = tx.Exec(`
						CREATE TABLE front_metadata (
							thread_id VARCHAR PRIMARY KEY,
							conversation_id VARCHAR NOT NULL,
							status VARCHAR,
							assignee_id VARCHAR,
							assignee_name VARCHAR,
							tags TEXT,
							last_message_ts BIGINT,
							created_at BIGINT NOT NULL,
							updated_at BIGINT NOT NULL,
							FOREIGN KEY (thread_id) REFERENCES threads(id)
						);
					`)
					if err != nil {
						return fmt.Errorf("failed to create front_metadata table: %w", err)
					}

					// Create indexes
					_, err = tx.Exec(`
						CREATE INDEX IF NOT EXISTS idx_front_thread_id ON front_metadata(thread_id);
					`)
					if err != nil {
						return fmt.Errorf("failed to create front_thread_id index: %w", err)
					}

					_, err = tx.Exec(`
						CREATE INDEX IF NOT EXISTS idx_front_conversation_id ON front_metadata(conversation_id);
					`)
					if err != nil {
						return fmt.Errorf("failed to create front_conversation_id index: %w", err)
					}

					_, err = tx.Exec(`
						CREATE INDEX IF NOT EXISTS idx_front_status ON front_metadata(status);
					`)
					if err != nil {
						return fmt.Errorf("failed to create front_status index: %w", err)
					}

					_, err = tx.Exec(`
						CREATE INDEX IF NOT EXISTS idx_front_updated ON front_metadata(updated_at);
					`)
					if err != nil {
						return fmt.Errorf("failed to create front_updated index: %w", err)
					}
				}

				// Check if front_comments table exists
				err = tx.QueryRow(`
					SELECT COUNT(*)
					FROM information_schema.tables
					WHERE table_name='front_comments'
				`).Scan(&count)
				if err != nil {
					return fmt.Errorf("failed to check front_comments table: %w", err)
				}

				// Create front_comments table if it doesn't exist
				if count == 0 {
					_, err = tx.Exec(`
						CREATE TABLE front_comments (
							id VARCHAR PRIMARY KEY,
							thread_id VARCHAR NOT NULL,
							conversation_id VARCHAR NOT NULL,
							author_name VARCHAR,
							body TEXT NOT NULL,
							created_at BIGINT NOT NULL,
							FOREIGN KEY (thread_id) REFERENCES threads(id)
						);
					`)
					if err != nil {
						return fmt.Errorf("failed to create front_comments table: %w", err)
					}

					// Create indexes
					_, err = tx.Exec(`
						CREATE INDEX IF NOT EXISTS idx_front_comments_thread ON front_comments(thread_id);
					`)
					if err != nil {
						return fmt.Errorf("failed to create front_comments_thread index: %w", err)
					}

					_, err = tx.Exec(`
						CREATE INDEX IF NOT EXISTS idx_front_comments_conversation ON front_comments(conversation_id);
					`)
					if err != nil {
						return fmt.Errorf("failed to create front_comments_conversation index: %w", err)
					}

					_, err = tx.Exec(`
						CREATE INDEX IF NOT EXISTS idx_front_comments_created ON front_comments(created_at);
					`)
					if err != nil {
						return fmt.Errorf("failed to create front_comments_created index: %w", err)
					}
				}

				return nil
			},
			Down: func(tx *sql.Tx) error {
				// Drop indexes
				_, err := tx.Exec(`DROP INDEX IF EXISTS idx_front_comments_created`)
				if err != nil {
					return err
				}
				_, err = tx.Exec(`DROP INDEX IF EXISTS idx_front_comments_conversation`)
				if err != nil {
					return err
				}
				_, err = tx.Exec(`DROP INDEX IF EXISTS idx_front_comments_thread`)
				if err != nil {
					return err
				}
				_, err = tx.Exec(`DROP INDEX IF EXISTS idx_front_updated`)
				if err != nil {
					return err
				}
				_, err = tx.Exec(`DROP INDEX IF EXISTS idx_front_status`)
				if err != nil {
					return err
				}
				_, err = tx.Exec(`DROP INDEX IF EXISTS idx_front_conversation_id`)
				if err != nil {
					return err
				}
				_, err = tx.Exec(`DROP INDEX IF EXISTS idx_front_thread_id`)
				if err != nil {
					return err
				}

				// Drop tables
				_, err = tx.Exec(`DROP TABLE IF EXISTS front_comments`)
				if err != nil {
					return err
				}
				_, err = tx.Exec(`DROP TABLE IF EXISTS front_metadata`)
				return err
			},
		},
		{
			Version: 7,
			Name:    "add_embeddings_and_feedback_tables",
			Up: func(tx *sql.Tx) error {
				// Install and load VSS extension
				_, err := tx.Exec(`INSTALL vss`)
				if err != nil {
					return fmt.Errorf("failed to install VSS extension: %w", err)
				}
				_, err = tx.Exec(`LOAD vss`)
				if err != nil {
					return fmt.Errorf("failed to load VSS extension: %w", err)
				}

				// Enable HNSW experimental persistence
				_, err = tx.Exec(`SET hnsw_enable_experimental_persistence = true`)
				if err != nil {
					return fmt.Errorf("failed to enable HNSW persistence: %w", err)
				}

				// Check if task_embeddings table exists
				var count int
				err = tx.QueryRow(`
					SELECT COUNT(*)
					FROM information_schema.tables
					WHERE table_name='task_embeddings'
				`).Scan(&count)
				if err != nil {
					return fmt.Errorf("failed to check task_embeddings table: %w", err)
				}

				// Create task_embeddings table if it doesn't exist
				if count == 0 {
					_, err = tx.Exec(`
						CREATE TABLE task_embeddings (
							task_id VARCHAR PRIMARY KEY,
							embedding FLOAT[768] NOT NULL,
							embedding_content VARCHAR NOT NULL,
							model VARCHAR NOT NULL,
							generated_at BIGINT NOT NULL,
							FOREIGN KEY (task_id) REFERENCES tasks(id)
						);
					`)
					if err != nil {
						return fmt.Errorf("failed to create task_embeddings table: %w", err)
					}

					// Create HNSW index for vector similarity search
					_, err = tx.Exec(`
						CREATE INDEX IF NOT EXISTS idx_task_embeddings_hnsw
						ON task_embeddings USING HNSW (embedding);
					`)
					if err != nil {
						return fmt.Errorf("failed to create HNSW index: %w", err)
					}
				}

				// Check if priority_feedback table exists
				err = tx.QueryRow(`
					SELECT COUNT(*)
					FROM information_schema.tables
					WHERE table_name='priority_feedback'
				`).Scan(&count)
				if err != nil {
					return fmt.Errorf("failed to check priority_feedback table: %w", err)
				}

				// Create priority_feedback table if it doesn't exist
				if count == 0 {
					_, err = tx.Exec(`
						CREATE TABLE priority_feedback (
							id VARCHAR PRIMARY KEY,
							task_id VARCHAR NOT NULL,
							user_vote INTEGER NOT NULL CHECK (user_vote IN (-1, 1)),
							reason VARCHAR DEFAULT NULL,
							original_score DOUBLE,
							adjusted_score DOUBLE,
							feedback_at BIGINT NOT NULL,
							FOREIGN KEY (task_id) REFERENCES tasks(id)
						);
					`)
					if err != nil {
						return fmt.Errorf("failed to create priority_feedback table: %w", err)
					}

					// Create indexes
					_, err = tx.Exec(`
						CREATE INDEX IF NOT EXISTS idx_priority_feedback_task ON priority_feedback(task_id);
					`)
					if err != nil {
						return fmt.Errorf("failed to create feedback_task index: %w", err)
					}

					_, err = tx.Exec(`
						CREATE INDEX IF NOT EXISTS idx_priority_feedback_vote ON priority_feedback(user_vote);
					`)
					if err != nil {
						return fmt.Errorf("failed to create feedback_vote index: %w", err)
					}

					_, err = tx.Exec(`
						CREATE INDEX IF NOT EXISTS idx_priority_feedback_ts ON priority_feedback(feedback_at);
					`)
					if err != nil {
						return fmt.Errorf("failed to create feedback_ts index: %w", err)
					}
				}

				return nil
			},
			Down: func(tx *sql.Tx) error {
				// Drop indexes
				_, err := tx.Exec(`DROP INDEX IF EXISTS idx_priority_feedback_ts`)
				if err != nil {
					return err
				}
				_, err = tx.Exec(`DROP INDEX IF EXISTS idx_priority_feedback_vote`)
				if err != nil {
					return err
				}
				_, err = tx.Exec(`DROP INDEX IF EXISTS idx_priority_feedback_task`)
				if err != nil {
					return err
				}
				_, err = tx.Exec(`DROP INDEX IF EXISTS idx_task_embeddings_hnsw`)
				if err != nil {
					return err
				}

				// Drop tables
				_, err = tx.Exec(`DROP TABLE IF EXISTS priority_feedback`)
				if err != nil {
					return err
				}
				_, err = tx.Exec(`DROP TABLE IF EXISTS task_embeddings`)
				return err
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