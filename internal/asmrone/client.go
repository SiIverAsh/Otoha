package asmrone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
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
	Transport      http.RoundTripper // optional shared proxy/rate-limited transport
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
	tr := opts.Transport
	if tr == nil {
		var err error
		tr, err = dlsite.BuildHTTPTransport(opts.ProxyURL)
		if err != nil {
			return nil, err
		}
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

// FetchWorksPage retrieves a paginated catalog page from GET /api/works across mirrors.
func (c *Client) FetchWorksPage(ctx context.Context, page, pageSize int, order, sortDir string, subtitleOnly bool) ([]*dlsite.Metadata, PaginationInfo, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 100
	}
	if order == "" {
		order = "id"
	}
	if sortDir == "" {
		sortDir = "asc"
	}

	q := url.Values{}
	q.Set("order", order)
	q.Set("sort", sortDir)
	q.Set("page", strconv.Itoa(page))
	q.Set("pageSize", strconv.Itoa(pageSize))
	if subtitleOnly {
		q.Set("subtitle", "1")
	}
	suffix := "/api/works?" + q.Encode()

	var lastErr error
	for _, base := range c.opts.BaseURLs {
		body, err := c.getRawJSON(ctx, strings.TrimRight(base, "/")+suffix)
		if err != nil {
			if ctx.Err() != nil {
				return nil, PaginationInfo{}, ctx.Err()
			}
			lastErr = err
			continue
		}
		var pageResp worksPageResponse
		if err := json.Unmarshal(body, &pageResp); err != nil {
			lastErr = fmt.Errorf("asmrone: decode works page %d: %w", page, err)
			continue
		}
		items := make([]*dlsite.Metadata, 0, len(pageResp.Works))
		for i := range pageResp.Works {
			item := &pageResp.Works[i]
			wno := dlsite.NormalizeWorkno(item.SourceID)
			if wno == "" && item.ID > 0 {
				wno = dlsite.WorknoFromKikoeruID(item.ID)
			}
			if wno == "" {
				continue
			}
			items = append(items, c.toMetadata(item, wno))
		}
		return items, pageResp.Pagination, nil
	}
	return nil, PaginationInfo{}, fmt.Errorf("asmrone: FetchWorksPage(%d) failed across all mirrors: %w", page, lastErr)
}

// FetchTracks retrieves and flattens GET /api/tracks/{numericID} across mirrors,
// pairing sibling .lrc/.vtt/.srt/.ass subtitle files with audio tracks.
func (c *Client) FetchTracks(ctx context.Context, numericID int) (*TracksBundle, error) {
	if numericID <= 0 {
		return nil, fmt.Errorf("asmrone: invalid numeric work id %d", numericID)
	}
	suffix := fmt.Sprintf("/api/tracks/%d", numericID)

	var lastErr error
	for _, base := range c.opts.BaseURLs {
		body, err := c.getRawJSON(ctx, strings.TrimRight(base, "/")+suffix)
		switch {
		case err == nil:
			var nodes []RemoteNode
			if uErr := json.Unmarshal(body, &nodes); uErr != nil {
				lastErr = uErr
				continue
			}
			return flattenRemoteNodes(nodes), nil
		case errors.Is(err, dlsite.ErrWorkNotFound):
			return nil, dlsite.ErrWorkNotFound
		case ctx.Err() != nil:
			return nil, ctx.Err()
		default:
			lastErr = err
		}
	}
	return nil, fmt.Errorf("asmrone: FetchTracks(%d) failed across all mirrors: %w", numericID, lastErr)
}

func (c *Client) getRawJSON(ctx context.Context, reqURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(600 * time.Millisecond)
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
			lastErr = err
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusGone {
			return nil, dlsite.ErrWorkNotFound
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("HTTP %d for %s", resp.StatusCode, reqURL)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, reqURL)
		}
		if readErr != nil {
			lastErr = readErr
			continue
		}
		return raw, nil
	}
	return nil, lastErr
}

var subExts = []string{".lrc", ".vtt", ".srt", ".ass"}

func isSubtitleTitle(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, se := range subExts {
		if ext == se {
			return true
		}
	}
	return false
}

func flattenRemoteNodes(roots []RemoteNode) *TracksBundle {
	bundle := &TracksBundle{}
	ord := 0

	var walk func(nodes []RemoteNode, prefix string)
	walk = func(nodes []RemoteNode, prefix string) {
		// Index sibling subtitle files in this folder by stem & audio filename
		subByKey := make(map[string]string)
		for _, n := range nodes {
			if n.Type == "folder" || !isSubtitleTitle(n.Title) {
				continue
			}
			u := strings.TrimSpace(n.MediaDownloadURL)
			if u == "" {
				u = strings.TrimSpace(n.MediaStreamURL)
			}
			if u == "" {
				continue
			}
			stem := strings.ToLower(strings.TrimSuffix(n.Title, filepath.Ext(n.Title)))
			if _, exists := subByKey[stem]; !exists {
				subByKey[stem] = u
			}
		}

		for _, n := range nodes {
			rel := n.Title
			if prefix != "" {
				rel = prefix + "/" + n.Title
			}
			switch n.Type {
			case "folder":
				walk(n.Children, rel)
			case "audio":
				ord++
				streamURL := strings.TrimSpace(n.MediaStreamURL)
				if streamURL == "" {
					streamURL = strings.TrimSpace(n.MediaDownloadURL)
				}
				stemLower := strings.ToLower(strings.TrimSuffix(n.Title, filepath.Ext(n.Title)))
				fullLower := strings.ToLower(n.Title)
				subURL := subByKey[stemLower]
				if subURL == "" {
					subURL = subByKey[fullLower]
				}
				bundle.AudioTracks = append(bundle.AudioTracks, RemoteAudioTrack{
					RelPath:      rel,
					Size:         n.Size,
					Duration:     n.Duration,
					Ord:          ord,
					RemoteHash:   n.Hash,
					StreamURL:    streamURL,
					DownloadURL:  strings.TrimSpace(n.MediaDownloadURL),
					StreamLowURL: strings.TrimSpace(n.StreamLowQualityURL),
					SubtitleURL:  subURL,
				})
			default:
				bundle.Attachments = append(bundle.Attachments, RemoteAttachment{
					Type:        n.Type,
					RelPath:     rel,
					Size:        n.Size,
					StreamURL:   strings.TrimSpace(n.MediaStreamURL),
					DownloadURL: strings.TrimSpace(n.MediaDownloadURL),
				})
			}
		}
	}

	walk(roots, "")
	return bundle
}

// i18nKey converts "zh_cn" (DLsite locale style) to "zh-cn" (asmr.one i18n key).
func i18nKey(locale string) string {
	return strings.ReplaceAll(dlsite.FormatLocaleParam(locale), "_", "-")
}

func compactRawJSON(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" || string(trimmed) == "[]" || string(trimmed) == "{}" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, trimmed); err != nil {
		return string(trimmed)
	}
	return buf.String()
}

func inferWorkTypeFromAttrs(attrs string) string {
	for _, token := range strings.Split(attrs, ",") {
		switch strings.TrimSpace(token) {
		case "SOU", "MNG", "GAM", "MOV", "illust", "novel":
			return strings.TrimSpace(token)
		}
	}
	return ""
}

func (c *Client) toMetadata(r *workInfoResponse, wno string) *dlsite.Metadata {
	release := strings.TrimSpace(r.Release)
	if len(release) >= 10 {
		release = release[:10]
	}
	createDate := strings.TrimSpace(r.CreateDate)
	if len(createDate) >= 10 {
		createDate = createDate[:10]
	}
	coverURL := strings.TrimSpace(r.MainCoverURL)
	if coverURL == "" {
		coverURL = strings.TrimSpace(r.SamCoverURL)
	}
	if strings.HasPrefix(coverURL, "//") {
		coverURL = "https:" + coverURL
	}

	m := &dlsite.Metadata{
		Workno:               wno,
		AsmrOneID:            r.ID,
		Title:                strings.TrimSpace(r.Title),
		ReleaseDate:          release,
		CreateDate:           createDate,
		AgeCategory:          mapAgeCategory(r),
		WorkType:             inferWorkTypeFromAttrs(r.WorkAttributes),
		Price:                r.Price,
		Rating:               r.RateAverage2dp,
		RateCount:            r.RateCount,
		ReviewCount:          r.ReviewCount,
		DLCount:              r.DLCount,
		Duration:             r.Duration,
		HasSubtitle:          r.HasSubtitle,
		CoverURL:             coverURL,
		SamCoverURL:          strings.TrimSpace(r.SamCoverURL),
		ThumbnailCoverURL:    strings.TrimSpace(r.ThumbnailCoverURL),
		SourceType:           strings.TrimSpace(r.SourceType),
		SourceURL:            strings.TrimSpace(r.SourceURL),
		WorkAttributes:       strings.TrimSpace(r.WorkAttributes),
		LanguageEditionsJSON: compactRawJSON(r.LanguageEditions),
		OtherEditionsJSON:    compactRawJSON(r.OtherLanguageEditions),
		TranslationInfoJSON:  compactRawJSON(r.TranslationInfo),
		RateDetailJSON:       compactRawJSON(r.RateCountDetail),
		RankJSON:             compactRawJSON(r.Rank),
		OriginalWorkno:       dlsite.NormalizeWorkno(r.OriginalWorkno),
		Locale:               dlsite.FormatLocaleParam(c.opts.Locale),
		Source:               "asmrone",
		FetchedAt:            time.Now().Unix(),
	}

	if r.Series != nil {
		m.SeriesName = strings.TrimSpace(r.Series.Name)
		m.SeriesID = dlsite.ExtractSeriesID(r.Series.SourceID)
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
