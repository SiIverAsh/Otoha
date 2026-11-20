package scrape

import (
	"context"
	"errors"
	"testing"

	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
)

type stubDLsiteTrackList struct {
	entries []dlsite.TrackListEntry
	err     error
}

func (s stubDLsiteTrackList) FetchTrackList(context.Context, string) ([]dlsite.TrackListEntry, error) {
	return s.entries, s.err
}

type stubASMR18Media struct {
	duration float64
	calls    int
}

func (s *stubASMR18Media) ProbeMediaDuration(context.Context, string) (float64, error) {
	s.calls++
	return s.duration, nil
}

func TestResolveASMR18SplitVerifiesPublishedDurationsAgainstMedia(t *testing.T) {
	tracks := []dlsite.TrackListEntry{
		{Title: "Introduction", DurationSeconds: 118},
		{Title: "Chapter 1", DurationSeconds: 255},
		{Title: "Chapter 2", DurationSeconds: 487},
	}
	source := stubDLsiteTrackList{entries: tracks}
	media := &stubASMR18Media{duration: 862.4}
	sources := asmr18SplitSources{dlsite: source, media: media}
	candidate := db.ASMR18TrackCandidate{Workno: "RJ123456"}
	segments, _, err := sources.resolve(context.Background(), candidate)
	if err != nil || media.calls != 1 || len(segments) != 3 {
		t.Fatalf("segments=%+v media calls=%d err=%v", segments, media.calls, err)
	}
	if segments[0].Start != 0 || *segments[0].End != 118 ||
		segments[1].Start != 118 || *segments[1].End != 373 ||
		segments[2].Start != 373 || *segments[2].End != media.duration {
		t.Fatalf("incorrect segment ranges: %+v", segments)
	}

	candidate.Duration = 1030
	segments, message, err := sources.resolve(context.Background(), candidate)
	if err != nil || len(segments) != 0 || message == "" || media.calls != 1 {
		t.Fatalf("mismatched edition: segments=%+v message=%q media calls=%d err=%v", segments, message, media.calls, err)
	}
}

func TestResolveASMR18SplitSkipsMediaProbeWithoutTimedList(t *testing.T) {
	candidate := db.ASMR18TrackCandidate{Workno: "RJ123456"}
	media := &stubASMR18Media{}
	for _, source := range []stubDLsiteTrackList{{}, {err: dlsite.ErrWorkNotFound}} {
		sources := asmr18SplitSources{dlsite: source, media: media}
		segments, message, err := sources.resolve(context.Background(), candidate)
		if err != nil || len(segments) != 0 || message == "" || media.calls != 0 {
			t.Fatalf("segments=%+v message=%q media calls=%d err=%v", segments, message, media.calls, err)
		}
	}
	fetchErr := errors.New("DLsite timed out")
	sources := asmr18SplitSources{dlsite: stubDLsiteTrackList{err: fetchErr}, media: media}
	_, _, err := sources.resolve(context.Background(), candidate)
	if !errors.Is(err, fetchErr) || media.calls != 0 {
		t.Fatalf("media calls=%d err=%v", media.calls, err)
	}
}
