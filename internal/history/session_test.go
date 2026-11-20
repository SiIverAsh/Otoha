package history

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
)

func TestSessionAggregationProgressAutoAdvancePrivacyAndVacuum(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "otoha.db")
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer store.Close()

	cfg := config.Default(dir)
	svc := NewService(store, cfg)
	ctx := context.Background()

	st, _ := store.UpsertStorage(ctx, "local", "Main", dir, "")
	dur100 := 100.0
	if err := store.UpsertScannedWork(ctx, st.ID, "RJ01000001", "SFW Work", "RJ01000001", dlsite.AgeGeneral, "", []db.ScannedTrackInput{
		{RelPath: "SE有り/01.wav", Size: 1000, Duration: &dur100, Ord: 1},
		{RelPath: "SE無し/01.wav", Size: 1000, Duration: &dur100, Ord: 2},
	}); err != nil {
		t.Fatalf("UpsertScannedWork SFW: %v", err)
	}
	if err := store.SaveMetadata(ctx, &dlsite.Metadata{
		Workno:      "RJ01000002",
		Title:       "SECRET_R18_WORK_TITLE_NEVER_LEAK",
		AgeCategory: dlsite.AgeR18,
	}, "", true); err != nil {
		t.Fatalf("SaveMetadata R18: %v", err)
	}

	detail, _ := store.GetWorkDetail(ctx, "RJ01000001", 1)
	seWithTrackID := detail.Tracks[0].ID

	// 1. Heartbeat #1 at t=1000 (30s listened) -> starts `listening`, NOT `listened` even if user seeks to 99s!
	queue := &QueueSnapshot{TrackIDs: []int64{seWithTrackID}, CurrentIndex: 0, FolderScope: "SE有り"}
	r1, err := svc.RecordHeartbeat(ctx, HeartbeatRequest{
		UserID:        1,
		Workno:        "RJ01000001",
		TrackID:       seWithTrackID,
		Position:      99.0, // user dragged slider to 99s, but only listened 30s!
		ListenedDelta: 30.0,
		Queue:         queue,
		NowUnix:       1000,
	})
	if err != nil || !r1.Recorded || r1.Progress != "listening" {
		t.Fatalf("unexpected r1: %+v err=%v", r1, err)
	}

	// 2. Heartbeat #2 at t=1030 (< 30 min) -> merges into SAME session row, advances to `listened` once cumulative listened >= 85s!
	r2, err := svc.RecordHeartbeat(ctx, HeartbeatRequest{
		UserID:        1,
		Workno:        "RJ01000001",
		TrackID:       seWithTrackID,
		Position:      88.0,
		ListenedDelta: 60.0, // total listened = 90s >= 85% of 100s queue!
		Queue:         queue,
		NowUnix:       1030,
	})
	if err != nil || r2.SessionID != r1.SessionID || r2.Progress != "listened" || !r2.AutoAdvanced {
		t.Fatalf("expected session merge and auto-advance to listened, got r2=%+v err=%v", r2, err)
	}

	// 3. Heartbeat #3 at t=3000 (> 30 min = 1800s after 1030) -> creates NEW session row, but ListRecent still deduplicates by work!
	r3, err := svc.RecordHeartbeat(ctx, HeartbeatRequest{
		UserID:        1,
		Workno:        "RJ01000001",
		TrackID:       seWithTrackID,
		Position:      15.0,
		ListenedDelta: 15.0,
		Queue:         queue,
		NowUnix:       3000,
	})
	if err != nil || r3.SessionID == r1.SessionID {
		t.Fatalf("expected new session after >30m gap, got r3=%+v", r3)
	}

	// 4. Record R18 work in history -> default "general" AgeMode MUST hide it from History and Recent (§9.6)
	_, _ = svc.RecordHeartbeat(ctx, HeartbeatRequest{
		UserID:        1,
		Workno:        "RJ01000002",
		Position:      10,
		ListenedDelta: 10,
		Queue:         &QueueSnapshot{TrackIDs: []int64{1}, FolderScope: "SECRET_QUEUE_TAG_999"},
		NowUnix:       3100,
	})

	sfwHist, sfwTotal, err := svc.ListHistory(ctx, 1, "general", 1, 20)
	if err != nil || sfwTotal != 2 || len(sfwHist) != 2 {
		t.Fatalf("expected 2 SFW sessions (R18 hidden), got total=%d entries=%+v err=%v", sfwTotal, sfwHist, err)
	}
	recent, err := svc.ListRecent(ctx, 1, "general", 10)
	if err != nil || len(recent) != 1 || recent[0].Workno != "RJ01000001" {
		t.Fatalf("expected 1 deduplicated SFW recent work, got %+v err=%v", recent, err)
	}

	// 5. Incognito & Global Privacy Off MUST write zero rows (§9.6 & §12)
	_, allBefore, _ := svc.ListHistory(ctx, 1, "all", 1, 50)
	resIncog, _ := svc.RecordHeartbeat(ctx, HeartbeatRequest{
		UserID:        1,
		Workno:        "RJ01000001",
		Position:      50,
		ListenedDelta: 30,
		Incognito:     true,
		NowUnix:       5000,
	})
	if resIncog.Recorded {
		t.Fatalf("incognito heartbeat must not record")
	}

	_ = cfg.Update(func(c *config.Config) { c.HistoryEnabled = false })
	resDisabled, _ := svc.RecordHeartbeat(ctx, HeartbeatRequest{
		UserID:        1,
		Workno:        "RJ01000001",
		Position:      60,
		ListenedDelta: 30,
		NowUnix:       7000,
	})
	if resDisabled.Recorded {
		t.Fatalf("disabled history must not record")
	}
	_, allAfter, _ := svc.ListHistory(ctx, 1, "all", 1, 50)
	if allAfter != allBefore {
		t.Fatalf("row count changed while privacy disabled: %d -> %d", allBefore, allAfter)
	}

	// 6. Clear + VACUUM must purge SECRET_QUEUE_TAG_999 from raw db file bytes (§9.6 & §12)
	if _, err := svc.Clear(ctx, 1, ClearOptions{}); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	rawDB, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("ReadFile db: %v", err)
	}
	if bytes.Contains(rawDB, []byte("SECRET_QUEUE_TAG_999")) {
		t.Fatalf("VACUUM did not purge deleted history queue_ctx bytes from otoha.db!")
	}
}

func TestASMR18SegmentHistoryWaitsForFinalDuration(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	service := NewService(store, config.Default(t.TempDir()))
	work := &dlsite.Metadata{Workno: "RJ123456", Title: "Work", AgeCategory: dlsite.AgeGeneral}
	_, err = store.SaveASMR18Work(ctx, work, "page", []db.RemoteTrackInput{{
		RelPath: "ASMR18/file1.m3u8", StreamURL: "https://cdn3.cloudintech.net/file/RJ123456/1.m3u8",
		Chapters: []db.Chapter{{Title: "First", Start: 0}, {Title: "Last", Start: 60}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := store.GetWorkDetail(ctx, work.Workno, 1)
	if err != nil || len(detail.Tracks) != 2 {
		t.Fatalf("expected two tracks: %+v, %v", detail, err)
	}
	first, last := detail.Tracks[0], detail.Tracks[1]
	heartbeat := HeartbeatRequest{
		UserID: 1, Workno: work.Workno, TrackID: first.ID, ListenedDelta: 60, NowUnix: 1000,
		Queue: &QueueSnapshot{TrackIDs: []int64{first.ID, last.ID}},
	}
	result, err := service.RecordHeartbeat(ctx, heartbeat)
	if err != nil || result.Progress != "listening" {
		t.Fatalf("incomplete duration caused early completion: %+v, %v", result, err)
	}
	heartbeat.TrackID, heartbeat.TrackDuration = last.ID, 40
	heartbeat.ListenedDelta, heartbeat.Position, heartbeat.NowUnix = 30, 30, 1030
	result, err = service.RecordHeartbeat(ctx, heartbeat)
	if err != nil || result.Progress != "listened" {
		t.Fatalf("expected completion once the full duration was known: %+v, %v", result, err)
	}
	entries, _, err := service.ListHistory(ctx, 1, "general", 1, 20)
	if err != nil || len(entries) != 1 || entries[0].TrackID != last.ID || entries[0].TrackName != "Last" || entries[0].Position != 30 {
		t.Fatalf("history did not retain the segment name and relative offset: %+v, %v", entries, err)
	}
}
