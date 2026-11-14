package db

import (
	"database/sql"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

// Migration represents a single numbered SQL migration script.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

func loadEmbeddedMigrations() ([]Migration, error) {
	return loadMigrationsFromFS(embeddedMigrations, "migrations")
}

func loadMigrationsFromFS(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var out []Migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		base := entry.Name()
		idx := strings.IndexByte(base, '_')
		if idx <= 0 {
			return nil, fmt.Errorf("invalid migration filename %q (expected NNN_name.sql)", base)
		}
		ver, err := strconv.Atoi(base[:idx])
		if err != nil {
			return nil, fmt.Errorf("invalid migration version in %q: %w", base, err)
		}
		raw, err := fs.ReadFile(fsys, dir+"/"+base)
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{
			Version: ver,
			Name:    base,
			SQL:     string(raw),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Version < out[j].Version
	})
	return out, nil
}

// RunMigrations applies pending migrations transactionally.
// Per §5 & §12: before applying any pending migration on a non-empty database file,
// it creates a backup at `<dbPath>.bak.<currentVersion>`, and rolls back on failure.
func RunMigrations(conn *sql.DB, dbPath string, migrations []Migration) (backupPath string, err error) {
	if _, err := conn.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return "", fmt.Errorf("init schema_migrations: %w", err)
	}

	applied := make(map[int]bool)
	curVersion := 0
	rows, err := conn.Query(`SELECT version FROM schema_migrations ORDER BY version ASC`)
	if err != nil {
		return "", fmt.Errorf("query schema_migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return "", err
		}
		applied[v] = true
		if v > curVersion {
			curVersion = v
		}
	}
	rows.Close()

	var pending []Migration
	for _, m := range migrations {
		if !applied[m.Version] {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		return "", nil
	}

	// Automatic pre-migration backup if dbPath already has existing schema/content (§5)
	if dbPath != "" && dbPath != ":memory:" {
		if st, statErr := os.Stat(dbPath); statErr == nil && st.Size() > 0 && curVersion > 0 {
			backupPath = fmt.Sprintf("%s.bak.%d", dbPath, curVersion)
			if err := backupDatabaseFile(conn, dbPath, backupPath); err != nil {
				return "", fmt.Errorf("pre-migration backup failed: %w", err)
			}
		} else if statErr == nil && st.Size() > 4096 && curVersion == 0 {
			// Existing file before migration tracking existed
			backupPath = fmt.Sprintf("%s.bak.0", dbPath)
			if err := backupDatabaseFile(conn, dbPath, backupPath); err != nil {
				return "", fmt.Errorf("pre-migration backup failed: %w", err)
			}
		}
	}

	for _, m := range pending {
		tx, txErr := conn.Begin()
		if txErr != nil {
			return backupPath, fmt.Errorf("begin migration %d (%s): %w", m.Version, m.Name, txErr)
		}
		if _, execErr := tx.Exec(m.SQL); execErr != nil {
			_ = tx.Rollback()
			return backupPath, fmt.Errorf("migration %d (%s) failed and rolled back: %w", m.Version, m.Name, execErr)
		}
		if _, recErr := tx.Exec(
			`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			m.Version, m.Name, time.Now().Unix(),
		); recErr != nil {
			_ = tx.Rollback()
			return backupPath, fmt.Errorf("record migration %d (%s) failed and rolled back: %w", m.Version, m.Name, recErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return backupPath, fmt.Errorf("commit migration %d (%s): %w", m.Version, m.Name, commitErr)
		}
	}

	return backupPath, nil
}

func backupDatabaseFile(conn *sql.DB, srcPath, dstPath string) error {
	// Flush WAL into main db file before copying so the backup is 100% self-consistent.
	_, _ = conn.Exec(`PRAGMA wal_checkpoint(FULL)`)
	_ = os.Remove(dstPath)

	escaped := strings.ReplaceAll(dstPath, "'", "''")
	if _, err := conn.Exec(fmt.Sprintf("VACUUM INTO '%s'", escaped)); err == nil {
		return nil
	}

	// Fallback byte copy if VACUUM INTO is unavailable
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
