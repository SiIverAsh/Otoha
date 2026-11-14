package scanner

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SiIverAsh/otoha/internal/db"
)

func writeTestWAV(t *testing.T, path string, sampleRate uint32, seconds int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir wav dir: %v", err)
	}
	channels := uint16(2)
	bitsPerSample := uint16(16)
	byteRate := sampleRate * uint32(channels) * uint32(bitsPerSample/8)
	blockAlign := channels * (bitsPerSample / 8)
	// Write header claiming `seconds` of PCM data, or actual small PCM payload
	dataSize := byteRate * uint32(seconds)

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create wav: %v", err)
	}
	defer f.Close()

	var hdr [44]byte
	copy(hdr[0:4], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:8], 36+dataSize)
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:20], 16)
	binary.LittleEndian.PutUint16(hdr[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(hdr[22:24], channels)
	binary.LittleEndian.PutUint32(hdr[24:28], sampleRate)
	binary.LittleEndian.PutUint32(hdr[28:32], byteRate)
	binary.LittleEndian.PutUint16(hdr[32:34], blockAlign)
	binary.LittleEndian.PutUint16(hdr[34:36], bitsPerSample)
	copy(hdr[36:40], "data")
	binary.LittleEndian.PutUint32(hdr[40:44], dataSize)

	if _, err := f.Write(hdr[:]); err != nil {
		t.Fatalf("write wav hdr: %v", err)
	}
	// Truncate/extend sparse file to exact WAV length so fileSize matches RIFF data chunk
	if err := f.Truncate(int64(44 + dataSize)); err != nil {
		t.Fatalf("truncate wav: %v", err)
	}
}

func TestScan520WorksPermissionSkipWildcardAndMultiPath(t *testing.T) {
	root := t.TempDir()
	dbDir := t.TempDir()
	store, err := db.Open(filepath.Join(dbDir, "otoha.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	st, err := store.UpsertStorage(ctx, "local", "Voices", root, "")
	if err != nil {
		t.Fatalf("UpsertStorage: %v", err)
	}

	// 1. Create 520 works (mix of 6-digit and 8-digit RJ/BJ/VJ) to verify >=500 works requirement (§12)
	for i := 1; i <= 520; i++ {
		var folder string
		if i <= 260 {
			folder = filepath.Join(root, fmt.Sprintf("[社团%d][RJ%08d] 作品_%d", i%10, 1000000+i, i))
		} else {
			folder = filepath.Join(root, "Circles", fmt.Sprintf("RJ%06d 标题_%d", 300000+i, i))
		}
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", folder, err)
		}
		// Give work #1 multi-version hierarchy + WAV duration + local cover
		if i == 1 {
			writeTestWAV(t, filepath.Join(folder, "01 本編.wav"), 8000, 12)
			writeTestWAV(t, filepath.Join(folder, "SE有り", "01 本編(SE有).wav"), 8000, 15)
			writeTestWAV(t, filepath.Join(folder, "SE無し", "01 本編(SE無).wav"), 8000, 15)
			_ = os.WriteFile(filepath.Join(folder, "RJ01000001_img_main.jpg"), []byte("fakejpg"), 0o644)
		} else {
			_ = os.WriteFile(filepath.Join(folder, "01.mp3"), []byte("dummy"), 0o644)
		}
	}

	// 2. Duplicate workno in a second path (§4.2: must record BOTH paths, never silently overwrite)
	dupDir := filepath.Join(root, "BackupCopy", "RJ01000001 备份")
	if err := os.MkdirAll(dupDir, 0o755); err != nil {
		t.Fatalf("mkdir dup: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dupDir, "01.mp3"), []byte("dup"), 0o644)

	// 3. Ignored directory matching `_*` wildcard (§4.2)
	ignoredDir := filepath.Join(root, "_temp_ignore", "RJ01999999")
	_ = os.MkdirAll(ignoredDir, 0o755)
	_ = os.WriteFile(filepath.Join(ignoredDir, "01.wav"), []byte("ignored"), 0o644)

	// 4. Unreadable directory simulated deterministically via ReadDirFunc (§4.2 & §12)
	forbiddenDir := filepath.Join(root, "ForbiddenLocked")
	_ = os.MkdirAll(forbiddenDir, 0o755)

	var logBuf bytes.Buffer
	logger := log.New(&logBuf, "", 0)

	stats, err := ScanRoot(ctx, root, Options{
		MaxDepth:       6,
		IgnorePatterns: []string{"_*", ".*"},
		Logger:         logger,
		ReadDirFunc: func(dir string) ([]os.DirEntry, error) {
			if strings.EqualFold(filepath.Clean(dir), filepath.Clean(forbiddenDir)) {
				return nil, os.ErrPermission
			}
			return os.ReadDir(dir)
		},
	}, func(w DiscoveredWork) error {
		return store.UpsertScannedWork(ctx, st.ID, w.Workno, w.FallbackTitle, w.RelPath, w.AgeHint, w.LocalCover, w.Tracks)
	})
	if err != nil {
		t.Fatalf("ScanRoot: %v", err)
	}

	// 520 unique works + 1 duplicate path = 521 work folders discovered
	if stats.WorksFound != 521 {
		t.Fatalf("expected 521 work folders discovered (520 + 1 duplicate path), got %d", stats.WorksFound)
	}
	if len(stats.Skipped) != 1 || !strings.Contains(logBuf.String(), "ForbiddenLocked") {
		t.Fatalf("expected ForbiddenLocked skipped and logged, got skipped=%+v log=%q", stats.Skipped, logBuf.String())
	}

	// Verify ignored `_*` work was NOT imported
	if _, err := store.GetWorkDetail(ctx, "RJ01999999", 1); err == nil {
		t.Fatalf("ignored _* directory RJ01999999 should not have been imported!")
	}

	// Verify RJ01000001 has BOTH paths recorded, local cover prioritized, WAV duration = 12s, and nested tree intact
	detail, err := store.GetWorkDetail(ctx, "RJ01000001", 1)
	if err != nil {
		t.Fatalf("GetWorkDetail RJ01000001: %v", err)
	}
	if len(detail.Paths) != 2 {
		t.Fatalf("expected 2 work_path rows for multi-path RJ01000001, got %+v", detail.Paths)
	}
	if !strings.HasSuffix(detail.CoverPath, "RJ01000001_img_main.jpg") {
		t.Fatalf("expected local cover RJ01000001_img_main.jpg, got %q", detail.CoverPath)
	}
	foundWAVDur := false
	for _, tr := range detail.Tracks {
		if tr.RelPath == "01 本編.wav" && tr.Duration != nil && math.Abs(*tr.Duration-12.0) < 0.05 {
			foundWAVDur = true
		}
	}
	if !foundWAVDur {
		t.Fatalf("expected 12.0s WAV duration on 01 本編.wav, got tracks=%+v", detail.Tracks)
	}
	// Tree should preserve SE有り and SE無し folders (§4.3)
	folderNames := map[string]bool{}
	for _, node := range detail.Tree {
		if node.Type == "folder" {
			folderNames[node.Name] = true
		}
	}
	if !folderNames["SE有り"] || !folderNames["SE無し"] {
		t.Fatalf("expected nested folders SE有り and SE無し in tree, got %+v", detail.Tree)
	}
}
