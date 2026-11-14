package stream

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
