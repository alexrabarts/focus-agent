package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

// MigrateSQLiteToDuckDB migrates data from SQLite to DuckDB
func MigrateSQLiteToDuckDB(sqlitePath, duckdbPath string) error {
	log.Printf("Starting migration from SQLite to DuckDB")
	log.Printf("Source: %s", sqlitePath)
	log.Printf("Destination: %s", duckdbPath)

	// Expand paths
	sqlitePath = expandPath(sqlitePath)
	duckdbPath = expandPath(duckdbPath)

	// Check if SQLite database exists
	if _, err := os.Stat(sqlitePath); os.IsNotExist(err) {
		return fmt.Errorf("SQLite database not found at %s", sqlitePath)
	}

	// Open SQLite database (read-only)
	sqliteDB, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro", sqlitePath))
	if err != nil {
		return fmt.Errorf("failed to open SQLite database: %w", err)
	}
	defer sqliteDB.Close()

	// Initialize new DuckDB database
	duckDB, err := Init(duckdbPath)
	if err != nil {
		return fmt.Errorf("failed to initialize DuckDB: %w", err)
	}
	defer duckDB.Close()

	// Run migrations to set up schema
	if err := RunMigrations(duckDB); err != nil {
		return fmt.Errorf("failed to run DuckDB migrations: %w", err)
	}

	// Migrate data table by table
	tables := []string{
		"messages",
		"threads",
		"tasks",
		"docs",
		"events",
		"prefs",
		"usage",
		"llm_cache",
		"sync_state",
	}

	for _, table := range tables {
		log.Printf("Migrating table: %s", table)
		if err := migrateTable(sqliteDB, duckDB, table); err != nil {
			return fmt.Errorf("failed to migrate table %s: %w", table, err)
		}
	}

	log.Println("✓ Migration completed successfully!")
	log.Printf("DuckDB database created at: %s", duckdbPath)
	log.Println()
	log.Println("Next steps:")
	log.Printf("1. Backup your SQLite database: cp %s %s.bak", sqlitePath, sqlitePath)
	log.Printf("2. Update config.yaml to point to: %s", duckdbPath)
	log.Println("3. Test the application with the new database")
	log.Printf("4. Once verified, you can remove the old SQLite database")

	return nil
}

func migrateTable(source *sql.DB, dest *DB, tableName string) error {
	// Check if table exists in source
	var count int
	err := source.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='%s'", tableName)).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check table existence: %w", err)
	}

	if count == 0 {
		log.Printf("  Skipping %s (table doesn't exist in source)", tableName)
		return nil
	}

	// Get row count
	var rowCount int
	err = source.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", tableName)).Scan(&rowCount)
	if err != nil {
		return fmt.Errorf("failed to count rows: %w", err)
	}

	if rowCount == 0 {
		log.Printf("  Skipping %s (no data)", tableName)
		return nil
	}

	// Query all rows from source
	rows, err := source.Query(fmt.Sprintf("SELECT * FROM %s", tableName))
	if err != nil {
		return fmt.Errorf("failed to query source table: %w", err)
	}
	defer rows.Close()

	// Get column names
	columns, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("failed to get columns: %w", err)
	}

	// Prepare insert statement for destination
	placeholders := ""
	for i := range columns {
		if i > 0 {
			placeholders += ", "
		}
		placeholders += "?"
	}

	insertSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		tableName,
		joinColumns(columns),
		placeholders)

	// Prepare statement
	stmt, err := dest.Prepare(insertSQL)
	if err != nil {
		return fmt.Errorf("failed to prepare insert statement: %w", err)
	}
	defer stmt.Close()

	// Copy rows
	copied := 0
	for rows.Next() {
		// Create slice of interface{} to scan into
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		// Convert SQLite INTEGER booleans to DuckDB BOOLEAN for specific columns
		if tableName == "threads" {
			for i, col := range columns {
				if col == "relevant_to_user" {
					// Convert INTEGER (0/1) to BOOLEAN (false/true)
					if intVal, ok := values[i].(int64); ok {
						values[i] = intVal != 0
					}
				}
			}
		}

		// Insert into destination
		if _, err := stmt.Exec(values...); err != nil {
			return fmt.Errorf("failed to insert row: %w", err)
		}

		copied++
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating rows: %w", err)
	}

	log.Printf("  ✓ Migrated %d rows from %s", copied, tableName)
	return nil
}

func joinColumns(columns []string) string {
	result := ""
	for i, col := range columns {
		if i > 0 {
			result += ", "
		}
		result += col
	}
	return result
}

func expandPath(path string) string {
	if len(path) >= 2 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
