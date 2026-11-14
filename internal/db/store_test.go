package db

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/SiIverAsh/otoha/internal/dlsite"
)

func TestMigrationsBackupAndTransactionalRollback(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "otoha.db")

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	// Insert user data before running v2/v3 migrations
	st, err := store.UpsertStorage(ctx, "local", "Main", dir, "")
	if err != nil {
		t.Fatalf("UpsertStorage: %v", err)
	}
	if err := store.UpsertScannedWork(ctx, st.ID, "RJ01234567", "Test Work", "RJ01234567", 1, "", nil); err != nil {
		t.Fatalf("UpsertScannedWork: %v", err)
	}
	progress := "listening"
	if _, err := store.UpsertUserState(ctx, 1, "RJ01234567", nil, nil, &progress); err != nil {
		t.Fatalf("UpsertUserState: %v", err)
	}

	// Simulate a failing future migration (partial DDL + syntax error) -> must back up & roll back!
	badMigrations := []Migration{
		{
			Version: 999,
			Name:    "999_broken.sql",
			SQL: `
				CREATE TABLE temp_should_rollback (id INTEGER PRIMARY KEY);
				THIS IS INVALID SQL THAT MUST FAIL;
			`,
		},
	}
	bakPath, migErr := RunMigrations(store.DB(), dbPath, badMigrations)
	if migErr == nil {
		t.Fatalf("expected broken migration to fail")
	}
	if bakPath == "" {
		t.Fatalf("expected backup path to be returned before running v2")
	}
	if info, err := os.Stat(bakPath); err != nil || info.Size() == 0 {
		t.Fatalf("backup file %s not created properly: %v", bakPath, err)
	}

	// Verifytemp_should_rollback was rolled back inside the transaction
	var tblCount int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='temp_should_rollback'`).Scan(&tblCount); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if tblCount != 0 {
		t.Fatalf("failed migration was NOT rolled back; temp_should_rollback exists!")
	}

	// Verify user_state survived untouched (§5: user_state never destroyed)
	detail, err := store.GetWorkDetail(ctx, "RJ01234567", 1)
	if err != nil || detail.Progress != "listening" {
		t.Fatalf("user_state lost after rollback: detail=%+v err=%v", detail, err)
	}
	store.Close()
}

func TestAgeFilteringAndUserTagsPreservation(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "otoha.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	// Save 1 all-ages work and 1 R18 work
	if err := store.SaveMetadata(ctx, &dlsite.Metadata{
		Workno:      "RJ01000001",
		Title:       "全年龄治愈音声",
		AgeCategory: dlsite.AgeGeneral,
		VAs:         []string{"花澤"},
		Tags:        []string{"治愈"},
	}, "", true); err != nil {
		t.Fatalf("SaveMetadata SFW: %v", err)
	}
	if err := store.SaveMetadata(ctx, &dlsite.Metadata{
		Workno:      "RJ01000002",
		Title:       "R18成人音声机密标题",
		AgeCategory: dlsite.AgeR18,
		VAs:         []string{"声优X"},
		Tags:        []string{"耳舐め"},
	}, "", true); err != nil {
		t.Fatalf("SaveMetadata R18: %v", err)
	}

	// Add user tag and verify subsequent DLsite metadata refresh preserves user tag!
	if _, err := store.SetWorkUserTags(ctx, "RJ01000001", []string{"睡前必听", "收藏"}); err != nil {
		t.Fatalf("SetWorkUserTags: %v", err)
	}
	if err := store.SaveMetadata(ctx, &dlsite.Metadata{
		Workno:      "RJ01000001",
		Title:       "全年龄治愈音声(更新)",
		AgeCategory: dlsite.AgeGeneral,
		Tags:        []string{"治愈", "ASMR"},
	}, "", true); err != nil {
		t.Fatalf("SaveMetadata refresh: %v", err)
	}

	// Default AgeMode ("general") must completely hide R18 work (§3.4 & §12)
	sfwList, total, err := store.ListWorks(ctx, ListWorksOptions{AgeMode: "general"})
	if err != nil {
		t.Fatalf("ListWorks general: %v", err)
	}
	if total != 1 || len(sfwList) != 1 || sfwList[0].Workno != "RJ01000001" {
		t.Fatalf("expected only 1 SFW work, got total=%d works=%+v", total, sfwList)
	}
	if len(sfwList[0].Tags) != 4 {
		t.Fatalf("expected 2 user tags + 2 dlsite tags = 4, got %+v", sfwList[0].Tags)
	}

	// AgeMode "all" returns both
	_, allTotal, err := store.ListWorks(ctx, ListWorksOptions{AgeMode: "all"})
	if err != nil || allTotal != 2 {
		t.Fatalf("expected 2 works in all mode, got %d (err=%v)", allTotal, err)
	}
}

func TestVacuumPhysicallyRemovesDeletedBytes(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "otoha.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	secretMarker := "UNIQUE_SECRET_QUEUE_MARKER_RJ01999888_DO_NOT_LEAK"
	if _, err := store.DB().ExecContext(ctx, `
		INSERT INTO listen_history (user_id, workno, track_id, started_at, last_at, position, listened, queue_ctx)
		VALUES (1, 'RJ01999888', 1, 1000, 1030, 30, 30, ?)
	`, secretMarker); err != nil {
		t.Fatalf("insert history: %v", err)
	}
	_, _ = store.DB().ExecContext(ctx, `PRAGMA wal_checkpoint(FULL);`)

	if _, err := store.DB().ExecContext(ctx, `DELETE FROM listen_history`); err != nil {
		t.Fatalf("delete history: %v", err)
	}
	if err := store.VacuumAndTruncateWAL(ctx); err != nil {
		t.Fatalf("VacuumAndTruncateWAL: %v", err)
	}

	for _, p := range []string{dbPath, dbPath + "-wal"} {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if bytes.Contains(raw, []byte(secretMarker)) {
			t.Fatalf("found deleted history marker in %s after VACUUM!", p)
		}
	}
}
