package dlsite

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	xproxy "golang.org/x/net/proxy"
)

var (
	// ErrWorkNotFound indicates DLsite returned 404 or empty product array (permanent failure, no retry).
	ErrWorkNotFound = errors.New("dlsite: work not found (404)")
)

// MetadataFallback is an optional Tier-4 source consulted when DLsite returns 404,
// is unreachable, or leaves critical fields empty. Implemented by internal/asmrone.Client.
// Declared here (not in asmrone) to keep the dependency one-directional.
type MetadataFallback interface {
	Fetch(ctx context.Context, workno string) (*Metadata, error)
}

// ClientOptions configures the DLsite metadata client (§3.2, §3.3, §9).
type ClientOptions struct {
	BaseURL          string
	Locale           string
	UserAgent        string
	ProxyURL         string
	MinInterval      time.Duration
	RequestTimeout   time.Duration
	MaxRetries       int
	RetryBaseDelay   time.Duration
	Cache            *Cache
	CoversDir        string
	Fallback         MetadataFallback
	Transport        http.RoundTripper // shared transport for aggregate request limits
	CompleteMetadata bool              // consult all configured metadata sources
}

// Client fetches public DLsite metadata serially with rate limiting, proxy auth, and disk caching.
type Client struct {
	opts       ClientOptions
	httpClient *http.Client
	rateMu     sync.Mutex
	lastReqAt  time.Time
}

// BuildHTTPTransport builds an *http.Transport supporting HTTP/HTTPS and SOCKS5 proxies
// WITH username/password authentication (§9).
func BuildHTTPTransport(proxyStr string) (*http.Transport, error) {
	tr := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		MaxIdleConns:          10,
		IdleConnTimeout:       60 * time.Second,
	}
	proxyStr = strings.TrimSpace(proxyStr)
	if proxyStr == "" {
		return tr, nil
	}
	u, err := url.Parse(proxyStr)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL %q: %w", proxyStr, err)
	}

	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		tr.Proxy = http.ProxyURL(u)
		if u.User != nil {
			pwd, _ := u.User.Password()
			basic := base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + pwd))
			tr.ProxyConnectHeader = http.Header{
				"Proxy-Authorization": []string{"Basic " + basic},
			}
		}
	case "socks5", "socks5h":
		var auth *xproxy.Auth
		if u.User != nil {
			pwd, _ := u.User.Password()
			auth = &xproxy.Auth{
				User:     u.User.Username(),
				Password: pwd,
			}
		}
		dialer, err := xproxy.SOCKS5("tcp", u.Host, auth, &net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		})
		if err != nil {
			return nil, fmt.Errorf("configure socks5 proxy: %w", err)
		}
		if ctxDialer, ok := dialer.(xproxy.ContextDialer); ok {
			tr.DialContext = ctxDialer.DialContext
		} else {
			tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (use http, https, or socks5)", u.Scheme)
	}
	return tr, nil
}

func NewClient(opts ClientOptions) (*Client, error) {
	if opts.BaseURL == "" {
		opts.BaseURL = "https://www.dlsite.com"
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	if opts.Locale == "" {
		opts.Locale = "zh_cn"
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "Mozilla/5.0 (compatible; Otoha/1.0; +https://github.com/SiIverAsh/Otoha)"
	}
	if opts.MinInterval == 0 {
		opts.MinInterval = 3 * time.Second
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = 20 * time.Second
	}
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = 3
	}
	if opts.RetryBaseDelay <= 0 {
		opts.RetryBaseDelay = 1 * time.Second
	}
	tr := opts.Transport
	if tr == nil {
		var err error
		tr, err = BuildHTTPTransport(opts.ProxyURL)
		if err != nil {
			return nil, err
		}
	}
	return &Client{
		opts: opts,
		httpClient: &http.Client{
			Transport: tr,
			Timeout:   opts.RequestTimeout,
		},
	}, nil
}

func (c *Client) waitRateLimit(ctx context.Context) error {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()

	if !c.lastReqAt.IsZero() && c.opts.MinInterval > 0 {
		elapsed := time.Since(c.lastReqAt)
		if wait := c.opts.MinInterval - elapsed; wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	c.lastReqAt = time.Now()
	return nil
}

type httpStatusError struct {
	StatusCode int
	URL        string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d for %s", e.StatusCode, e.URL)
}

func (c *Client) getBytes(ctx context.Context, rawURL string, cookie string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.opts.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := c.opts.RetryBaseDelay * time.Duration(1<<(attempt-1))
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		if err := c.waitRateLimit(ctx); err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.opts.UserAgent)
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,ja;q=0.8,en;q=0.7")
		if strings.Contains(strings.ToLower(rawURL), "asmr") {
			req.Header.Set("Referer", "https://asmr.one/")
			req.Header.Set("Origin", "https://asmr.one")
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = err
			continue
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			return nil, ErrWorkNotFound
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = &httpStatusError{StatusCode: resp.StatusCode, URL: rawURL}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, &httpStatusError{StatusCode: resp.StatusCode, URL: rawURL}
		}
		if readErr != nil {
			lastErr = readErr
			continue
		}
		return body, nil
	}
	return nil, fmt.Errorf("request failed after retries (%s): %w", rawURL, lastErr)
}

func (c *Client) fetchProductJSON(ctx context.Context, workno, localeParam string) (*Metadata, error) {
	u := fmt.Sprintf("%s/maniax/api/=/product.json?workno=%s&locale=%s", c.opts.BaseURL, url.QueryEscape(workno), url.QueryEscape(localeParam))
	body, err := c.getBytes(ctx, u, "")
	if err != nil {
		return nil, err
	}
	return ParseProductJSON(body, workno)
}

// Fetch retrieves metadata for workno using disk cache first (unless force=true),
// followed by Tier-1 product.json (+ translation_info & ja_jp fallback), Tier-2 info/ajax,
// and Tier-3 HTML parsing (§3.2, §3.3, §13).
func (c *Client) Fetch(ctx context.Context, workno string, force bool) (*Metadata, error) {
	wno := NormalizeWorkno(workno)
	if wno == "" {
		return nil, fmt.Errorf("invalid workno: %q", workno)
	}

	if !force && c.opts.Cache != nil {
		if cached, ok := c.opts.Cache.Get(wno); ok {
			if cached.NotFound {
				return nil, ErrWorkNotFound
			}
			cp := *cached
			cp.Source = "cache"
			return &cp, nil
		}
	}

	localeParam := FormatLocaleParam(c.opts.Locale)
	meta, err := c.fetchProductJSON(ctx, wno, localeParam)
	if err != nil && ctx.Err() == nil {
		// Some live products are absent from product.json. Consult the product
		// page before treating an empty JSON response as a missing work.
		htmlMeta, htmlErr := c.fetchHTML(ctx, wno, c.opts.Locale)
		if htmlErr == nil {
			meta = htmlMeta
			err = nil
		}
	}
	if errors.Is(err, ErrWorkNotFound) {
		// Tier-4: DLsite has no record (delisted / exclusive) — try asmr.one before
		// writing the permanent not-found marker, which would block all future rescans.
		if fb := c.tryFallback(ctx, wno); fb != nil {
			fb.FetchedAt = time.Now().Unix()
			if c.opts.Cache != nil {
				_ = c.opts.Cache.Put(fb)
			}
			return fb, nil
		}
		if c.opts.Cache != nil {
			_ = c.opts.Cache.PutNotFound(wno)
		}
		return nil, ErrWorkNotFound
	}
	if err != nil {
		// If DLsite itself is unreachable / 5xx, also consult Tier-4 fallback without marking 404.
		if fb := c.tryFallback(ctx, wno); fb != nil {
			fb.FetchedAt = time.Now().Unix()
			if c.opts.Cache != nil {
				_ = c.opts.Cache.Put(fb)
			}
			return fb, nil
		}
		return nil, err
	}

	meta.Locale = localeParam

	// Translated editions can omit shared voice and series credits (§13).
	if meta.OriginalWorkno != "" && (len(meta.VAs) == 0 || meta.SeriesName == "") {
		if origMeta, origErr := c.fetchProductJSON(ctx, meta.OriginalWorkno, localeParam); origErr == nil {
			MergeFallbackMetadata(meta, origMeta)
		}
	}

	// Non-Japanese locale fallback (§13: re-fetch ja_jp if VAs are still missing)
	if localeParam != "ja_jp" && len(meta.VAs) == 0 {
		target := wno
		if meta.OriginalWorkno != "" {
			target = meta.OriginalWorkno
		}
		if jaMeta, jaErr := c.fetchProductJSON(ctx, target, "ja_jp"); jaErr == nil {
			MergeFallbackMetadata(meta, jaMeta)
		}
	}

	// Tier-2: Dynamic metadata (dl_count, rating, price) from maniax-touch/product/info/ajax
	if meta.DLCount == 0 || meta.Rating == 0 || meta.Price == 0 {
		ajaxURL := fmt.Sprintf("%s/maniax-touch/product/info/ajax?product_id=%s", c.opts.BaseURL, url.QueryEscape(wno))
		if ajaxBody, ajaxErr := c.getBytes(ctx, ajaxURL, ""); ajaxErr == nil {
			_ = ApplyAjaxDynamic(meta, ajaxBody, wno)
		}
	}

	// Tier-3: JSON can contain credits while omitting the release date or other
	// static fields. Those omissions also require checking the product page.
	if missingStaticMetadata(meta) {
		if htmlMeta, htmlErr := c.fetchHTML(ctx, wno, c.opts.Locale); htmlErr == nil {
			if meta.Title == "" {
				meta.Title = htmlMeta.Title
			}
			MergeFallbackMetadata(meta, htmlMeta)
		}
	}

	// Tier-4: asmr.one fills gaps DLsite left (common for delisted or translated works).
	if c.opts.CompleteMetadata || missingStaticMetadata(meta) || len(meta.VAs) == 0 || len(meta.Tags) == 0 {
		if fb := c.tryFallback(ctx, wno); fb != nil {
			if meta.Title == "" {
				meta.Title = fb.Title
			}
			MergeFallbackMetadata(meta, fb)
			meta.Source = strings.TrimPrefix(meta.Source+"+asmrone", "+")
		}
	}

	meta.FetchedAt = time.Now().Unix()
	if c.opts.Cache != nil {
		_ = c.opts.Cache.Put(meta)
	}
	return meta, nil
}

func missingStaticMetadata(meta *Metadata) bool {
	return meta.Title == "" || meta.ReleaseDate == "" || meta.CircleID == "" ||
		meta.CircleName == "" || meta.WorkType == "" || meta.AgeCategory == AgeUnknown ||
		meta.CoverURL == "" || meta.FileSize == 0 || (len(meta.VAs) == 0 && len(meta.Tags) == 0)
}

func (c *Client) tryFallback(ctx context.Context, wno string) *Metadata {
	if c.opts.Fallback == nil || ctx.Err() != nil {
		return nil
	}
	fb, err := c.opts.Fallback.Fetch(ctx, wno)
	if err != nil || fb == nil || fb.Title == "" || NormalizeWorkno(fb.Workno) != wno {
		return nil
	}
	return fb
}

func (c *Client) fetchHTML(ctx context.Context, workno, locale string) (*Metadata, error) {
	body, err := c.getWorkHTML(ctx, workno, locale)
	if err != nil {
		return nil, err
	}
	return ParseWorkHTML(body, workno)
}

func (c *Client) getWorkHTML(ctx context.Context, workno, locale string) ([]byte, error) {
	u := fmt.Sprintf("%s/maniax/work/=/product_id/%s.html", c.opts.BaseURL, url.QueryEscape(workno))
	return c.getBytes(ctx, u, FormatLocaleCookie(locale))
}

// DownloadCover downloads coverURL to <CoversDir>/<workno>.jpg if not already downloaded.
func (c *Client) DownloadCover(ctx context.Context, workno, coverURL string) (string, error) {
	wno := NormalizeWorkno(workno)
	if wno == "" || coverURL == "" || c.opts.CoversDir == "" {
		return "", nil
	}
	if err := os.MkdirAll(c.opts.CoversDir, 0o755); err != nil {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(strings.Split(coverURL, "?")[0]))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".webp" {
		ext = ".jpg"
	}
	dst := filepath.Join(c.opts.CoversDir, wno+ext)
	if st, err := os.Stat(dst); err == nil && st.Size() > 0 {
		return dst, nil
	}
	body, err := c.getBytes(ctx, coverURL, "")
	if err != nil {
		return "", err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// BatchProgress reports batch scrape progress for CLI and SSE listeners.
type BatchProgress struct {
	Current int
	Total   int
	Workno  string
	Status  string // "cached" | "ok" | "not_found" | "error"
	Meta    *Metadata
	Err     error
}

// ScrapeBatch scrapes a list of worknos serially with checkpointing so interruption (ctx cancel)
// can be resumed cleanly without re-fetching completed items (§3.3 & §12).
func (c *Client) ScrapeBatch(ctx context.Context, worknos []string, force bool, onProgress func(BatchProgress)) (*ScrapeCheckpoint, error) {
	var cp *ScrapeCheckpoint
	if !force && c.opts.Cache != nil {
		cp, _ = c.opts.Cache.LoadCheckpoint()
	}
	if cp == nil || cp.Finished || force {
		cp = &ScrapeCheckpoint{
			Queue:  make([]string, 0, len(worknos)),
			States: make(map[string]string, len(worknos)),
		}
		seen := make(map[string]struct{}, len(worknos))
		for _, w := range worknos {
			wno := NormalizeWorkno(w)
			if wno == "" {
				continue
			}
			if _, ok := seen[wno]; ok {
				continue
			}
			seen[wno] = struct{}{}
			cp.Queue = append(cp.Queue, wno)
		}
	} else {
		// Append any newly discovered worknos not yet in checkpoint queue
		inQueue := make(map[string]struct{}, len(cp.Queue))
		for _, q := range cp.Queue {
			inQueue[q] = struct{}{}
		}
		for _, w := range worknos {
			wno := NormalizeWorkno(w)
			if wno == "" {
				continue
			}
			if _, ok := inQueue[wno]; !ok {
				inQueue[wno] = struct{}{}
				cp.Queue = append(cp.Queue, wno)
			}
		}
	}

	total := len(cp.Queue)
	for i := 0; i < total; i++ {
		if err := ctx.Err(); err != nil {
			cp.NextIndex = i
			if c.opts.Cache != nil {
				_ = c.opts.Cache.SaveCheckpoint(cp)
			}
			return cp, err
		}
		wno := cp.Queue[i]
		if !force {
			if st, done := cp.States[wno]; done && (st == "ok" || st == "not_found") {
				if onProgress != nil {
					var cached *Metadata
					if c.opts.Cache != nil {
						cached, _ = c.opts.Cache.Get(wno)
					}
					onProgress(BatchProgress{Current: i + 1, Total: total, Workno: wno, Status: "cached", Meta: cached})
				}
				continue
			}
		}

		meta, err := c.Fetch(ctx, wno, force)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
			cp.NextIndex = i
			if c.opts.Cache != nil {
				_ = c.opts.Cache.SaveCheckpoint(cp)
			}
			return cp, ctx.Err()
		}
		if errors.Is(err, ErrWorkNotFound) {
			cp.States[wno] = "not_found"
			cp.NextIndex = i + 1
			if c.opts.Cache != nil {
				_ = c.opts.Cache.SaveCheckpoint(cp)
			}
			if onProgress != nil {
				onProgress(BatchProgress{Current: i + 1, Total: total, Workno: wno, Status: "not_found", Err: err})
			}
			continue
		}
		if err != nil {
			cp.States[wno] = "error:" + err.Error()
			cp.NextIndex = i + 1
			if c.opts.Cache != nil {
				_ = c.opts.Cache.SaveCheckpoint(cp)
			}
			if onProgress != nil {
				onProgress(BatchProgress{Current: i + 1, Total: total, Workno: wno, Status: "error", Err: err})
			}
			continue
		}

		cp.States[wno] = "ok"
		cp.NextIndex = i + 1
		if c.opts.Cache != nil {
			_ = c.opts.Cache.SaveCheckpoint(cp)
		}
		status := "ok"
		if meta.Source == "cache" {
			status = "cached"
		}
		if onProgress != nil {
			onProgress(BatchProgress{Current: i + 1, Total: total, Workno: wno, Status: status, Meta: meta})
		}
	}

	cp.Finished = true
	if c.opts.Cache != nil {
		_ = c.opts.Cache.SaveCheckpoint(cp)
	}
	return cp, nil
}
