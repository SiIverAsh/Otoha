// Package asmr18 reads public work pages; it never evaluates page JavaScript.
package asmr18

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/SiIverAsh/otoha/internal/dlsite"
)

const (
	BaseURL         = "https://asmr18.fans"
	DefaultInterval = 500 * time.Millisecond
)

// ErrChallenge means the site returned an access challenge instead of work data.
var ErrChallenge = errors.New("ASMR18 access challenge")

// HTTPError preserves upstream status and retry guidance for the sync scheduler.
type HTTPError struct {
	StatusCode int
	RetryAfter string
}

func (e *HTTPError) Error() string {
	if e.RetryAfter != "" {
		return fmt.Sprintf("ASMR18 HTTP %d (Retry-After: %s)", e.StatusCode, e.RetryAfter)
	}
	return fmt.Sprintf("ASMR18 HTTP %d", e.StatusCode)
}

type FetchOptions struct {
	ProbeDuration bool
}

type Chapter struct {
	Title string  `json:"title"`
	Start float64 `json:"start"`
}

type Track struct {
	Key      string    `json:"key"`
	Title    string    `json:"title"`
	URL      string    `json:"url"`
	Chapters []Chapter `json:"chapters"`
	Duration float64   `json:"duration"`
}

type Work struct {
	Metadata dlsite.Metadata `json:"metadata"`
	Tracks   []Track         `json:"tracks"`
}

type Client struct {
	http     *http.Client
	interval time.Duration
	rateMu   sync.Mutex
	last     time.Time
}

func New(proxy string, interval time.Duration) (*Client, error) {
	tr, err := dlsite.BuildHTTPTransport(proxy)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || (req.URL.Host != "asmr18.fans" && !ValidMediaURL(req.URL.String())) {
			return fmt.Errorf("unsupported ASMR18 redirect")
		}
		return nil
	}}
	return &Client{http: client, interval: interval}, nil
}

var workPath = regexp.MustCompile(`^/(boys|girls|allages)/(rj(?:[0-9]{8}|[0-9]{6}))/\z`)

func PageURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "asmr18.fans" || u.User != nil || !workPath.MatchString(strings.ToLower(u.Path)) {
		return "", fmt.Errorf("expected https://asmr18.fans/{boys,girls,allages}/rj…/")
	}
	u.Path = strings.ToLower(u.Path)
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// waitRateLimit gates request starts globally across all callers, including List
// and optional CDN probes. Waiting does not reserve future slots, so cancellations
// cannot accumulate a burst of requests when the client becomes idle again.
func (c *Client) waitRateLimit(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.rateMu.Lock()
		delay := c.interval - time.Since(c.last)
		if delay <= 0 {
			c.last = time.Now()
			c.rateMu.Unlock()
			return nil
		}
		c.rateMu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) get(ctx context.Context, raw string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Otoha/1.0)")
	req.Header.Set("Referer", BaseURL+"/")
	if err := c.waitRateLimit(ctx); err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, &HTTPError{StatusCode: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if len(b) > 4<<20 {
		return nil, fmt.Errorf("ASMR18 page too large")
	}
	if err == nil {
		preview := b
		if len(preview) > 8192 {
			preview = preview[:8192]
		}
		lower := strings.ToLower(string(preview))
		if strings.EqualFold(resp.Header.Get("CF-Mitigated"), "challenge") ||
			strings.Contains(lower, "<title>just a moment") ||
			strings.Contains(lower, "/cdn-cgi/challenge-platform/") {
			return nil, ErrChallenge
		}
	}
	return b, err
}

// Fetch preserves the original behavior of validating manifests and probing
// duration. Bulk callers can omit that request with FetchWithOptions.
func (c *Client) Fetch(ctx context.Context, raw string) (*Work, error) {
	return c.FetchWithOptions(ctx, raw, FetchOptions{ProbeDuration: true})
}

func (c *Client) FetchWithOptions(ctx context.Context, raw string, opts FetchOptions) (*Work, error) {
	u, err := PageURL(raw)
	if err != nil {
		return nil, err
	}
	b, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	w, err := Parse(u, string(b))
	if err != nil {
		return nil, err
	}
	if !opts.ProbeDuration {
		return w, nil
	}
	for i := range w.Tracks {
		duration, err := c.ProbeMediaDuration(ctx, w.Tracks[i].URL)
		if err != nil {
			return nil, err
		}
		w.Tracks[i].Duration = duration
	}
	return w, nil
}

// ProbeMediaDuration reads the timing entries of a complete ASMR18 HLS
// media playlist. A master or unfinished playlist returns zero.
func (c *Client) ProbeMediaDuration(ctx context.Context, mediaURL string) (float64, error) {
	if !ValidMediaURL(mediaURL) {
		return 0, fmt.Errorf("unsupported ASMR18 HLS URL")
	}
	manifest, err := c.get(ctx, mediaURL)
	if err != nil {
		return 0, fmt.Errorf("read HLS manifest: %w", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(manifest)), "#EXTM3U") {
		return 0, fmt.Errorf("invalid HLS manifest")
	}
	var duration float64
	var isComplete bool
	for line := range strings.SplitSeq(string(manifest), "\n") {
		line = strings.TrimSpace(line)
		if line == "#EXT-X-ENDLIST" {
			isComplete = true
		} else if value, isDuration := strings.CutPrefix(line, "#EXTINF:"); isDuration {
			seconds, _, _ := strings.Cut(value, ",")
			if segment, err := strconv.ParseFloat(seconds, 64); err == nil && segment > 0 && !math.IsInf(segment, 0) {
				duration += segment
			}
		}
	}
	if !isComplete {
		return 0, nil
	}
	return duration, nil
}

// List returns unique RJ work pages from one category page, following only its primary list.
func (c *Client) List(ctx context.Context, category string, page int) ([]string, bool, error) {
	if category != "boys" && category != "girls" && category != "allages" {
		return nil, false, fmt.Errorf("invalid ASMR18 category")
	}
	if page < 1 {
		return nil, false, fmt.Errorf("invalid page")
	}
	u := BaseURL + "/" + category + "/"
	if page > 1 {
		u += fmt.Sprintf("page/%d/", page)
	}
	b, err := c.get(ctx, u)
	if err != nil {
		return nil, false, err
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(b)))
	if err != nil {
		return nil, false, err
	}
	seen := map[string]bool{}
	var out []string
	doc.Find("#main a[href]").Each(func(_ int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		canonical, e := PageURL(href)
		if e == nil && strings.HasPrefix(canonical, BaseURL+"/"+category+"/") && !seen[canonical] {
			seen[canonical] = true
			out = append(out, canonical)
		}
	})
	return out, doc.Find("link[rel='next'], a.nextpostslink").Length() > 0, nil
}

var packed = regexp.MustCompile(`(?s)\}\('(.*?)',(\d+),(\d+),'(.*?)'\.split\('\|'\),0,\{\}\)\)`)
var word = regexp.MustCompile(`\b[0-9a-zA-Z]+\b`)
var mediaURL = regexp.MustCompile(`https?://[^\s'"\\<>]+\.m3u8(?:\?[^\s'"\\<>]*)?`)

func unpack(s string) string {
	return packed.ReplaceAllStringFunc(s, func(part string) string {
		m := packed.FindStringSubmatch(part)
		base, _ := strconv.Atoi(m[2])
		if base < 2 || base > 62 {
			return ""
		}
		words := strings.Split(m[4], "|")
		keys := map[string]string{}
		const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
		var encode func(int) string
		encode = func(n int) string {
			p := ""
			if n >= base {
				p = encode(n / base)
			}
			return p + string(alphabet[n%base])
		}
		for i, w := range words {
			if w != "" {
				keys[encode(i)] = w
			}
		}
		return word.ReplaceAllStringFunc(m[1], func(w string) string {
			if v, ok := keys[w]; ok {
				return v
			}
			return w
		})
	})
}

// ValidMediaURL keeps imported playback sources on the site's public CDN.
func ValidMediaURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.User == nil && u.Port() == "" && regexp.MustCompile(`^cdn[0-9]+\.cloudintech\.net$`).MatchString(u.Hostname()) && strings.HasPrefix(u.Path, "/file/") && strings.HasSuffix(u.Path, ".m3u8")
}

func Parse(raw, html string) (*Work, error) {
	canonical, err := PageURL(raw)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(canonical)
	parts := workPath.FindStringSubmatch(u.Path)
	wno := dlsite.NormalizeWorkno(parts[2])
	if wno == "" {
		return nil, fmt.Errorf("invalid RJ")
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, err
	}
	m := dlsite.Metadata{Workno: wno, Title: strings.TrimSpace(doc.Find("h1").First().Text()), Source: "asmr18", FetchedAt: time.Now().Unix()}
	if m.Title == "" {
		return nil, fmt.Errorf("ASMR18 missing title")
	}
	if parts[1] == "allages" {
		m.AgeCategory = dlsite.AgeGeneral
	} else {
		m.AgeCategory = dlsite.AgeR18
	}
	m.CoverURL, _ = doc.Find("video[poster]").First().Attr("poster")
	doc.Find("#post-tag a[href]").Each(func(_ int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		p, e := url.Parse(href)
		if e != nil {
			return
		}
		name := strings.TrimSpace(s.Text())
		if name == "" {
			return
		}
		switch strings.Split(strings.Trim(p.Path, "/"), "/")[0] {
		case "cv":
			m.VAs = append(m.VAs, name)
		case "circle":
			m.CircleName = name
		case "scenario":
			m.Scenarios = append(m.Scenarios, name)
		case "illustrator":
			m.Illusts = append(m.Illusts, name)
		case "music":
			m.Musics = append(m.Musics, name)
		case "series":
			m.SeriesName = name
		case "genre":
			m.Tags = append(m.Tags, name)
		}
	})
	result := &Work{Metadata: m}
	seen := map[string]bool{}
	// Each file tab contains its own player and chapter offsets.
	containers := doc.Find(".tab_content")
	if containers.Length() == 0 {
		containers = doc.Selection
	}
	containers.Each(func(i int, s *goquery.Selection) {
		body, _ := s.Html()
		decoded := unpack(body)
		var urls []string
		for _, v := range mediaURL.FindAllString(decoded, -1) {
			if ValidMediaURL(v) && !seen[v] {
				seen[v] = true
				urls = append(urls, v)
			}
		}
		var chapters []Chapter
		s.Find("#chapter a[data-value]").Each(func(_ int, a *goquery.Selection) {
			v, _ := a.Attr("data-value")
			start, e := strconv.ParseFloat(v, 64)
			if e != nil || start < 0 || math.IsNaN(start) || math.IsInf(start, 0) {
				return
			}
			title := a.Clone()
			title.Find("span").Remove()
			chapters = append(chapters, Chapter{Title: strings.TrimSpace(title.Text()), Start: start})
		})
		// Some pages place chapters immediately outside the sole file tab.
		if containers.Length() == 1 && len(chapters) == 0 {
			doc.Find("#chapter a[data-value]").Each(func(_ int, a *goquery.Selection) {
				v, _ := a.Attr("data-value")
				start, e := strconv.ParseFloat(v, 64)
				if e == nil && start >= 0 && !math.IsInf(start, 0) {
					title := a.Clone()
					title.Find("span").Remove()
					chapters = append(chapters, Chapter{Title: strings.TrimSpace(title.Text()), Start: start})
				}
			})
		}
		key, _ := s.Attr("id")
		if key == "" {
			key = fmt.Sprintf("file%d", i+1)
		}
		title := strings.TrimSpace(doc.Find("label[for='" + strings.TrimSuffix(key, "_content") + "']").Text())
		if title == "" || title == wno {
			title = m.Title
		}
		for j, v := range urls {
			result.Tracks = append(result.Tracks, Track{Key: fmt.Sprintf("%s-%d", key, j+1), Title: title, URL: v, Chapters: chapters})
		}
	})
	if len(result.Tracks) == 0 {
		return nil, fmt.Errorf("ASMR18 no supported HLS source for %s", wno)
	}
	return result, nil
}
