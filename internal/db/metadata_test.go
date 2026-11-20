package db

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/SiIverAsh/otoha/internal/dlsite"
)

func completeMetadata(wno string) *dlsite.Metadata {
	return &dlsite.Metadata{
		Workno: wno, AsmrOneID: 123, Title: "Full title", CircleID: "RG123", CircleName: "Circle",
		ReleaseDate: "2026-10-01", CreateDate: "2026-09-01", AgeCategory: 1, WorkType: "SOU",
		Price: 990, FileSize: 1234567, Duration: 180, HasSubtitle: true, Rating: 4.7,
		RateCount: 21, ReviewCount: 7, DLCount: 300, SeriesID: "42", SeriesName: "Series",
		CoverURL: "https://example.test/main.jpg", SamCoverURL: "https://example.test/sample.jpg",
		ThumbnailCoverURL: "https://example.test/thumb.jpg", OriginalWorkno: "RJ654321",
		SourceType: "DLsite", SourceURL: "https://example.test/work", WorkAttributes: "音声",
		LanguageEditionsJSON: `[{"lang":"ja_jp"}]`, OtherEditionsJSON: `[{"workno":"RJ654321"}]`,
		TranslationInfoJSON: `{"is_original":true}`, RateDetailJSON: `{"5":10}`, RankJSON: `[{"rank":3}]`,
		VAs: []string{"Voice"}, Scenarios: []string{"Writer"}, Illusts: []string{"Artist"}, Musics: []string{"Composer"},
		Tags: []string{"ASMR", "Healing"}, FetchedAt: 1700000000,
	}
}

func assertPersistedMetadata(t *testing.T, detail *WorkDetail, want *dlsite.Metadata) {
	t.Helper()
	meta := reflect.ValueOf(want).Elem()
	stored := reflect.ValueOf(detail).Elem()
	for i := 0; i < meta.NumField(); i++ {
		name := meta.Type().Field(i).Name
		switch name {
		case "Source", "Locale", "NotFound", "Tags":
			continue
		case "FetchedAt":
			name = "ScrapedAt"
		}
		got := stored.FieldByName(name)
		if !got.IsValid() || !reflect.DeepEqual(got.Interface(), meta.Field(i).Interface()) {
			t.Errorf("%s = %v, want %v", name, got, meta.Field(i).Interface())
		}
	}
}

func TestFillMissingMetadataFillsAllFieldsAndPreservesPlayback(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	wno := "RJ123456"
	tracks := []RemoteTrackInput{{Title: "Keep track", RelPath: "ASMR18/main.m3u8", StreamURL: "https://example.test/main.m3u8", Duration: 120, Chapters: []Chapter{{Title: "Keep chapter", Start: 0}}}}
	if _, err := s.SaveASMR18Work(ctx, &dlsite.Metadata{Workno: wno}, "https://asmr18.fans/allages/rj123456/", tracks); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE work SET title='',circle_name='  ',release_date=NULL,age_category=NULL,price=NULL,scraped_at=0,created_at=77,needs_rescrape=1 WHERE workno=?`, wno); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetWorkDetail(ctx, wno, 1)
	if err != nil {
		t.Fatal(err)
	}
	trackID := before.Tracks[0].ID
	if _, err := s.DB().Exec(`INSERT INTO play_position(user_id,track_id,position,updated_at) VALUES(1,?,35,5)`, trackID); err != nil {
		t.Fatal(err)
	}
	progress := "listening"
	if _, err := s.UpsertUserState(ctx, 1, wno, nil, nil, &progress); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWorkUserTags(ctx, wno, []string{"Personal"}); err != nil {
		t.Fatal(err)
	}
	want := completeMetadata(wno)
	if err := s.FillMissingMetadata(ctx, want, "/covers/downloaded.jpg"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetWorkDetail(ctx, wno, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertPersistedMetadata(t, got, want)
	if got.CreatedAt != 77 || !got.NeedsRescrape || got.CoverPath != "/covers/downloaded.jpg" || got.Progress != progress {
		t.Fatalf("existing work state changed: %+v", got)
	}
	if len(got.Tracks) != 1 || got.Tracks[0].ID != trackID || got.Tracks[0].PlayPosition != 35 || got.Tracks[0].StreamURL != tracks[0].StreamURL || !reflect.DeepEqual(got.Tracks[0].Chapters, tracks[0].Chapters) || got.ASMR18SourceURL != before.ASMR18SourceURL {
		t.Fatalf("playback/source data changed: %+v", got)
	}
	tags := map[string]string{}
	for _, tag := range got.Tags {
		tags[tag.Name] = tag.Source
	}
	if !reflect.DeepEqual(tags, map[string]string{"Personal": "user", "ASMR": "dlsite", "Healing": "dlsite"}) {
		t.Fatalf("tags = %v", tags)
	}
}

func TestFillMissingMetadataPreservesKnownValuesAndMergesRelations(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	original := completeMetadata("RJ123456")
	if err := s.SaveMetadata(ctx, original, "/local/cover.jpg", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWorkUserTags(ctx, original.Workno, []string{"User", "Shared"}); err != nil {
		t.Fatal(err)
	}
	incoming := completeMetadata("rj123456")
	// Change every scalar to ensure none can overwrite an existing value.
	value := reflect.ValueOf(incoming).Elem()
	for i := 0; i < value.NumField(); i++ {
		if value.Type().Field(i).Name == "Workno" {
			continue
		}
		switch value.Field(i).Kind() {
		case reflect.String:
			value.Field(i).SetString("replacement")
		case reflect.Int, reflect.Int64:
			value.Field(i).SetInt(99)
		case reflect.Float64:
			value.Field(i).SetFloat(1.1)
		}
	}
	incoming.VAs, incoming.Scenarios, incoming.Illusts, incoming.Musics = []string{"Voice", "Added voice"}, []string{"Writer"}, []string{"Artist"}, []string{"Composer"}
	incoming.Tags = []string{"Healing", "Shared", "New tag"}
	for i := 0; i < 2; i++ {
		if err := s.FillMissingMetadata(ctx, incoming, "/remote/cover.jpg"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetWorkDetail(ctx, original.Workno, 1)
	if err != nil {
		t.Fatal(err)
	}
	original.VAs = []string{"Voice", "Added voice"}
	assertPersistedMetadata(t, got, original)
	if got.CoverPath != "/local/cover.jpg" {
		t.Fatalf("local cover replaced: %s", got.CoverPath)
	}
	tags := map[string]string{}
	for _, tag := range got.Tags {
		tags[tag.Name] = tag.Source
	}
	if !reflect.DeepEqual(tags, map[string]string{"ASMR": "dlsite", "Healing": "dlsite", "New tag": "dlsite", "User": "user", "Shared": "user"}) {
		t.Fatalf("tags overwritten or duplicated: %v", tags)
	}
}

func TestFillMissingMetadataRollsBackRelationFailure(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.SaveMetadata(ctx, &dlsite.Metadata{Workno: "RJ123456", Title: "Keep"}, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`CREATE TRIGGER fail_person BEFORE INSERT ON person BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.FillMissingMetadata(ctx, completeMetadata("RJ123456"), ""); err == nil {
		t.Fatal("expected relation failure")
	}
	got, err := s.GetWorkDetail(ctx, "RJ123456", 1)
	if err != nil || got.ReleaseDate != "" || got.CircleID != "" {
		t.Fatalf("partial metadata committed: %+v, %v", got, err)
	}
	if err := s.FillMissingMetadata(ctx, completeMetadata("RJ999999"), ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing work error = %v", err)
	}
}

func TestListIncompleteASMR18MetadataUsesCoreFields(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, id := range []string{"RJ100001", "RJ100002", "RJ100003", "RJ100004"} {
		m := &dlsite.Metadata{Workno: id, Title: "Title", CircleID: "RG1", CircleName: "Circle", ReleaseDate: "2026-01-01", WorkType: "SOU", AgeCategory: 1, CoverURL: "cover"}
		switch id {
		case "RJ100002":
			m.ReleaseDate = ""
		case "RJ100003":
			m.WorkType = ""
		case "RJ100004":
			m.CircleID = ""
		}
		if _, err := s.SaveASMR18Work(ctx, m, "page", []RemoteTrackInput{{RelPath: "main.m3u8", StreamURL: "https://example.test/main.m3u8"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveMetadata(ctx, &dlsite.Metadata{Workno: "RJ100005", Title: "Other provider"}, "", true); err != nil {
		t.Fatal(err)
	}
	incomplete, err := s.ListIncompleteASMR18Worknos(ctx)
	if err != nil || !reflect.DeepEqual(incomplete, []string{"RJ100002", "RJ100003", "RJ100004"}) {
		t.Fatalf("incomplete = %v, %v", incomplete, err)
	}
	all, err := s.ListAllASMR18Worknos(ctx)
	if err != nil || !reflect.DeepEqual(all, []string{"RJ100001", "RJ100002", "RJ100003", "RJ100004"}) {
		t.Fatalf("all = %v, %v", all, err)
	}
	for _, id := range incomplete {
		if err := s.FillMissingMetadata(ctx, completeMetadata(id), ""); err != nil {
			t.Fatal(err)
		}
	}
	incomplete, err = s.ListIncompleteASMR18Worknos(ctx)
	if err != nil || len(incomplete) != 0 {
		t.Fatalf("filled works still selected: %v, %v", incomplete, err)
	}
}
