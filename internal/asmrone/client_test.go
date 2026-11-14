package asmrone

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SiIverAsh/otoha/internal/dlsite"
)

func TestMirrorFailover404ShortCircuitI18nAndAgeMapping(t *testing.T) {
	var m1Hits atomic.Int32
	var m2Hits atomic.Int32

	mirror1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m1Hits.Add(1)
		// Verify required WAF headers
		if r.Header.Get("Referer") != "https://asmr.one/" || r.Header.Get("Origin") != "https://asmr.one" {
			t.Errorf("missing Referer/Origin headers: referer=%q origin=%q", r.Header.Get("Referer"), r.Header.Get("Origin"))
		}
		if r.URL.Path == "/api/workInfo/RJ999404" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"未找到该音声"}`)
			return
		}
		// Simulate mirror #1 outage (503)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer mirror1.Close()

	mirror2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m2Hits.Add(1)
		switch r.URL.Path {
		case "/api/workInfo/RJ298293":
			_, _ = io.WriteString(w, `{
				"id": 298293,
				"title": "蓄音レヱル 紅",
				"nsfw": true,
				"release": "2020-10-24",
				"dl_count": 9257,
				"price": 891,
				"review_count": 36,
				"rate_count": 3439,
				"rate_average_2dp": 4.8,
				"age_category_string": "r15",
				"original_workno": "RJ200001",
				"mainCoverUrl": "https://api.asmr-200.com/api/cover/298293.jpg?type=main",
				"circle": {"id": 49556, "name": "RaRo", "source_id": "RG49556"},
				"vas": [{"id": "uuid-1", "name": "黒沢ともよ"}],
				"tags": [
					{"id": 442, "name": "耳かき", "i18n": {"zh-cn": {"name": "掏耳"}, "ja-jp": {"name": "耳かき"}}},
					{"id": 999, "name": "独自タグ", "i18n": {"ja-jp": {"name": "独自タグ"}}}
				]
			}`)
		case "/api/workInfo/RJ01000099":
			// Empty age_category_string + nsfw=true => must map to AgeR18
			_, _ = io.WriteString(w, `{
				"id": 1000099,
				"title": "深夜成人サンプル",
				"nsfw": true,
				"age_category_string": "",
				"circle": {"id": 88, "name": "NightCircle", "source_id": "invalid_not_rg"}
			}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mirror2.Close()

	client, err := New(Options{
		BaseURLs:    []string{mirror1.URL, mirror2.URL},
		Locale:      "zh_cn",
		MinInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 1. Mirror failover (503 on mirror1 -> 200 on mirror2) + i18n tag selection + r15 age mapping
	meta, err := client.Fetch(context.Background(), "RJ298293")
	if err != nil {
		t.Fatalf("Fetch RJ298293: %v", err)
	}
	if m1Hits.Load() != 1 || m2Hits.Load() != 1 {
		t.Fatalf("expected 1 hit on mirror1 and 1 hit on mirror2, got m1=%d m2=%d", m1Hits.Load(), m2Hits.Load())
	}
	if meta.Source != "asmrone" || meta.Title != "蓄音レヱル 紅" || meta.CircleID != "RG49556" || meta.CircleName != "RaRo" {
		t.Fatalf("unexpected basic metadata: %+v", meta)
	}
	if meta.AgeCategory != dlsite.AgeR15 {
		t.Fatalf("expected AgeR15 (2) from age_category_string='r15', got %d", meta.AgeCategory)
	}
	if len(meta.Tags) != 2 || meta.Tags[0] != "掏耳" || meta.Tags[1] != "独自タグ" {
		t.Fatalf("unexpected i18n tags: %v", meta.Tags)
	}
	if len(meta.VAs) != 1 || meta.VAs[0] != "黒沢ともよ" || meta.OriginalWorkno != "RJ200001" {
		t.Fatalf("unexpected VAs / OriginalWorkno: %+v", meta)
	}

	// 2. Empty age_category_string + nsfw=true -> AgeR18, and internal circle.id NOT leaked into CircleID
	mAdult, err := client.Fetch(context.Background(), "RJ01000099")
	if err != nil {
		t.Fatalf("Fetch RJ01000099: %v", err)
	}
	if mAdult.AgeCategory != dlsite.AgeR18 || mAdult.CircleID != "" {
		t.Fatalf("expected AgeR18 and empty CircleID, got %+v", mAdult)
	}

	// 3. 404 on mirror1 must immediately return ErrWorkNotFound with ZERO requests to mirror2
	m2Before := m2Hits.Load()
	_, err = client.Fetch(context.Background(), "RJ999404")
	if !errors.Is(err, dlsite.ErrWorkNotFound) {
		t.Fatalf("expected ErrWorkNotFound, got %v", err)
	}
	if m2Hits.Load() != m2Before {
		t.Fatalf("404 must not query subsequent mirrors; m2Hits went %d -> %d", m2Before, m2Hits.Load())
	}
}
