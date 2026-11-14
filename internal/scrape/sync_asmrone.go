package scrape

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/SiIverAsh/otoha/internal/asmrone"
	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
	"github.com/SiIverAsh/otoha/internal/search"
)

// FieldCompleteness tracks non-empty field counts and coverage ratios across synced works.
type FieldCompleteness struct {
	TotalChecked    int     `json:"total_checked"`
	CircleNonEmpty  int     `json:"circle_non_empty"`
	VANonEmpty      int     `json:"va_non_empty"`
	TagsNonEmpty    int     `json:"tags_non_empty"`
	ReleaseNonEmpty int     `json:"release_non_empty"`
	CoverNonEmpty   int     `json:"cover_non_empty"`
	DurationNonZero int     `json:"duration_non_zero"`
	EditionsPresent int     `json:"editions_present"`
	CircleRatePct   float64 `json:"circle_rate_pct"`
	VARatePct       float64 `json:"va_rate_pct"`
	TagsRatePct     float64 `json:"tags_rate_pct"`
	CoverRatePct    float64 `json:"cover_rate_pct"`
	DurationRatePct float64 `json:"duration_rate_pct"`
}

func (fc *FieldCompleteness) Add(m *dlsite.Metadata) {
	if m == nil {
		return
	}
	fc.TotalChecked++
	if m.CircleName != "" {
		fc.CircleNonEmpty++
	}
	if len(m.VAs) > 0 {
		fc.VANonEmpty++
	}
	if len(m.Tags) > 0 {
		fc.TagsNonEmpty++
	}
	if m.ReleaseDate != "" {
		fc.ReleaseNonEmpty++
	}
	if m.CoverURL != "" {
		fc.CoverNonEmpty++
	}
	if m.Duration > 0 {
		fc.DurationNonZero++
	}
	if m.LanguageEditionsJSON != "" || m.OtherEditionsJSON != "" {
		fc.EditionsPresent++
	}
	if fc.TotalChecked > 0 {
		denom := float64(fc.TotalChecked)
		round1 := func(n int) float64 {
			return math.Round((float64(n)/denom)*1000) / 10
		}
		fc.CircleRatePct = round1(fc.CircleNonEmpty)
		fc.VARatePct = round1(fc.VANonEmpty)
		fc.TagsRatePct = round1(fc.TagsNonEmpty)
		fc.CoverRatePct = round1(fc.CoverNonEmpty)
		fc.DurationRatePct = round1(fc.DurationNonZero)
	}
}

// AsmrOneSyncCheckpoint stores exact page cursor & incremental stats on disk so interrupted
// bulk runs resume from `NextPage` without repeating completed pages.
type AsmrOneSyncCheckpoint struct {
	Order         string            `json:"order"`
	Sort          string            `json:"sort"`
	PageSize      int               `json:"page_size"`
	SubtitleOnly  bool              `json:"subtitle_only"`
	IncludeTracks bool              `json:"include_tracks"`
	CurrentPage   int               `json:"current_page"`
	NextPage      int               `json:"next_page"`
	TotalPages    int               `json:"total_pages"`
	TotalCount    int               `json:"total_count"`
	Processed     int               `json:"processed"`
	Inserted      int               `json:"inserted"`
	Duplicates    int               `json:"duplicates"` // existing records skipped/merged — never counted as failures
	TracksSynced  int               `json:"tracks_synced"`
	Failures      int               `json:"failures"`
	WorksPerSec   float64           `json:"works_per_sec"`
	ETASeconds    int64             `json:"eta_seconds"`
	Completeness  FieldCompleteness `json:"completeness"`
	Finished      bool              `json:"finished"`
	UpdatedAt     int64             `json:"updated_at"`
}

// SyncOptions configures bulk catalog & track synchronization from asmr.one.
type SyncOptions struct {
	PageSize        int
	StartPage       int // 0 => resume from checkpoint automatically
	MaxPages        int // 0 => sync until last page
	Order           string
	Sort            string
	SubtitleOnly    bool
	IncludeTracks   bool
	ResetCheckpoint bool
}

func CheckpointFilePath(cfg *config.Config) string {
	return filepath.Join(cfg.DataDir, "cache", "asmrone", "sync_checkpoint.json")
}

func LoadSyncCheckpoint(cfg *config.Config) (*AsmrOneSyncCheckpoint, error) {
	p := CheckpointFilePath(cfg)
	raw, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cp AsmrOneSyncCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		// Tolerate corrupted checkpoint without crashing
		return nil, nil
	}
	return &cp, nil
}

func SaveSyncCheckpoint(cfg *config.Config, cp *AsmrOneSyncCheckpoint) error {
	if cp == nil {
		return nil
	}
	cp.UpdatedAt = time.Now().Unix()
	p := CheckpointFilePath(cfg)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func NewAsmrOneClient(cfg *config.Config) (*asmrone.Client, error) {
	snap := cfg.Snapshot()
	return asmrone.New(asmrone.Options{
		BaseURLs:    snap.AsmrOneBaseURLs,
		Locale:      snap.ScraperLocale,
		UserAgent:   snap.ScraperUserAgent,
		ProxyURL:    snap.ScraperProxy,
		MinInterval: time.Duration(snap.AsmrOneIntervalSec * float64(time.Second)),
	})
}

// SyncWorkRemoteTracks fetches GET /api/tracks/{id} for a single work and saves its audio tree + bonus attachments.
func SyncWorkRemoteTracks(ctx context.Context, store *db.Store, client *asmrone.Client, workno string, asmrOneID int) (int, error) {
	wno := dlsite.NormalizeWorkno(workno)
	if wno == "" {
		return 0, fmt.Errorf("invalid workno %q", workno)
	}
	numID := asmrOneID
	if numID <= 0 {
		numID = dlsite.NumericIDFromWorkno(wno)
	}
	if numID <= 0 {
		return 0, fmt.Errorf("cannot derive numeric id for %s", wno)
	}

	bundle, err := client.FetchTracks(ctx, numID)
	if err != nil {
		return 0, err
	}

	inputs := make([]db.RemoteTrackInput, 0, len(bundle.AudioTracks))
	for _, tr := range bundle.AudioTracks {
		inputs = append(inputs, db.RemoteTrackInput{
			RelPath:      tr.RelPath,
			Size:         tr.Size,
			Duration:     tr.Duration,
			Ord:          tr.Ord,
			RemoteHash:   tr.RemoteHash,
			StreamURL:    tr.StreamURL,
			DownloadURL:  tr.DownloadURL,
			StreamLowURL: tr.StreamLowURL,
			SubtitleURL:  tr.SubtitleURL,
		})
	}

	attachJSON := ""
	if len(bundle.Attachments) > 0 {
		if raw, mErr := json.Marshal(bundle.Attachments); mErr == nil {
			attachJSON = string(raw)
		}
	}

	if err := store.SaveRemoteTracks(ctx, wno, inputs, attachJSON); err != nil {
		return 0, err
	}
	return len(inputs), nil
}

// SyncAsmrOneCatalog incrementally fetches paginated works from GET /api/works,
// commits each page immediately to SQLite + Bleve, updates the disk checkpoint per page,
// separates duplicates from failures, and computes field completeness + speed + ETA.
func SyncAsmrOneCatalog(
	ctx context.Context,
	store *db.Store,
	searchEng *search.Engine,
	cfg *config.Config,
	opts SyncOptions,
	onProgress func(AsmrOneSyncCheckpoint),
) (*AsmrOneSyncCheckpoint, error) {
	if opts.PageSize <= 0 || opts.PageSize > 100 {
		opts.PageSize = 100
	}
	if opts.Order == "" {
		opts.Order = "id"
	}
	if opts.Sort == "" {
		opts.Sort = "asc"
	}

	client, err := NewAsmrOneClient(cfg)
	if err != nil {
		return nil, err
	}

	var cp *AsmrOneSyncCheckpoint
	if !opts.ResetCheckpoint && opts.StartPage <= 0 {
		loaded, _ := LoadSyncCheckpoint(cfg)
		if loaded != nil && loaded.Order == opts.Order && loaded.Sort == opts.Sort && loaded.PageSize == opts.PageSize && loaded.SubtitleOnly == opts.SubtitleOnly {
			cp = loaded
			// Even if previously marked Finished, allow incremental continuation from last NextPage!
			cp.Finished = false
		}
	}
	if cp == nil {
		startP := opts.StartPage
		if startP <= 0 {
			startP = 1
		}
		cp = &AsmrOneSyncCheckpoint{
			Order:         opts.Order,
			Sort:          opts.Sort,
			PageSize:      opts.PageSize,
			SubtitleOnly:  opts.SubtitleOnly,
			IncludeTracks: opts.IncludeTracks,
			CurrentPage:   startP - 1,
			NextPage:      startP,
		}
	} else {
		cp.IncludeTracks = opts.IncludeTracks
		if cp.NextPage <= 0 {
			cp.NextPage = 1
		}
	}

	runStart := time.Now()
	worksInThisRun := 0
	pagesDoneInThisRun := 0

	for {
		if err := ctx.Err(); err != nil {
			_ = SaveSyncCheckpoint(cfg, cp)
			return cp, err
		}
		if opts.MaxPages > 0 && pagesDoneInThisRun >= opts.MaxPages {
			_ = SaveSyncCheckpoint(cfg, cp)
			return cp, nil
		}

		page := cp.NextPage
		items, pag, err := client.FetchWorksPage(ctx, page, cp.PageSize, cp.Order, cp.Sort, cp.SubtitleOnly)
		if errors.Is(err, context.Canceled) || (errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil) {
			_ = SaveSyncCheckpoint(cfg, cp)
			return cp, ctx.Err()
		}
		if err != nil {
			cp.Failures++
			_ = SaveSyncCheckpoint(cfg, cp)
			return cp, err
		}

		if pag.TotalCount > 0 {
			cp.TotalCount = pag.TotalCount
			cp.TotalPages = (pag.TotalCount + cp.PageSize - 1) / cp.PageSize
		}

		if len(items) == 0 {
			cp.Finished = true
			_ = SaveSyncCheckpoint(cfg, cp)
			if onProgress != nil {
				onProgress(*cp)
			}
			return cp, nil
		}

		// Incremental disk persistence: commit page immediately to SQLite + Bleve
		ins, dup, dbErr := store.SaveMetadataBatch(ctx, items)
		if dbErr != nil {
			cp.Failures += len(items)
			_ = SaveSyncCheckpoint(cfg, cp)
			return cp, dbErr
		}

		searchDocs := make([]db.SearchDocument, 0, len(items))
		for _, m := range items {
			cp.Completeness.Add(m)
			searchDocs = append(searchDocs, db.SearchDocument{
				Workno:     m.Workno,
				Title:      m.Title,
				CircleName: m.CircleName,
				SeriesName: m.SeriesName,
				VAs:        m.VAs,
				Tags:       m.Tags,
			})
		}
		if searchEng != nil {
			_ = searchEng.IndexWorkBatch(searchDocs)
		}

		if cp.IncludeTracks {
			for _, m := range items {
				if ctx.Err() != nil {
					break
				}
				nTracks, trErr := SyncWorkRemoteTracks(ctx, store, client, m.Workno, m.AsmrOneID)
				if trErr == nil {
					cp.TracksSynced += nTracks
				} else if !errors.Is(trErr, dlsite.ErrWorkNotFound) {
					cp.Failures++
				}
			}
		}

		cp.CurrentPage = page
		cp.NextPage = page + 1
		cp.Processed += len(items)
		cp.Inserted += ins
		cp.Duplicates += dup
		worksInThisRun += len(items)
		pagesDoneInThisRun++

		elapsedSec := time.Since(runStart).Seconds()
		if elapsedSec > 0.05 && worksInThisRun > 0 {
			cp.WorksPerSec = math.Round((float64(worksInThisRun)/elapsedSec)*10) / 10
		}
		if cp.TotalPages > 0 && cp.WorksPerSec > 0 {
			remWorks := max(0, (cp.TotalPages-cp.CurrentPage)*cp.PageSize)
			cp.ETASeconds = int64(float64(remWorks) / cp.WorksPerSec)
		} else {
			cp.ETASeconds = 0
		}

		if cp.TotalPages > 0 && page >= cp.TotalPages {
			cp.Finished = true
		}

		_ = SaveSyncCheckpoint(cfg, cp)
		if onProgress != nil {
			onProgress(*cp)
		}

		if cp.Finished {
			return cp, nil
		}
	}
}
