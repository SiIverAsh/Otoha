package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SiIverAsh/otoha/internal/dlsite"
)

// FillMissingMetadata enriches an existing work without replacing known metadata,
// user data, or playback sources. Unlike SaveMetadata, people and tags are merged.
func (s *Store) FillMissingMetadata(ctx context.Context, meta *dlsite.Metadata, coverPath string) error {
	if meta == nil {
		return fmt.Errorf("missing metadata")
	}
	wno := dlsite.NormalizeWorkno(meta.Workno)
	if wno == "" {
		return fmt.Errorf("invalid workno in metadata: %q", meta.Workno)
	}
	if meta.NotFound {
		return fmt.Errorf("metadata not found for %s", wno)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var sets []string
	var args []any
	for _, field := range []struct{ column, value string }{
		{"title", meta.Title},
		{"circle_id", meta.CircleID},
		{"circle_name", meta.CircleName},
		{"release_date", meta.ReleaseDate},
		{"create_date", meta.CreateDate},
		{"work_type", meta.WorkType},
		{"series_id", meta.SeriesID},
		{"series_name", meta.SeriesName},
		{"cover_path", coverPath},
		{"cover_url", meta.CoverURL},
		{"sam_cover_url", meta.SamCoverURL},
		{"thumbnail_cover_url", meta.ThumbnailCoverURL},
		{"original_workno", meta.OriginalWorkno},
		{"source_type", meta.SourceType},
		{"source_url", meta.SourceURL},
		{"work_attributes", meta.WorkAttributes},
		{"language_editions_json", meta.LanguageEditionsJSON},
		{"other_editions_json", meta.OtherEditionsJSON},
		{"translation_info_json", meta.TranslationInfoJSON},
		{"rate_detail_json", meta.RateDetailJSON},
		{"rank_json", meta.RankJSON},
	} {
		value := strings.TrimSpace(field.value)
		if value == "" {
			continue
		}
		missing := fmt.Sprintf("TRIM(COALESCE(%s, '')) = ''", field.column)
		if field.column == "title" {
			missing += " OR UPPER(TRIM(title)) = UPPER(workno)"
		} else if strings.HasSuffix(field.column, "_json") {
			if value == "[]" || value == "{}" || value == "null" {
				continue
			}
			missing = fmt.Sprintf("TRIM(COALESCE(%s, '')) IN ('', '[]', '{}', 'null')", field.column)
		}
		sets = append(sets, fmt.Sprintf("%s = CASE WHEN %s THEN ? ELSE %s END", field.column, missing, field.column))
		args = append(args, value)
	}
	hasSubtitle := 0
	if meta.HasSubtitle {
		hasSubtitle = 1
	}
	fetchedAt := meta.FetchedAt
	if fetchedAt <= 0 {
		fetchedAt = time.Now().Unix()
	}
	for _, field := range []struct {
		column string
		value  any
	}{
		{"asmrone_id", meta.AsmrOneID},
		{"age_category", meta.AgeCategory},
		{"price", meta.Price},
		{"file_size", meta.FileSize},
		{"duration", meta.Duration},
		{"has_subtitle", hasSubtitle},
		{"rating", meta.Rating},
		{"rate_count", meta.RateCount},
		{"review_count", meta.ReviewCount},
		{"dl_count", meta.DLCount},
		{"scraped_at", fetchedAt},
	} {
		// Incoming zero/negative values are unknown and must not erase evidence.
		sets = append(sets, fmt.Sprintf("%s = CASE WHEN COALESCE(%s, 0) = 0 AND ? > 0 THEN ? ELSE %s END", field.column, field.column, field.column))
		args = append(args, field.value, field.value)
	}
	args = append(args, wno)
	result, err := tx.ExecContext(ctx, "UPDATE work SET "+strings.Join(sets, ", ")+" WHERE workno = ?", args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	for _, role := range []struct {
		name   string
		people []string
	}{
		{"va", meta.VAs},
		{"scenario", meta.Scenarios},
		{"illust", meta.Illusts},
		{"music", meta.Musics},
	} {
		for _, raw := range role.people {
			name := strings.TrimSpace(raw)
			if name == "" {
				continue
			}
			id, err := upsertPersonTx(ctx, tx, name)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_person(workno,person_id,role) VALUES(?,?,?)`, wno, id, role.name); err != nil {
				return err
			}
		}
	}
	for _, raw := range meta.Tags {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		id, err := upsertTagTx(ctx, tx, name, "dlsite")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_tag(workno,tag_id) VALUES(?,?)`, wno, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListIncompleteASMR18Worknos selects works lacking core catalog metadata.
// Optional fields such as series, translations, and ranks do not trigger retries.
func (s *Store) ListIncompleteASMR18Worknos(ctx context.Context) ([]string, error) {
	return s.listASMR18MetadataWorknos(ctx, `WHERE
		TRIM(COALESCE(w.title, '')) = '' OR
		TRIM(COALESCE(w.release_date, '')) = '' OR
		TRIM(COALESCE(w.work_type, '')) = '' OR
		TRIM(COALESCE(w.circle_id, '')) = '' OR
		TRIM(COALESCE(w.circle_name, '')) = '' OR
		COALESCE(w.age_category, 0) = 0 OR
		(TRIM(COALESCE(w.cover_url, '')) = '' AND TRIM(COALESCE(w.cover_path, '')) = '')`)
}

// ListAllASMR18Worknos returns imported ASMR18 works for audits and explicit refreshes.
func (s *Store) ListAllASMR18Worknos(ctx context.Context) ([]string, error) {
	return s.listASMR18MetadataWorknos(ctx, "")
}

func (s *Store) ListASMR18WorknosMissingReleaseDate(ctx context.Context) ([]string, error) {
	return s.listASMR18MetadataWorknos(ctx, "WHERE TRIM(COALESCE(w.release_date, '')) = ''")
}

// FillASMR18ReleaseDate writes exactly one field, only for an imported ASMR18 work
// whose release date is still missing. Existing dates and other fields are kept.
func (s *Store) FillASMR18ReleaseDate(ctx context.Context, workno, releaseDate string) (bool, error) {
	wno := dlsite.NormalizeWorkno(workno)
	date := strings.TrimSpace(releaseDate)
	if wno == "" {
		return false, fmt.Errorf("invalid workno %q", workno)
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return false, fmt.Errorf("invalid release date %q: %w", releaseDate, err)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE work SET release_date = ?
		WHERE workno = ? AND TRIM(COALESCE(release_date, '')) = ''
		AND EXISTS (SELECT 1 FROM asmr18_work aw WHERE aw.workno = work.workno)`, date, wno)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

func (s *Store) listASMR18MetadataWorknos(ctx context.Context, condition string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT w.workno FROM work w JOIN asmr18_work aw ON aw.workno = w.workno `+condition+` ORDER BY w.workno ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	worknos := []string{}
	for rows.Next() {
		var wno string
		if err := rows.Scan(&wno); err != nil {
			return nil, err
		}
		worknos = append(worknos, wno)
	}
	return worknos, rows.Err()
}
