package scrape

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/SiIverAsh/otoha/internal/asmr18"
	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
)

// ASMR18SplitOptions selects existing single-stream works for track splitting.
type ASMR18SplitOptions struct {
	Worknos      []string
	MaxWorks     int
	ChaptersOnly bool
	DryRun       bool
}

// ASMR18SplitResult reports the evidence and track ranges for one work.
type ASMR18SplitResult struct {
	Workno   string
	Source   string
	Segments []db.ASMR18TrackSegment
	Message  string
	Err      error
}

// SplitASMR18Tracks expands source chapter cues or verifies official DLsite
// timings against the existing ASMR18 stream before saving a split.
func SplitASMR18Tracks(ctx context.Context, store *db.Store, cfg *config.Config, opts ASMR18SplitOptions, report func(ASMR18SplitResult)) error {
	if opts.MaxWorks < 0 {
		return fmt.Errorf("maximum works cannot be negative")
	}
	worknos := make([]string, 0, len(opts.Worknos))
	for _, input := range opts.Worknos {
		workno := dlsite.NormalizeWorkno(input)
		if !strings.HasPrefix(workno, "RJ") {
			return fmt.Errorf("expected an RJ number, got %q", input)
		}
		worknos = append(worknos, workno)
	}
	candidates, err := store.ListASMR18TrackCandidates(ctx, worknos, 0)
	if err != nil {
		return err
	}
	var sources asmr18SplitSources
	if !opts.ChaptersOnly {
		snap := cfg.Snapshot()
		sources.dlsite, err = dlsite.NewClient(dlsite.ClientOptions{
			BaseURL:     snap.DLsiteBaseURL,
			UserAgent:   snap.ScraperUserAgent,
			ProxyURL:    snap.ScraperProxy,
			MinInterval: time.Duration(snap.ScraperIntervalSec * float64(time.Second)),
		})
		if err != nil {
			return err
		}
		sources.media, err = asmr18.New(snap.ScraperProxy, asmr18.DefaultInterval)
		if err != nil {
			return err
		}
	}
	processed := 0
	failures := 0
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if opts.MaxWorks > 0 && processed == opts.MaxWorks {
			break
		}
		result := ASMR18SplitResult{
			Workno:   candidate.Workno,
			Source:   "ASMR18",
			Segments: db.ASMR18SegmentsFromChapters(candidate.Chapters, candidate.Duration),
		}
		if len(result.Segments) == 0 {
			if opts.ChaptersOnly {
				continue
			}
			result.Source = "DLsite"
			result.Segments, result.Message, result.Err = sources.resolve(ctx, candidate)
		}
		if len(result.Segments) > 0 && !opts.DryRun {
			if err := store.SaveASMR18TrackSegments(ctx, candidate, result.Segments); err != nil {
				return fmt.Errorf("save ASMR18 tracks for %s: %w", candidate.Workno, err)
			}
		}
		processed++
		if report != nil {
			report(result)
		}
		if result.Err != nil {
			failures++
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if dlsite.IsTrackListAccessError(result.Err) || blockingASMR18Error(result.Err) {
				return fmt.Errorf("stopped at %s: %w", result.Workno, result.Err)
			}
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d ASMR18 works could not be checked", failures)
	}
	return nil
}

type asmr18SplitSources struct {
	dlsite interface {
		FetchTrackList(context.Context, string) ([]dlsite.TrackListEntry, error)
	}
	media interface {
		ProbeMediaDuration(context.Context, string) (float64, error)
	}
}

func (s asmr18SplitSources) resolve(ctx context.Context, candidate db.ASMR18TrackCandidate) ([]db.ASMR18TrackSegment, string, error) {
	entries, err := s.dlsite.FetchTrackList(ctx, candidate.Workno)
	if errors.Is(err, dlsite.ErrWorkNotFound) {
		return nil, "DLsite work unavailable", nil
	}
	if err != nil {
		return nil, "", err
	}
	if len(entries) < 2 {
		return nil, "DLsite has no unambiguous timed track list", nil
	}
	duration := candidate.Duration
	if duration <= 0 {
		duration, err = s.media.ProbeMediaDuration(ctx, candidate.StreamURL)
		if err != nil {
			return nil, "", err
		}
		if duration <= 0 {
			return nil, "ASMR18 duration unavailable", nil
		}
	}
	segments := segmentsFromDLsite(entries, duration)
	if len(segments) == 0 {
		return nil, "DLsite track timings differ from the ASMR18 stream", nil
	}
	return segments, "", nil
}

func segmentsFromDLsite(entries []dlsite.TrackListEntry, duration float64) []db.ASMR18TrackSegment {
	if len(entries) < 2 || duration <= 0 || math.IsInf(duration, 0) || math.IsNaN(duration) {
		return nil
	}
	segments := make([]db.ASMR18TrackSegment, 0, len(entries))
	var start float64
	for _, entry := range entries {
		end := start + entry.DurationSeconds
		if entry.Title == "" || !(end > start) || math.IsInf(end, 0) {
			return nil
		}
		segments = append(segments, db.ASMR18TrackSegment{Title: entry.Title, Start: start, End: &end})
		start = end
	}
	// DLsite creators publish whole seconds, which may truncate each
	// track separately. Cap the combined allowance for longer lists.
	tolerance := min(10.0, float64(len(entries))+0.5)
	last := &segments[len(segments)-1]
	if math.Abs(start-duration) > tolerance || last.Start >= duration {
		return nil
	}
	last.End = &duration
	return segments
}
