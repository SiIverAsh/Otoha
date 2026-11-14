package dlsite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Cache persists scraped DLsite metadata and permanent 404 markers to disk (§3.3)
// so re-scanning a media library never re-requests already cached works.
type Cache struct {
	dir string
	mu  sync.RWMutex
}

func NewCache(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Cache{dir: dir}, nil
}

func (c *Cache) entryPath(workno string) string {
	return filepath.Join(c.dir, NormalizeWorkno(workno)+".json")
}

// Get returns cached metadata if present on disk.
func (c *Cache) Get(workno string) (*Metadata, bool) {
	wno := NormalizeWorkno(workno)
	if wno == "" {
		return nil, false
	}
	c.mu.RLock()
	raw, err := os.ReadFile(c.entryPath(wno))
	c.mu.RUnlock()
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	var m Metadata
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false
	}
	return &m, true
}

// Put atomically writes metadata to the disk cache.
func (c *Cache) Put(m *Metadata) error {
	if m == nil {
		return nil
	}
	wno := NormalizeWorkno(m.Workno)
	if wno == "" {
		return nil
	}
	if m.FetchedAt == 0 {
		m.FetchedAt = time.Now().Unix()
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	dst := c.entryPath(wno)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// PutNotFound records a permanent 404 marker so future scans do not hammer DLsite for nonexistent IDs (§3.3).
func (c *Cache) PutNotFound(workno string) error {
	wno := NormalizeWorkno(workno)
	if wno == "" {
		return nil
	}
	return c.Put(&Metadata{
		Workno:    wno,
		NotFound:  true,
		Source:    "not_found",
		FetchedAt: time.Now().Unix(),
	})
}

// Invalidate removes a single workno from the disk cache (used when forcing a re-scrape).
func (c *Cache) Invalidate(workno string) error {
	wno := NormalizeWorkno(workno)
	if wno == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	err := os.Remove(c.entryPath(wno))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ScrapeCheckpoint records exact batch scrape progress on disk so interrupted batches
// resume from the exact remaining worknos without repeating completed ones (§3.3 & §12).
type ScrapeCheckpoint struct {
	Queue      []string          `json:"queue"`
	States     map[string]string `json:"states"` // "ok" | "not_found" | "error:<msg>"
	NextIndex  int               `json:"next_index"`
	Finished   bool              `json:"finished"`
	UpdatedAt  int64             `json:"updated_at"`
}

func (c *Cache) checkpointPath() string {
	return filepath.Join(c.dir, "scrape_checkpoint.json")
}

func (c *Cache) LoadCheckpoint() (*ScrapeCheckpoint, error) {
	c.mu.RLock()
	raw, err := os.ReadFile(c.checkpointPath())
	c.mu.RUnlock()
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cp ScrapeCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		// Corrupted checkpoint: tolerate and start fresh rather than crashing
		return nil, nil
	}
	if cp.States == nil {
		cp.States = make(map[string]string)
	}
	return &cp, nil
}

func (c *Cache) SaveCheckpoint(cp *ScrapeCheckpoint) error {
	if cp == nil {
		return nil
	}
	cp.UpdatedAt = time.Now().Unix()
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	dst := c.checkpointPath()
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
