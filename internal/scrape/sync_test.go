package scrape

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/search"
)

func TestSyncAsmrOneCheckpointResumeDuplicatesAndTracks(t *testing.T) {
	var pageHits [5]atomic.Int32
	var trackHits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/works":
			p, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if p >= 1 && p <= 4 {
				pageHits[p].Add(1)
			}
			// 4 total pages, 2 works per page (8 works total)
			if p > 4 {
				_, _ = io.WriteString(w, `{"works":[],"pagination":{"currentPage":5,"pageSize":2,"totalCount":8}}`)
				return
			}
			id1 := 1000000 + (p-1)*2 + 1
			id2 := 1000000 + (p-1)*2 + 2
			_, _ = fmt.Fprintf(w, `{
				"works": [
					{
						"id": %d,
						"source_id": "RJ%08d",
						"title": "全量同步作品_%d",
						"nsfw": false,
						"release": "2026-05-01",
						"create_date": "2026-05-02",
						"dl_count": 320,
						"price": 1100,
						"rate_average_2dp": 4.9,
						"rate_count": 45,
						"has_subtitle": true,
						"duration": 1800,
						"age_category_string": "general",
						"mainCoverUrl": "%s/cover/%d.jpg",
						"language_editions": [{"lang":"JPN","label":"日本語","workno":"RJ%08d"}],
						"circle": {"id": 99, "name": "音葉サークル", "source_id": "RG01005704"},
						"vas": [{"id": "u1", "name": "早見テスト"}],
						"tags": [{"id": 1, "name": "癒し", "i18n": {"zh-cn": {"name": "治愈"}}}]
					},
					{
						"id": %d,
						"source_id": "RJ%08d",
						"title": "全量同步作品_%d",
						"nsfw": false,
						"release": "2026-05-01",
						"age_category_string": "general",
						"duration": 900,
						"mainCoverUrl": "%s/cover/%d.jpg",
						"circle": {"id": 99, "name": "音葉サークル", "source_id": "RG01005704"},
						"vas": [{"id": "u2", "name": "花澤テスト"}],
						"tags": [{"id": 2, "name": "耳かき", "i18n": {"zh-cn": {"name": "掏耳"}}}]
					}
				],
				"pagination": {"currentPage": %d, "pageSize": 2, "totalCount": 8}
			}`, id1, id1, id1, "http://example.invalid", id1, id1, id2, id2, id2, "http://example.invalid", id2, p)

		case "/api/tracks/1000001":
			trackHits.Add(1)
			_, _ = io.WriteString(w, `[
				{
					"type": "folder",
					"title": "SE有り",
					"children": [
						{
							"type": "audio",
							"hash": "1000001/1",
							"title": "01 本編.mp3",
							"mediaStreamUrl": "http://example.invalid/stream/01.mp3",
							"mediaDownloadUrl": "http://example.invalid/dl/01.mp3",
							"duration": 612.5,
							"size": 123456
						},
						{
							"type": "text",
							"hash": "1000001/2",
							"title": "01 本編.lrc",
							"mediaDownloadUrl": "http://example.invalid/sub/01.lrc",
							"size": 1024
						}
					]
				},
				{
					"type": "folder",
					"title": "omake",
					"children": [
						{
							"type": "image",
							"hash": "1000001/3",
							"title": "jacket.jpg",
							"mediaDownloadUrl": "http://example.invalid/img/jacket.jpg",
							"size": 4096
						}
					]
				}
			]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	cfg := config.Default(dataDir)
	cfg.AsmrOneBaseURLs = []string{srv.URL}
	cfg.AsmrOneIntervalSec = 0.001

	store, err := db.Open(filepath.Join(dataDir, "otoha.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer store.Close()

	searchEng := search.Open(filepath.Join(dataDir, "index.bleve"))
	defer searchEng.Close()

	// 1. Pre-insert RJ01000001 so it is counted as a `duplicate` (NOT a `failure`!) during page 1 sync
	_, _, _ = store.SaveMetadataBatch(context.Background(), nil)
	_ = store.UpsertScannedWork(context.Background(), 0, "RJ01000001", "Existing", "", 1, "", nil)

	// 2. Interrupt after page 2 of 4 -> checkpoint must record NextPage = 3!
	ctxCancel, cancel := context.WithCancel(context.Background())
	cp1, err := SyncAsmrOneCatalog(ctxCancel, store, searchEng, cfg, SyncOptions{
		PageSize: 2,
		Order:    "id",
		Sort:     "asc",
	}, func(p AsmrOneSyncCheckpoint) {
		if p.CurrentPage == 2 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled after page 2, got %v", err)
	}
	if cp1.CurrentPage != 2 || cp1.NextPage != 3 || cp1.Inserted != 3 || cp1.Duplicates != 1 || cp1.Failures != 0 {
		t.Fatalf("unexpected checkpoint after page 2: %+v", cp1)
	}

	// 3. Resume without StartPage -> MUST start directly at page 3 (page 1 & 2 hit count stays 1!)
	cp2, err := SyncAsmrOneCatalog(context.Background(), store, searchEng, cfg, SyncOptions{
		PageSize: 2,
		Order:    "id",
		Sort:     "asc",
	}, nil)
	if err != nil || !cp2.Finished {
		t.Fatalf("resume sync failed: cp=%+v err=%v", cp2, err)
	}
	if pageHits[1].Load() != 1 || pageHits[2].Load() != 1 || pageHits[3].Load() != 1 || pageHits[4].Load() != 1 {
		t.Fatalf("expected each page 1..4 requested exactly once across interrupt+resume, got hits=%v", []int32{pageHits[1].Load(), pageHits[2].Load(), pageHits[3].Load(), pageHits[4].Load()})
	}
	if cp2.Inserted != 7 || cp2.Duplicates != 1 || cp2.Failures != 0 || cp2.Completeness.CircleRatePct != 100 {
		t.Fatalf("unexpected final sync stats: %+v", cp2)
	}

	// 4. Sync remote tracks + paired .lrc subtitle + omake attachments for RJ01000001
	asmrClient, _ := NewAsmrOneClient(cfg)
	nTr, err := SyncWorkRemoteTracks(context.Background(), store, asmrClient, "RJ01000001", 1000001)
	if err != nil || nTr != 1 {
		t.Fatalf("SyncWorkRemoteTracks: n=%d err=%v", nTr, err)
	}
	det, err := store.GetWorkDetail(context.Background(), "RJ01000001", 1)
	if err != nil {
		t.Fatalf("GetWorkDetail: %v", err)
	}
	if det.CircleID != "RG01005704" || len(det.Tracks) != 1 || !det.Tracks[0].IsRemote || det.Tracks[0].SubtitleURL != "http://example.invalid/sub/01.lrc" || det.RemoteFilesJSON == "" {
		t.Fatalf("unexpected synced work detail with remote tracks: %+v", det)
	}
}
