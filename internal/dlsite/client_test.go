package dlsite

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientTiersCacheRetryAndResume(t *testing.T) {
	var productHits atomic.Int32
	var ajaxHits atomic.Int32
	var htmlHits atomic.Int32
	var rateLimitAttempts atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/maniax/api/=/product.json", func(w http.ResponseWriter, r *http.Request) {
		productHits.Add(1)
		wno := r.URL.Query().Get("workno")
		locale := r.URL.Query().Get("locale")

		switch wno {
		case "RJ01234567":
			// Translated work in zh_cn has empty [] creaters (PHP quirk!), ja_jp has real VAs
			if locale == "zh_cn" {
				_, _ = io.WriteString(w, `[{
					"workno": "RJ01234567",
					"work_name": "治愈耳搔汉化版",
					"maker_id": "RG54321",
					"maker_name": "音叶社",
					"regist_date": "2025-08-15 00:00:00",
					"age_category": 1,
					"work_type": "SOU",
					"price": 1320,
					"contents_file_size": 524288000,
					"series_id": "SRI0000012345",
					"series_name": "治愈系列",
					"image_main": {"url": "//img.dlsite.jp/modpub/RJ01234567_img_main.jpg"},
					"genres": [{"name": "耳舐め"}, {"name": "バイノーラル"}],
					"creaters": [],
					"translation_info": {"is_translation_agree": true, "original_workno": "RJ01000001"}
				}]`)
				return
			}
			_, _ = io.WriteString(w, `[{"workno":"RJ01234567","work_name":"癒し耳かき","age_category":1,"creaters":{"voice_by":[{"name":"花澤テスト"}]}}]`)
		case "RJ01000001":
			if locale == "zh_cn" {
				// Still empty VAs in zh_cn so client must also test ja_jp fallback!
				_, _ = io.WriteString(w, `[{"workno":"RJ01000001","work_name":"癒し耳かき","maker_id":"RG54321","maker_name":"音葉サークル","age_category":1,"creaters":[]}]`)
				return
			}
			_, _ = io.WriteString(w, `[{
				"workno": "RJ01000001",
				"work_name": "癒し耳かき",
				"maker_id": "RG54321",
				"maker_name": "音葉サークル",
				"age_category": 1,
				"creaters": {
					"voice_by": [{"name": "早見テスト"}, {"name": "上田テスト"}],
					"scenario_by": [{"name": "脚本太郎"}],
					"illust_by": [{"name": "絵師花子"}]
				}
			}]`)
		case "RJ999404":
			// Nonexistent work returns empty array -> permanent 404, no retry!
			_, _ = io.WriteString(w, `[]`)
		case "RJ888429":
			// First 2 attempts return 429, 3rd succeeds
			n := rateLimitAttempts.Add(1)
			if n <= 2 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_, _ = io.WriteString(w, `[{"workno":"RJ888429","work_name":"限流重试作品","maker_id":"RG11111","maker_name":"RetryCircle","regist_date":"2026-01-02 00:00:00","age_category":3,"price":990,"dl_count":"500","rate_average_2dp":4.75,"genres":[{"name":"ASMR"}],"creaters":{"voice_by":[{"name":"CV测试"}]}}]`)
		case "RJ777500":
			// Product JSON broken -> falls back to HTML Tier-3
			w.WriteHeader(http.StatusInternalServerError)
		default:
			_, _ = io.WriteString(w, `[{"workno":"`+wno+`","work_name":"Batch `+wno+`","age_category":1,"price":550,"dl_count":10,"rate_average_2dp":4.5,"genres":[{"name":"全年龄"}],"creaters":{"voice_by":[{"name":"CV1"}]}}]`)
		}
	})

	mux.HandleFunc("/maniax-touch/product/info/ajax", func(w http.ResponseWriter, r *http.Request) {
		ajaxHits.Add(1)
		pid := r.URL.Query().Get("product_id")
		_, _ = fmt.Fprintf(w, `{%q: {"dl_count": "4321", "rate_average_2dp": 4.92, "rate_count": 310, "review_count": 28, "price": 1320}}`, pid)
	})

	mux.HandleFunc("/maniax/work/=/product_id/RJ777500.html", func(w http.ResponseWriter, r *http.Request) {
		htmlHits.Add(1)
		if !strings.Contains(r.Header.Get("Cookie"), "locale=zh-cn") {
			t.Errorf("expected hyphen Cookie locale=zh-cn, got %q", r.Header.Get("Cookie"))
		}
		_, _ = io.WriteString(w, `<!doctype html><html><head>
			<meta property="og:title" content="HTML回退标题 [测试社团] | DLsite 同人">
			<meta property="og:image" content="//img.dlsite.jp/RJ777500.jpg">
		</head><body>
			<span class="maker_name"><a href="https://www.dlsite.com/maniax/circle/profile/=/maker_id/RG99999.html">测试社团</a></span>
			<table id="work_outline"><tbody>
				<tr><th>年龄指定</th><td><span>18禁</span></td></tr>
				<tr><th>贩卖日</th><td>2024年11月20日</td></tr>
				<tr><th>声优</th><td><a href="#">声优A</a> <a href="#">声优B</a></td></tr>
				<tr><th>分类</th><td><div class="main_genre"><a href="/genre/001">治愈</a></div></td></tr>
				<tr><th>文件容量</th><td>1.5 GB</td></tr>
			</tbody></table>
		</body></html>`)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cache, err := NewCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	client, err := NewClient(ClientOptions{
		BaseURL:        srv.URL,
		Locale:         "zh_cn",
		MinInterval:    1 * time.Millisecond,
		MaxRetries:     2,
		RetryBaseDelay: 2 * time.Millisecond,
		Cache:          cache,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// 1. Translated 8-digit work + ja_jp fallback + Tier-2 dynamic ajax
	meta, err := client.Fetch(context.Background(), "RJ01234567", false)
	if err != nil {
		t.Fatalf("Fetch RJ01234567: %v", err)
	}
	if meta.Title != "治愈耳搔汉化版" || meta.AgeCategory != AgeGeneral || meta.DLCount != 4321 || meta.Rating != 4.92 {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	if len(meta.VAs) != 2 || meta.VAs[0] != "早見テスト" {
		t.Fatalf("expected VAs from ja_jp original fallback, got %v", meta.VAs)
	}
	if meta.CoverURL != "https://img.dlsite.jp/modpub/RJ01234567_img_main.jpg" {
		t.Fatalf("unexpected CoverURL: %s", meta.CoverURL)
	}

	// 2. Cache hit must NOT trigger any new HTTP request (§3.3 & §12)
	beforeHits := productHits.Load()
	cachedMeta, err := client.Fetch(context.Background(), "RJ01234567", false)
	if err != nil || cachedMeta.Source != "cache" {
		t.Fatalf("expected cache hit, got meta=%+v err=%v", cachedMeta, err)
	}
	if productHits.Load() != beforeHits {
		t.Fatalf("cache hit still made network requests: %d -> %d", beforeHits, productHits.Load())
	}

	// 3. Permanent 404 must NOT retry and must be cached as permanent not_found
	before404 := productHits.Load()
	_, err = client.Fetch(context.Background(), "RJ999404", false)
	if !errors.Is(err, ErrWorkNotFound) {
		t.Fatalf("expected ErrWorkNotFound, got %v", err)
	}
	if got := productHits.Load() - before404; got != 1 {
		t.Fatalf("404 must not retry; expected 1 request, got %d", got)
	}
	_, err = client.Fetch(context.Background(), "RJ999404", false)
	if !errors.Is(err, ErrWorkNotFound) || productHits.Load() != before404+1 {
		t.Fatalf("cached 404 should not hit server again")
	}

	// 4. 429 exponential backoff retry succeeds on 3rd try
	m429, err := client.Fetch(context.Background(), "RJ888429", false)
	if err != nil || m429.Title != "限流重试作品" || m429.AgeCategory != AgeR18 {
		t.Fatalf("429 retry failed: meta=%+v err=%v", m429, err)
	}

	// 5. HTML Tier-3 fallback with hyphen Cookie
	mHTML, err := client.Fetch(context.Background(), "RJ777500", false)
	if err != nil {
		t.Fatalf("HTML fallback failed: %v", err)
	}
	if mHTML.Title != "HTML回退标题" || mHTML.CircleID != "RG99999" || mHTML.AgeCategory != AgeR18 || mHTML.ReleaseDate != "2024-11-20" || len(mHTML.VAs) != 2 {
		t.Fatalf("unexpected HTML fallback metadata: %+v", mHTML)
	}

	// 6. Interruptible & resumable ScrapeBatch (§3.3 & §12)
	batch := []string{"RJ100001", "RJ100002", "RJ100003", "RJ100004"}
	ctxCancel, cancel := context.WithCancel(context.Background())
	seen := 0
	_, err = client.ScrapeBatch(ctxCancel, batch, false, func(p BatchProgress) {
		seen++
		if seen == 2 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	hitsAfterCancel := productHits.Load()

	// Resume: RJ100001 and RJ100002 must come from checkpoint/cache, only RJ100003 & RJ100004 fetched!
	cp, err := client.ScrapeBatch(context.Background(), batch, false, nil)
	if err != nil || !cp.Finished {
		t.Fatalf("resume failed: cp=%+v err=%v", cp, err)
	}
	if delta := productHits.Load() - hitsAfterCancel; delta != 2 {
		t.Fatalf("resume should only fetch 2 remaining items, got %d requests", delta)
	}
}

func TestHTTPProxyWithBasicAuth(t *testing.T) {
	var sawProxyAuth atomic.Bool
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Proxy-Authorization")
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:secret123"))
		if auth == want {
			sawProxyAuth.Store(true)
		} else {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		_, _ = io.WriteString(w, `[{"workno":"RJ123456","work_name":"代理测试","age_category":1,"price":100,"dl_count":1,"rate_average_2dp":5.0,"genres":[{"name":"测试"}],"creaters":{"voice_by":[{"name":"CV"}]}}]`)
	}))
	defer proxySrv.Close()

	proxyURL := strings.Replace(proxySrv.URL, "http://", "http://alice:secret123@", 1)
	client, err := NewClient(ClientOptions{
		BaseURL:     "http://dlsite.example.invalid",
		ProxyURL:    proxyURL,
		MinInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClient with proxy: %v", err)
	}
	meta, err := client.Fetch(context.Background(), "RJ123456", true)
	if err != nil || meta.Title != "代理测试" || !sawProxyAuth.Load() {
		t.Fatalf("authenticated HTTP proxy failed: meta=%+v err=%v sawAuth=%v", meta, err, sawProxyAuth.Load())
	}
}
