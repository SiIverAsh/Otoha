package kikoeru

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/SiIverAsh/otoha/internal/db"
)

func buildSampleKikoeruDB(t *testing.T, dbPath string) {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sample kikoeru db: %v", err)
	}
	defer conn.Close()

	schema := `
	CREATE TABLE knex_migrations (id INTEGER PRIMARY KEY, name TEXT);
	INSERT INTO knex_migrations (id, name) VALUES (1, '20210502081522_init.js');

	CREATE TABLE t_circle (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
	CREATE TABLE t_work (
		id INTEGER PRIMARY KEY,
		root_folder TEXT NOT NULL,
		dir TEXT NOT NULL,
		title TEXT NOT NULL,
		circle_id INTEGER NOT NULL,
		nsfw BOOLEAN,
		release TEXT,
		dl_count INTEGER,
		price INTEGER,
		review_count INTEGER,
		rate_count INTEGER,
		rate_average_2dp REAL,
		rate_count_detail TEXT,
		rank TEXT
	);
	CREATE TABLE t_tag (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
	CREATE TABLE t_va (id TEXT PRIMARY KEY, name TEXT NOT NULL);
	CREATE TABLE r_tag_work (tag_id INTEGER, work_id INTEGER, PRIMARY KEY (tag_id, work_id));
	CREATE TABLE r_va_work (va_id TEXT, work_id INTEGER, PRIMARY KEY (va_id, work_id));
	CREATE TABLE t_user (name TEXT PRIMARY KEY, password TEXT, "group" TEXT);
	CREATE TABLE t_review (
		user_name TEXT,
		work_id INTEGER,
		rating INTEGER,
		review_text TEXT,
		progress TEXT,
		created_at TEXT,
		updated_at TEXT,
		PRIMARY KEY (user_name, work_id)
	);
	-- azuse fork t_history (§14.4 exception)
	CREATE TABLE t_history (
		id INTEGER PRIMARY KEY,
		user_name TEXT NOT NULL,
		work_id TEXT NOT NULL,
		file_index TEXT NOT NULL,
		file_name TEXT,
		play_time INTEGER,
		total_time INTEGER,
		created_at TEXT,
		updated_at TEXT
	);

	INSERT INTO t_circle (id, name) VALUES (12345, '音葉社'), (99999, 'WrongCircle');

	-- Normal 6-digit work RJ334212
	INSERT INTO t_work VALUES (334212, 'Voices', '[音葉社][RJ334212] 正常六位作品', '正常六位作品', 12345, 0, '2022-05-01', 1500, 1100, 10, 80, 4.85, '[]', '');

	-- Truncated 8-digit bug (§14.3): folder is RJ01234567, kikoeru truncated '012345' -> id = 12345!
	INSERT INTO t_work VALUES (12345, 'Voices', 'RJ01234567 真正的八位作品', '错误的六位标题', 99999, 1, '2010-01-01', 5, 100, 0, 1, 2.0, '[]', '');

	INSERT INTO t_tag VALUES (1, '耳かき'), (2, 'バイノーラル');
	INSERT INTO r_tag_work VALUES (1, 334212), (2, 334212);

	INSERT INTO t_va VALUES ('550e8400-e29b-41d4-a716-446655440000', '花澤テスト');
	INSERT INTO r_va_work VALUES ('550e8400-e29b-41d4-a716-446655440000', 334212);

	INSERT INTO t_user VALUES ('admin', 'md5_unsavable_hash', 'administrator');
	INSERT INTO t_review VALUES ('admin', 334212, 5, '神作推荐', 'listened', '2023-01-01', '2023-01-02');
	INSERT INTO t_review VALUES ('admin', 12345, 4, '八位号听感不错', 'listening', '2023-02-01', '2023-02-02');

	INSERT INTO t_history VALUES (1, 'admin', '12345', '0', '01.wav', 185, 600, '2023-02-02 12:00:00', '2023-02-02 12:03:05');
	`
	if _, err := conn.Exec(schema); err != nil {
		t.Fatalf("seed sample kikoeru schema: %v", err)
	}
}

func sha256File(t *testing.T, p string) [32]byte {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return sha256.Sum256(raw)
}

func TestImportKikoeruDryRunReadOnlyConflictAndCommit(t *testing.T) {
	dir := t.TempDir()
	srcDB := filepath.Join(dir, "kikoeru.sqlite3")
	buildSampleKikoeruDB(t, srcDB)

	hashBefore := sha256File(t, srcDB)

	dstStore, err := db.Open(filepath.Join(dir, "otoha.db"))
	if err != nil {
		t.Fatalf("db.Open dst: %v", err)
	}
	defer dstStore.Close()
	ctx := context.Background()

	dryReportPath := filepath.Join(dir, "dry-report.json")
	dryRep, err := RunImport(ctx, dstStore, ImportOptions{
		SourceDBPath:   srcDB,
		DryRun:         true,
		ReportFilePath: dryReportPath,
	})
	if err != nil {
		t.Fatalf("RunImport dry-run: %v", err)
	}

	// §12: Source DB must be 100% untouched byte-for-byte!
	hashAfterDry := sha256File(t, srcDB)
	if hashBefore != hashAfterDry {
		t.Fatalf("source kikoeru DB was mutated during --dry-run!")
	}

	// Verify 8-digit truncation conflict detected (§12 & §14.3)
	if dryRep.ConflictsDetected != 1 || dryRep.Conflicts[0].DiskWorkno != "RJ01234567" || dryRep.Conflicts[0].PaddedWorkno != "RJ012345" {
		t.Fatalf("expected 8-digit conflict RJ012345 -> RJ01234567, got %+v", dryRep.Conflicts)
	}
	if !dryRep.AzuseHistoryFound || dryRep.HistoryImported != 1 {
		t.Fatalf("expected azuse t_history detected, got %+v", dryRep)
	}
	if st, err := os.Stat(dryReportPath); err != nil || st.Size() == 0 {
		t.Fatalf("expected dry-run report file on disk, err=%v", err)
	}
	// Dry-run must NOT write to dstStore
	if _, total, _ := dstStore.ListWorks(ctx, db.ListWorksOptions{AgeMode: "all"}); total != 0 {
		t.Fatalf("dry-run wrote %d works to target DB!", total)
	}

	// Real commit import
	realReportPath := filepath.Join(dir, "commit-report.json")
	rep, err := RunImport(ctx, dstStore, ImportOptions{
		SourceDBPath:   srcDB,
		DryRun:         false,
		ReportFilePath: realReportPath,
	})
	if err != nil {
		t.Fatalf("RunImport commit: %v", err)
	}
	if sha256File(t, srcDB) != hashBefore {
		t.Fatalf("source kikoeru DB was mutated during real import!")
	}
	if rep.UniqueWorksImported != 2 {
		t.Fatalf("expected 2 imported works, got %d", rep.UniqueWorksImported)
	}

	// Verify admin user imported WITHOUT MD5 password and flagged must_reset_password = true (§14.4)
	adminUser, err := dstStore.GetUserByUsername(ctx, "admin")
	if err != nil || adminUser.PasswordHash != "" || !adminUser.MustResetPassword {
		t.Fatalf("expected admin user with empty password_hash & must_reset_password=true, got %+v err=%v", adminUser, err)
	}

	// Verify 6-digit work has VAs (UUID discarded) & review preserved
	w6, err := dstStore.GetWorkDetail(ctx, "RJ334212", adminUser.ID)
	if err != nil || len(w6.VAs) != 1 || w6.VAs[0] != "花澤テスト" || w6.Progress != "listened" {
		t.Fatalf("unexpected RJ334212 detail: %+v err=%v", w6, err)
	}

	// Verify 8-digit conflict work resolved to RJ01234567, flagged NeedsRescrape, AND user review preserved!
	w8, err := dstStore.GetWorkDetail(ctx, "RJ01234567", adminUser.ID)
	if err != nil || !w8.NeedsRescrape || w8.Progress != "listening" || w8.State == nil || w8.State.Review != "八位号听感不错" {
		t.Fatalf("unexpected RJ01234567 detail: %+v err=%v", w8, err)
	}
}
