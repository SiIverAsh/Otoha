package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/SiIverAsh/otoha/internal/dlsite"
)

// ASMR18TrackCandidate identifies a work's single ASMR18 stream. Duration is
// zero until the stream duration has been measured.
type ASMR18TrackCandidate struct {
	ID        int64
	Workno    string
	StreamURL string
	Duration  float64
	Chapters  []Chapter
}

// ASMR18TrackSegment defines a named playback range within an ASMR18 stream.
// Only the final range can omit its end when the original duration is unknown.
type ASMR18TrackSegment struct {
	Title string
	Start float64
	End   *float64
}

const singleASMR18Stream = `
	t.source_track_id IS NULL AND COALESCE(t.stream_url, '') != ''
	AND st.kind = 'asmr18'
	AND NOT EXISTS (
		SELECT 1 FROM track other
		JOIN storage other_storage ON other_storage.id = other.storage_id
		WHERE other.workno = t.workno AND other.id != t.id
		AND other.source_track_id IS NULL AND other_storage.kind = 'asmr18'
	)`

type asmr18SegmentSource struct {
	id       int64
	relPath  string
	duration float64
}

// ListASMR18TrackCandidates returns single-stream works without saved segments.
// An empty worknos list and a zero limit select all eligible works.
func (s *Store) ListASMR18TrackCandidates(ctx context.Context, worknos []string, limit int) ([]ASMR18TrackCandidate, error) {
	var filters string
	args := make([]any, 0, len(worknos)+1)
	if len(worknos) > 0 {
		placeholders := make([]string, 0, len(worknos))
		for _, workno := range worknos {
			normalized := dlsite.NormalizeWorkno(workno)
			if normalized == "" {
				return nil, fmt.Errorf("invalid work number %q", workno)
			}
			placeholders = append(placeholders, "?")
			args = append(args, normalized)
		}
		filters = " AND aw.workno IN (" + strings.Join(placeholders, ", ") + ")"
	}
	if limit == 0 {
		limit = -1
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT t.id, t.workno, t.stream_url, COALESCE(t.duration, 0) AS duration,
				EXISTS (SELECT 1 FROM track_chapter ch WHERE ch.track_id = t.id AND ch.ord > 0) AS has_chapters
			FROM asmr18_work aw
			JOIN track t ON t.workno = aw.workno
			JOIN storage st ON st.id = t.storage_id
			WHERE `+singleASMR18Stream+`
			AND NOT EXISTS (SELECT 1 FROM track segment WHERE segment.source_track_id = t.id)
			`+filters+`
			ORDER BY has_chapters DESC, (COALESCE(t.duration, 0) > 0) DESC, t.workno
			LIMIT ?
		)
		SELECT c.id, c.workno, c.stream_url, c.duration, ch.title, ch.start
		FROM candidates c LEFT JOIN track_chapter ch ON ch.track_id = c.id
		ORDER BY c.has_chapters DESC, (c.duration > 0) DESC, c.workno, ch.ord
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []ASMR18TrackCandidate
	for rows.Next() {
		var candidate ASMR18TrackCandidate
		var title sql.NullString
		var start sql.NullFloat64
		if err := rows.Scan(&candidate.ID, &candidate.Workno, &candidate.StreamURL, &candidate.Duration, &title, &start); err != nil {
			return nil, err
		}
		if len(candidates) == 0 || candidates[len(candidates)-1].ID != candidate.ID {
			candidates = append(candidates, candidate)
		}
		if start.Valid {
			current := &candidates[len(candidates)-1]
			current.Chapters = append(current.Chapters, Chapter{Title: title.String, Start: start.Float64})
		}
	}
	return candidates, rows.Err()
}

// SaveASMR18TrackSegments adds numbered ranges while retaining the original
// stream and any history that references it. An existing split is idempotent.
func (s *Store) SaveASMR18TrackSegments(ctx context.Context, candidate ASMR18TrackCandidate, segments []ASMR18TrackSegment) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	source := asmr18SegmentSource{id: candidate.ID}
	var streamURL string
	var alreadySplit bool
	err = tx.QueryRowContext(ctx, `
		SELECT t.rel_path, t.stream_url, COALESCE(t.duration, 0),
			EXISTS (SELECT 1 FROM track segment WHERE segment.source_track_id = t.id)
		FROM track t JOIN storage st ON st.id = t.storage_id
		JOIN asmr18_work aw ON aw.workno = t.workno
		WHERE t.id = ? AND t.workno = ? AND `+singleASMR18Stream,
		candidate.ID, candidate.Workno,
	).Scan(&source.relPath, &streamURL, &source.duration, &alreadySplit)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s no longer has the same single ASMR18 stream", candidate.Workno)
	}
	if err != nil {
		return err
	}
	if streamURL != candidate.StreamURL || (candidate.Duration > 0 && source.duration != candidate.Duration) {
		return fmt.Errorf("%s ASMR18 stream changed while matching the segment boundaries", candidate.Workno)
	}
	if alreadySplit {
		return nil
	}
	chapters, err := loadASMR18TrackChapters(ctx, tx, candidate.ID)
	if err != nil {
		return err
	}
	if !slices.Equal(chapters, candidate.Chapters) {
		return fmt.Errorf("%s ASMR18 chapters changed while matching the segment boundaries", candidate.Workno)
	}
	if err := validateASMR18TrackSegments(segments, source.duration); err != nil {
		return fmt.Errorf("%s: %w", candidate.Workno, err)
	}
	if end := segments[len(segments)-1].End; source.duration <= 0 && end != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE track SET duration = ? WHERE id = ?`, *end, candidate.ID); err != nil {
			return err
		}
	}
	if err := insertASMR18TrackSegments(ctx, tx, source, segments); err != nil {
		return err
	}
	return tx.Commit()
}

func insertASMR18TrackSegments(ctx context.Context, tx *sql.Tx, source asmr18SegmentSource, segments []ASMR18TrackSegment) error {
	for i, segment := range segments {
		end := segment.End
		if end == nil && source.duration > 0 {
			end = &source.duration
		}
		var segmentDuration *float64
		if end != nil {
			length := *end - segment.Start
			segmentDuration = &length
		}
		ord := i + 1
		relPath := fmt.Sprintf("%s-part-%03d.m3u8", strings.TrimSuffix(source.relPath, ".m3u8"), ord)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO track(workno, storage_id, work_rel, rel_path, duration, ord, display_title,
				source_track_id, start_seconds, end_seconds)
			SELECT workno, storage_id, work_rel, ?, ?, ?, ?, id, ?, ? FROM track WHERE id = ?
		`, relPath, segmentDuration, ord, strings.TrimSpace(segment.Title), segment.Start, end, source.id); err != nil {
			return err
		}
	}
	return nil
}

func loadASMR18TrackChapters(ctx context.Context, tx *sql.Tx, trackID int64) ([]Chapter, error) {
	rows, err := tx.QueryContext(ctx, `SELECT title, start FROM track_chapter WHERE track_id = ? ORDER BY ord`, trackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chapters []Chapter
	for rows.Next() {
		var chapter Chapter
		if err := rows.Scan(&chapter.Title, &chapter.Start); err != nil {
			return nil, err
		}
		chapters = append(chapters, chapter)
	}
	return chapters, rows.Err()
}

// ASMR18SegmentsFromChapters turns an ordered set of authoritative cues into
// contiguous tracks, or returns nil when it cannot establish a full split.
func ASMR18SegmentsFromChapters(chapters []Chapter, duration float64) []ASMR18TrackSegment {
	segments := make([]ASMR18TrackSegment, 0, len(chapters))
	for _, chapter := range chapters {
		title := strings.TrimSpace(chapter.Title)
		start := chapter.Start
		if len(segments) > 0 {
			previous := &segments[len(segments)-1]
			if start == previous.Start && title == previous.Title {
				continue
			}
			previous.End = &start
		}
		segments = append(segments, ASMR18TrackSegment{Title: title, Start: start})
	}
	if len(segments) > 0 && duration > 0 {
		segments[len(segments)-1].End = &duration
	}
	if validateASMR18TrackSegments(segments, duration) != nil {
		return nil
	}
	return segments
}

func validateASMR18TrackSegments(segments []ASMR18TrackSegment, duration float64) error {
	if len(segments) < 2 || segments[0].Start != 0 {
		return fmt.Errorf("a split needs at least two segments beginning at zero")
	}
	for i, segment := range segments {
		if strings.TrimSpace(segment.Title) == "" || segment.Start < 0 || math.IsNaN(segment.Start) || math.IsInf(segment.Start, 0) {
			return fmt.Errorf("segment %d needs a title and a finite start time", i+1)
		}
		if segment.End != nil && (*segment.End <= segment.Start || math.IsNaN(*segment.End) || math.IsInf(*segment.End, 0)) {
			return fmt.Errorf("segment %d needs a finite end after its start", i+1)
		}
		if i+1 < len(segments) && (segment.End == nil || *segment.End != segments[i+1].Start) {
			return fmt.Errorf("segments %d and %d must be contiguous", i+1, i+2)
		}
	}
	last := segments[len(segments)-1]
	if duration > 0 && (last.Start >= duration || (last.End != nil && *last.End != duration)) {
		return fmt.Errorf("last segment must cover the measured end at %.3f seconds", duration)
	}
	return nil
}
