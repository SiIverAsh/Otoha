package stream

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/SiIverAsh/otoha/internal/dlsite"
)

// ContentTypeForExt returns the exact MIME type specified in §7 for native browser decoding.
func ContentTypeForExt(nameOrPath string) string {
	switch strings.ToLower(filepath.Ext(nameOrPath)) {
	case ".wav":
		return "audio/wav"
	case ".flac":
		return "audio/flac"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".aac":
		return "audio/aac"
	case ".ogg":
		return "audio/ogg"
	case ".opus":
		return "audio/ogg"
	default:
		return "application/octet-stream"
	}
}

// ServeAudioFile streams a local audio file with full HTTP Range / 206 Partial Content support
// via stdlib http.ServeContent without transcoding or loading the entire file into memory (§7).
func ServeAudioFile(w http.ResponseWriter, r *http.Request, absPath string) error {
	f, err := os.Open(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "audio file not found", http.StatusNotFound)
			return err
		}
		http.Error(w, "failed to open audio file", http.StatusInternalServerError)
		return err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		http.Error(w, "failed to stat audio file", http.StatusInternalServerError)
		return err
	}
	if st.IsDir() {
		http.Error(w, "invalid audio target", http.StatusBadRequest)
		return errors.New("stream target is a directory")
	}

	w.Header().Set("Content-Type", ContentTypeForExt(absPath))
	// http.ServeContent sets Accept-Ranges: bytes, Content-Range, 206 Partial Content,
	// and streams using bounded io.CopyN buffers without buffering the entire file in RAM.
	http.ServeContent(w, r, filepath.Base(absPath), st.ModTime(), f)
	return nil
}

// ProxyRemoteAudio streams a remote audio URL (e.g. from asmr.one) while forwarding HTTP Range
// headers and attaching required Referer/Origin headers so browser seeking (206 Partial Content) works seamlessly.
func ProxyRemoteAudio(w http.ResponseWriter, r *http.Request, remoteURL, nameOrPath, proxyURL string) error {
	tr, err := dlsite.BuildHTTPTransport(proxyURL)
	if err != nil {
		http.Error(w, "invalid proxy configuration", http.StatusBadGateway)
		return err
	}
	client := &http.Client{Transport: tr}

	method := http.MethodGet
	if r.Method == http.MethodHead {
		method = http.MethodHead
	}
	req, err := http.NewRequestWithContext(r.Context(), method, remoteURL, nil)
	if err != nil {
		http.Error(w, "invalid remote stream URL", http.StatusBadGateway)
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Otoha/1.0; +https://github.com/SiIverAsh/Otoha)")
	req.Header.Set("Referer", "https://asmr.one/")
	req.Header.Set("Origin", "https://asmr.one")
	for _, hdr := range []string{"Range", "If-Range", "If-Modified-Since"} {
		if v := r.Header.Get(hdr); v != "" {
			req.Header.Set(hdr, v)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "upstream audio stream failed", http.StatusBadGateway)
		return err
	}
	defer resp.Body.Close()

	for _, hdr := range []string{"Accept-Ranges", "Content-Range", "Content-Length", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(hdr); v != "" {
			w.Header().Set(hdr, v)
		}
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" || strings.HasPrefix(ct, "application/octet-stream") {
		ct = ContentTypeForExt(nameOrPath)
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(resp.StatusCode)

	if method != http.MethodHead {
		buf := make([]byte, 32*1024)
		_, _ = io.CopyBuffer(w, resp.Body, buf)
	}
	return nil
}
