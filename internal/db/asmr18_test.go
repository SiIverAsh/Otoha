package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/SiIverAsh/otoha/internal/dlsite"
)

func TestASMR18DedupPreservesMetadataTracksAndProgress(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	original := &dlsite.Metadata{Workno: "RJ123456", Title: "Existing title", CircleName: "Existing circle", Rating: 4.9, AgeCategory: 1, VAs: []string{"Existing VA"}, Tags: []string{"Existing tag"}}
	if err := s.SaveMetadata(ctx, original, "", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRemoteTracks(ctx, "RJ123456", []RemoteTrackInput{{RelPath: "original.mp3", StreamURL: "https://example.test/original.mp3"}}, `[{"type":"text"}]`); err != nil {
		t.Fatal(err)
	}
	incoming := &dlsite.Metadata{Workno: "rj123456", Title: "Incoming title", CircleName: "Incoming circle", AgeCategory: 3, VAs: []string{"Incoming VA"}, Tags: []string{"Incoming tag"}}
	tracks := []RemoteTrackInput{{Title: "ASMR18 display title", RelPath: "ASMR18/file1.m3u8", StreamURL: "https://cdn3.cloudintech.net/file/RJ123456/1.m3u8", Duration: 120, Chapters: []Chapter{{Title: "First", Start: 0}, {Title: "Second", Start: 60}}}}
	inserted, err := s.SaveASMR18Work(ctx, incoming, "https://asmr18.fans/boys/rj123456/", tracks)
	if err != nil || inserted {
		t.Fatalf("inserted=%v err=%v", inserted, err)
	}
	detail, err := s.GetWorkDetail(ctx, "RJ123456", 1)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Title != original.Title || detail.CircleName != original.CircleName || detail.Rating != 4.9 || detail.AgeCategory != 1 || detail.VAs[0] != "Existing VA" || detail.RemoteFilesJSON != `[{"type":"text"}]` {
		t.Fatalf("lost existing data: %+v", detail)
	}
	if len(detail.Tracks) != 3 || detail.Tracks[0].RelPath != "original.mp3" || detail.Tracks[1].Title != "First" || detail.Tracks[2].Title != "Second" {
		t.Fatalf("original provider track or separate ASMR18 tracks missing: %+v", detail.Tracks)
	}
	first, second := detail.Tracks[1], detail.Tracks[2]
	if first.SourceTrackID == nil || first.StartSeconds == nil || *first.StartSeconds != 0 || first.EndSeconds == nil || *first.EndSeconds != 60 {
		t.Fatalf("first segment identity or range is missing: %+v", first)
	}
	if _, err := s.DB().Exec(`INSERT INTO play_position(user_id,track_id,position,updated_at) VALUES(1,?,45,1)`, first.ID); err != nil {
		t.Fatal(err)
	}
	tracks[0].StreamURL = "https://cdn3.cloudintech.net/file/RJ123456/refreshed.m3u8"
	for i := 0; i < 2; i++ {
		if _, err := s.SaveASMR18Work(ctx, incoming, "https://asmr18.fans/boys/rj123456/", tracks); err != nil {
			t.Fatal(err)
		}
	}
	detail, err = s.GetWorkDetail(ctx, "RJ123456", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Tracks) != 3 {
		t.Fatalf("duplicate tracks: %d", len(detail.Tracks))
	}
	if source, err := s.ResolveTrack(ctx, *first.SourceTrackID); err != nil || source.Title != "ASMR18 display title" {
		t.Fatalf("original stream ID lost: %+v %v", source, err)
	}
	if got := detail.Tracks[1]; got.ID != first.ID || got.PlayPosition != 45 || got.StreamURL != tracks[0].StreamURL || detail.Tracks[2].ID != second.ID {
		t.Fatalf("retry changed the segment IDs, position, or source: %+v", detail.Tracks)
	}
	if resolved, err := s.ResolveTrack(ctx, second.ID); err != nil || resolved.StreamURL != tracks[0].StreamURL || resolved.StorageKind != "asmr18" {
		t.Fatalf("segment could not resolve refreshed parent stream: %+v %v", resolved, err)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM work`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("RJ duplicates: %d %v", count, err)
	}
}

func TestASMR18BackfillKeepsOriginalPositionsAndPrunesRemovedStreams(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	meta := &dlsite.Metadata{Workno: "RJ123456", Title: "Work"}
	source := RemoteTrackInput{RelPath: "ASMR18/file1.m3u8", StreamURL: "https://cdn3.cloudintech.net/file/RJ123456/1.m3u8"}
	if _, err := store.SaveASMR18Work(ctx, meta, "page", []RemoteTrackInput{source}); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.ListASMR18TrackCandidates(ctx, []string{"rj123456"}, 1)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates = %+v, %v", candidates, err)
	}
	candidate := candidates[0]
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO play_position(user_id,track_id,position,updated_at) VALUES(1,?,75,1)`, candidate.ID); err != nil {
		t.Fatal(err)
	}
	segments := ASMR18SegmentsFromChapters([]Chapter{{Title: "First", Start: 0}, {Title: "Last", Start: 60}}, 0)
	if err := store.SaveASMR18TrackSegments(ctx, candidate, segments); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveASMR18TrackSegments(ctx, candidate, segments); err != nil {
		t.Fatalf("saving the same split twice: %v", err)
	}
	candidates, err = store.ListASMR18TrackCandidates(ctx, nil, 0)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("already segmented work is still pending: %+v, %v", candidates, err)
	}
	detail, err := store.GetWorkDetail(ctx, meta.Workno, 1)
	if err != nil || len(detail.Tracks) != 2 || detail.TrackCount != 2 {
		t.Fatalf("detail did not replace the parent with two tracks: %+v, %v", detail, err)
	}
	last := detail.Tracks[1]
	if detail.Tracks[0].PlayPosition != 0 || last.PlayPosition != 15 || last.EndSeconds != nil || last.Duration != nil {
		t.Fatalf("invalid open-ended segment or inherited resume: %+v", detail.Tracks)
	}
	if err := store.UpdateTrackDuration(ctx, candidate.ID, 110); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveTrack(ctx, last.ID)
	if err != nil || resolved.EndSeconds == nil || *resolved.EndSeconds != 110 || resolved.Duration == nil || *resolved.Duration != 50 {
		t.Fatalf("open-ended segment did not learn the source duration: %+v, %v", resolved, err)
	}
	updated := source
	updated.RelPath = "ASMR18/replacement.m3u8"
	if _, err := store.SaveASMR18Work(ctx, meta, "page", []RemoteTrackInput{updated}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveTrack(ctx, last.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed stream left an orphan segment: %v", err)
	}
}

func TestASMR18BackfillRejectsStaleCandidateAndMultiStream(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	meta := &dlsite.Metadata{Workno: "RJ123456", Title: "Work"}
	source := RemoteTrackInput{RelPath: "ASMR18/file1.m3u8", StreamURL: "https://cdn3.cloudintech.net/file/RJ123456/1.m3u8", Duration: 120}
	if _, err := store.SaveASMR18Work(ctx, meta, "page", []RemoteTrackInput{source}); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.ListASMR18TrackCandidates(ctx, nil, 0)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates = %+v, %v", candidates, err)
	}
	segments := ASMR18SegmentsFromChapters([]Chapter{{Title: "First", Start: 0}, {Title: "Last", Start: 60}}, 120)
	stale := candidates[0]
	stale.StreamURL += "?expired"
	if err := store.SaveASMR18TrackSegments(ctx, stale, segments); err == nil {
		t.Fatal("accepted a stale source URL")
	}
	second := source
	second.RelPath = "ASMR18/file2.m3u8"
	if _, err := store.SaveASMR18Work(ctx, meta, "page", []RemoteTrackInput{source, second}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveASMR18TrackSegments(ctx, candidates[0], segments); err == nil {
		t.Fatal("accepted a work which now has two streams")
	}
	got, err := store.ListASMR18TrackCandidates(ctx, nil, 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("multi-stream work is eligible: %+v, %v", got, err)
	}
}

func TestASMR18PreservesLocalPriorityAndStorage(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	local, err := s.UpsertStorage(ctx, "local", "Local", "/library", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertScannedWork(ctx, local.ID, "RJ123456", "Local title", "RJ123456", 1, "", []ScannedTrackInput{{RelPath: "local.mp3", Ord: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveASMR18Work(ctx, &dlsite.Metadata{Workno: "RJ123456", Title: "Incoming"}, "page", []RemoteTrackInput{{RelPath: "ASMR18/file1.m3u8", StreamURL: "https://cdn3.cloudintech.net/file/RJ123456/1.m3u8"}}); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetWorkDetail(ctx, "RJ123456", 1)
	if err != nil || len(d.Tracks) != 1 || d.Tracks[0].IsRemote {
		t.Fatalf("local priority lost: %+v %v", d, err)
	}
	conflict, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conflict.Close()
	if _, err := conflict.UpsertStorage(ctx, "local", "asmr18.fans", "/keep", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := conflict.SaveASMR18Work(ctx, &dlsite.Metadata{Workno: "RJ123456"}, "page", []RemoteTrackInput{{RelPath: "file"}}); err == nil {
		t.Fatal("overwrote local storage name")
	}
	var kind string
	if err := conflict.DB().QueryRow(`SELECT kind FROM storage WHERE name='asmr18.fans'`).Scan(&kind); err != nil || kind != "local" {
		t.Fatal("changed local storage")
	}
}

func TestASMR18NewWorkAndAtomicRollback(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	meta := &dlsite.Metadata{Workno: "RJ01752865", Title: "New work", AgeCategory: 1}
	tracks := []RemoteTrackInput{{RelPath: "ASMR18/file1.m3u8", StreamURL: "https://cdn3.cloudintech.net/file/RJ01752865/1.m3u8", Chapters: []Chapter{{Title: "Invalid", Start: -1}}}}
	if _, err := s.SaveASMR18Work(ctx, meta, "page", tracks); err == nil {
		t.Fatal("expected rollback")
	}
	var count int
	s.DB().QueryRow(`SELECT count(*) FROM work`).Scan(&count)
	if count != 0 {
		t.Fatal("partial work committed")
	}
	tracks[0].Chapters[0].Start = 0
	inserted, err := s.SaveASMR18Work(ctx, meta, "page", tracks)
	if err != nil || !inserted {
		t.Fatalf("%v %v", inserted, err)
	}
}
