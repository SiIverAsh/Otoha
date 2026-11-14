package asmrone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SiIverAsh/otoha/internal/dlsite"
)

// DefaultBaseURLs lists the public asmr.one API mirrors in failover order.
var DefaultBaseURLs = []string{
	"https://api.asmr-200.com",
	"https://api.asmr.one",
	"https://api.asmr-100.com",
	"https://api.asmr-300.com",
}

type Options struct {
	BaseURLs       []string
	Locale         string // reuses cfg.ScraperLocale ("zh_cn")
	UserAgent      string
	ProxyURL       string
	MinInterval    time.Duration
	RequestTimeout time.Duration
}

type Client struct {
	opts       Options
	httpClient *http.Client
	rateMu     sync.Mutex
	lastReqAt  time.Time
}

func New(opts Options) (*Client, error) {
	if len(opts.BaseURLs) == 0 {
		opts.BaseURLs = DefaultBaseURLs
	}
	if opts.Locale == "" {
		opts.Locale = "zh_cn"
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "Mozilla/5.0 (compatible; Otoha/1.0; +https://github.com/SiIverAsh/Otoha)"
	}
	if opts.MinInterval <= 0 {
		opts.MinInterval = 500 * time.Millisecond
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = 20 * time.Second
	}
	tr, err := dlsite.BuildHTTPTransport(opts.ProxyURL)
	if err != nil {
		return nil, err
	}
	return &Client{
		opts:       opts,
		httpClient: &http.Client{Transport: tr, Timeout: opts.RequestTimeout},
	}, nil
}

func (c *Client) waitRateLimit(ctx context.Context) error {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	if !c.lastReqAt.IsZero() {
		if wait := c.opts.MinInterval - time.Since(c.lastReqAt); wait > 0 {
			t := time.NewTimer(wait)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
			}
		}
	}
	c.lastReqAt = time.Now()
	return nil
}

// Fetch implements dlsite.MetadataFallback across all configured mirrors.
func (c *Client) Fetch(ctx context.Context, workno string) (*dlsite.Metadata, error) {
	wno := dlsite.NormalizeWorkno(workno)
	if wno == "" {
		return nil, fmt.Errorf("asmrone: invalid workno %q", workno)
	}

	var lastErr error
	for _, base := range c.opts.BaseURLs {
		raw, err := c.getWorkInfo(ctx, strings.TrimRight(base, "/"), wno)
		switch {
		case err == nil:
			return c.toMetadata(raw, wno), nil
		case errors.Is(err, dlsite.ErrWorkNotFound):
			return nil, dlsite.ErrWorkNotFound // 404/400: work absent across mirrors, stop immediately
		case ctx.Err() != nil:
			return nil, ctx.Err()
		default:
			lastErr = err // network/timeout/5xx/429: failover to next mirror
		}
	}
	return nil, fmt.Errorf("asmrone: all mirrors failed: %w", lastErr)
}

func (c *Client) getWorkInfo(ctx context.Context, base, wno string) (*workInfoResponse, error) {
	if err := c.waitRateLimit(ctx); err != nil {
		return nil, err
	}
	reqURL := base + "/api/workInfo/" + wno

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.opts.UserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://asmr.one/")
	req.Header.Set("Origin", "https://asmr.one")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound,
		resp.StatusCode == http.StatusBadRequest,
		resp.StatusCode == http.StatusGone:
		return nil, dlsite.ErrWorkNotFound
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("asmrone: HTTP %d for %s", resp.StatusCode, reqURL)
	}

	var out workInfoResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("asmrone: decode %s: %w", reqURL, err)
	}
	if strings.TrimSpace(out.Title) == "" && out.ID == 0 {
		return nil, dlsite.ErrWorkNotFound
	}
	return &out, nil
}

// i18nKey converts "zh_cn" (DLsite locale style) to "zh-cn" (asmr.one i18n key).
func i18nKey(locale string) string {
	return strings.ReplaceAll(dlsite.FormatLocaleParam(locale), "_", "-")
}

func (c *Client) toMetadata(r *workInfoResponse, wno string) *dlsite.Metadata {
	release := strings.TrimSpace(r.Release)
	if len(release) >= 10 {
		release = release[:10]
	}
	coverURL := strings.TrimSpace(r.MainCoverURL)
	if coverURL == "" {
		coverURL = strings.TrimSpace(r.SamCoverURL)
	}
	if strings.HasPrefix(coverURL, "//") {
		coverURL = "https:" + coverURL
	}

	m := &dlsite.Metadata{
		Workno:         wno,
		Title:          strings.TrimSpace(r.Title),
		ReleaseDate:    release,
		AgeCategory:    mapAgeCategory(r),
		Price:          r.Price,
		Rating:         r.RateAverage2dp,
		RateCount:      r.RateCount,
		ReviewCount:    r.ReviewCount,
		DLCount:        r.DLCount,
		CoverURL:       coverURL,
		OriginalWorkno: dlsite.NormalizeWorkno(r.OriginalWorkno),
		Locale:         dlsite.FormatLocaleParam(c.opts.Locale),
		Source:         "asmrone",
		FetchedAt:      time.Now().Unix(),
	}

	if r.Circle != nil {
		m.CircleName = strings.TrimSpace(r.Circle.Name)
		// source_id is the DLsite RG code; circle.id is an internal autoincrement — never use it.
		m.CircleID = dlsite.ExtractCircleID(r.Circle.SourceID)
	} else if strings.TrimSpace(r.Name) != "" {
		m.CircleName = strings.TrimSpace(r.Name)
	}

	key := i18nKey(c.opts.Locale)
	seenTags := make(map[string]struct{}, len(r.Tags))
	for _, t := range r.Tags {
		name := t.Name
		if loc, ok := t.I18n[key]; ok && strings.TrimSpace(loc.Name) != "" {
			name = loc.Name
		}
		if name = strings.TrimSpace(name); name != "" {
			if _, exists := seenTags[name]; !exists {
				seenTags[name] = struct{}{}
				m.Tags = append(m.Tags, name)
			}
		}
	}

	seenVAs := make(map[string]struct{}, len(r.VAs))
	for _, v := range r.VAs {
		if name := strings.TrimSpace(v.Name); name != "" && !strings.EqualFold(name, "N/A") {
			if _, exists := seenVAs[name]; !exists {
				seenVAs[name] = struct{}{}
				m.VAs = append(m.VAs, name)
			}
		}
	}
	return m
}

func mapAgeCategory(r *workInfoResponse) int {
	switch strings.ToLower(strings.TrimSpace(r.AgeCategoryString)) {
	case "adult", "r18":
		return dlsite.AgeR18
	case "r15":
		return dlsite.AgeR15
	case "general", "all_ages":
		return dlsite.AgeGeneral
	}
	// Fallback boolean: prefer stricter R18 when nsfw=true so SFW default filter stays safe.
	if r.Nsfw {
		return dlsite.AgeR18
	}
	return dlsite.AgeGeneral
}
