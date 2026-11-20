package stream

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/SiIverAsh/otoha/internal/dlsite"
)

var hlsURI = regexp.MustCompile(`URI="([^"]+)"`)

// ResolveHLSResource restricts child playlists, keys and segments to the stored
// manifest's origin and directory. Client input cannot turn this into an open proxy.
func ResolveHLSResource(manifest, resource string) (*url.URL, error) {
	root, err := url.Parse(manifest)
	if err != nil || root.Host == "" || root.User != nil || (root.Scheme != "https" && root.Scheme != "http") {
		return nil, fmt.Errorf("invalid manifest URL")
	}
	target := root
	if resource != "" {
		ref, e := url.Parse(resource)
		if e != nil {
			return nil, e
		}
		target = root.ResolveReference(ref)
	}
	dir := path.Dir(root.Path) + "/"
	clean := path.Clean(target.Path)
	if target.Scheme != root.Scheme || target.Host != root.Host || target.User != nil || strings.Contains(target.Path, "\\") || !strings.HasPrefix(clean, dir) {
		return nil, fmt.Errorf("HLS resource outside work directory")
	}
	return target, nil
}

func rewriteHLS(body, manifest, current, endpoint string) (string, error) {
	base, err := url.Parse(current)
	if err != nil {
		return "", err
	}
	rewrite := func(raw string) (string, error) {
		ref, e := url.Parse(raw)
		if e != nil {
			return "", e
		}
		absolute := base.ResolveReference(ref).String()
		if _, e = ResolveHLSResource(manifest, absolute); e != nil {
			return "", e
		}
		return endpoint + "?hls=" + url.QueryEscape(absolute), nil
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			var rewriteErr error
			lines[i] = hlsURI.ReplaceAllStringFunc(line, func(attr string) string {
				m := hlsURI.FindStringSubmatch(attr)
				u, e := rewrite(m[1])
				if e != nil {
					rewriteErr = e
					return attr
				}
				return `URI="` + u + `"`
			})
			if rewriteErr != nil {
				return "", rewriteErr
			}
		} else {
			lines[i], err = rewrite(trimmed)
			if err != nil {
				return "", err
			}
		}
	}
	return strings.Join(lines, "\n"), nil
}

func ProxyHLS(w http.ResponseWriter, r *http.Request, manifest, proxy string) error {
	target, err := ResolveHLSResource(manifest, r.URL.Query().Get("hls"))
	if err != nil {
		http.Error(w, "invalid HLS resource", 400)
		return err
	}
	tr, err := dlsite.BuildHTTPTransport(proxy)
	if err != nil {
		http.Error(w, "invalid proxy", 502)
		return err
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		_, e := ResolveHLSResource(manifest, req.URL.String())
		return e
	}}
	req, err := http.NewRequestWithContext(r.Context(), "GET", target.String(), nil)
	if err != nil {
		http.Error(w, "invalid HLS URL", 502)
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Otoha/1.0)")
	req.Header.Set("Referer", "https://asmr18.fans/")
	req.Header.Set("Origin", "https://asmr18.fans")
	playlist := strings.HasSuffix(strings.ToLower(target.Path), ".m3u8")
	if !playlist {
		for _, h := range []string{"Range", "If-Range"} {
			if v := r.Header.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "upstream HLS request failed", 502)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, "upstream HLS resource unavailable", resp.StatusCode)
		return fmt.Errorf("HLS HTTP %d", resp.StatusCode)
	}
	if playlist {
		b, e := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
		if e != nil || len(b) > 4<<20 || !strings.HasPrefix(strings.TrimSpace(string(b)), "#EXTM3U") {
			http.Error(w, "invalid HLS playlist", 502)
			return fmt.Errorf("invalid HLS playlist")
		}
		body, e := rewriteHLS(string(b), manifest, resp.Request.URL.String(), r.URL.Path)
		if e != nil {
			http.Error(w, "unsupported HLS resource", 502)
			return e
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		_, err = io.WriteString(w, body)
		return err
	}
	for _, h := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "Content-Type"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	return err
}
