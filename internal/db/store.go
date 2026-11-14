package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SiIverAsh/otoha/internal/dlsite"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("db: record not found")

// ValidProgress enumerates user_state.progress values aligned with kikoeru t_review.progress (§5 & §14.2).
var ValidProgress = map[string]bool{
	"":          true,
	"marked":    true,
	"listening": true,
	"listened":  true,
	"replay":    true,
	"postponed": true,
}

type Work struct {
	Workno            string   `json:"workno"`
	AsmrOneID         int      `json:"asmrone_id,omitempty"`
	Title             string   `json:"title"`
	CircleID          string   `json:"circle_id"`
	CircleName        string   `json:"circle_name"`
	ReleaseDate       string   `json:"release_date"`
	CreateDate        string   `json:"create_date,omitempty"`
	AgeCategory       int      `json:"age_category"`
	WorkType          string   `json:"work_type"`
	SeriesID          string   `json:"series_id"`
	SeriesName        string   `json:"series_name"`
	Price             int      `json:"price"`
	FileSize          int64    `json:"file_size"`
	Duration          int      `json:"duration,omitempty"`
	HasSubtitle       bool     `json:"has_subtitle"`
	Rating            float64  `json:"rating"`
	RateCount         int      `json:"rate_count"`
	ReviewCount       int      `json:"review_count"`
	DLCount           int      `json:"dl_count"`
	CoverPath         string   `json:"cover_path,omitempty"`
	CoverURL          string   `json:"cover_url,omitempty"`
	SamCoverURL       string   `json:"sam_cover_url,omitempty"`
	ThumbnailCoverURL string   `json:"thumbnail_cover_url,omitempty"`
	OriginalWorkno    string   `json:"original_workno,omitempty"`
	NeedsRescrape     bool     `json:"needs_rescrape"`
	ScrapedAt         int64    `json:"scraped_at"`
	CreatedAt         int64    `json:"created_at"`
	VAs               []string `json:"vas"`
	Tags              []Tag    `json:"tags"`
	UserRating        *int     `json:"user_rating,omitempty"`
	Progress          string   `json:"progress,omitempty"`
	TrackCount        int      `json:"track_count"`
}

type Tag struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Source string `json:"source"` // "dlsite" | "user"
}

type WorkPath struct {
	Workno      string `json:"workno"`
	StorageID   int64  `json:"storage_id"`
	StorageName string `json:"storage_name,omitempty"`
	StorageRoot string `json:"storage_root,omitempty"`
	RelPath     string `json:"rel_path"`
}

type Track struct {
	ID           int64    `json:"id"`
	Workno       string   `json:"workno"`
	StorageID    int64    `json:"storage_id"`
	WorkRel      string   `json:"work_rel"`
	RelPath      string   `json:"rel_path"`
	Size         int64    `json:"size"`
	Duration     *float64 `json:"duration"`
	Ord          int      `json:"ord"`
	PlayPosition float64  `json:"play_position"`
	HasSubtitle  bool     `json:"has_subtitle"`
	StreamURL    string   `json:"stream_url,omitempty"`
	DownloadURL  string   `json:"download_url,omitempty"`
	StreamLowURL string   `json:"stream_low_url,omitempty"`
	SubtitleURL  string   `json:"subtitle_url,omitempty"`
	RemoteHash   string   `json:"remote_hash,omitempty"`
	IsRemote     bool     `json:"is_remote,omitempty"`
}

type TrackTreeNode struct {
	Name     string           `json:"name"`
	Type     string           `json:"type"` // "folder" | "audio"
	RelPath  string           `json:"rel_path"`
	Track    *Track           `json:"track,omitempty"`
	Children []*TrackTreeNode `json:"children,omitempty"`
}

type UserState struct {
	UserID    int64  `json:"user_id"`
	Workno    string `json:"workno"`
	Rating    *int   `json:"rating"`
	Review    string `json:"review"`
	Progress  string `json:"progress"`
	UpdatedAt int64  `json:"updated_at"`
}

type WorkDetail struct {
	Work
	SourceType           string           `json:"source_type,omitempty"`
	SourceURL            string           `json:"source_url,omitempty"`
	WorkAttributes       string           `json:"work_attributes,omitempty"`
	LanguageEditionsJSON string           `json:"language_editions_json,omitempty"`
	OtherEditionsJSON    string           `json:"other_editions_json,omitempty"`
	TranslationInfoJSON  string           `json:"translation_info_json,omitempty"`
	RateDetailJSON       string           `json:"rate_detail_json,omitempty"`
	RankJSON             string           `json:"rank_json,omitempty"`
	RemoteFilesJSON      string           `json:"remote_files_json,omitempty"`
	Scenarios            []string         `json:"scenarios"`
	Illusts              []string         `json:"illusts"`
	Musics               []string         `json:"musics"`
	Paths                []WorkPath       `json:"paths"`
	Tracks               []Track          `json:"tracks"`
	Tree                 []*TrackTreeNode `json:"tree"`
	State                *UserState       `json:"state,omitempty"`
}

type Storage struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"` // "local" | "webdav"
	Name   string `json:"name"`
	Root   string `json:"root"`
	Config string `json:"config,omitempty"`
}

type User struct {
	ID                int64  `json:"id"`
	Username          string `json:"username"`
	PasswordHash      string `json:"-"`
	IsAdmin           bool   `json:"is_admin"`
	MustResetPassword bool   `json:"must_reset_password"`
	CreatedAt         int64  `json:"created_at"`
}

type HistoryEntry struct {
	ID          int64   `json:"id"`
	UserID      int64   `json:"user_id"`
	Workno      string  `json:"workno"`
	WorkTitle   string  `json:"work_title"`
	CircleName  string  `json:"circle_name"`
	AgeCategory int     `json:"age_category"`
	TrackID     int64   `json:"track_id"`
	TrackName   string  `json:"track_name"`
	TrackDur    float64 `json:"track_duration"`
	StartedAt   int64   `json:"started_at"`
	LastAt      int64   `json:"last_at"`
	Position    float64 `json:"position"`
	Listened    float64 `json:"listened"`
	QueueCtx    string  `json:"queue_ctx"`
}

type ListWorksOptions struct {
	UserID    int64
	Page      int
	PageSize  int
	SortBy    string // "release_date" | "created_at" | "dl_count" | "rating" | "price" | "workno" | "title"
	SortDesc  bool
	AgeMode   string // "general" (age=1) | "sfw_unrated" (age in 0,1) | "r15" (age<=2) | "r18" (age=3) | "all"
	Circle    string
	VA        string
	Tag       string
	Progress     string
	StorageID    int64
	SubtitleOnly bool
	Worknos      []string // optional explicit workno set from Bleve search
	QueryLike string   // optional degraded SQL LIKE search
}

// Store wraps the pure-Go modernc.org/sqlite database.
type Store struct {
	db     *sql.DB
	dbPath string
}

// Open opens (or creates) the SQLite database in WAL mode with secure_delete enabled,
// and executes embedded migrations with automatic pre-migration backup.
func Open(dbPath string) (*Store, error) {
	if dbPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			return nil, err
		}
	}
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	// Single writer friendliness + WAL concurrency
	conn.SetMaxOpenConns(1)

	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA foreign_keys = ON;",
		"PRAGMA busy_timeout = 10000;",
		"PRAGMA secure_delete = ON;",
	}
	for _, p := range pragmas {
		if _, err := conn.Exec(p); err != nil {
			conn.Close()
			return nil, fmt.Errorf("exec %s: %w", p, err)
		}
	}

	migrations, err := loadEmbeddedMigrations()
	if err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := RunMigrations(conn, dbPath, migrations); err != nil {
		conn.Close()
		return nil, err
	}

	return &Store{db: conn, dbPath: dbPath}, nil
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) DBPath() string {
	return s.dbPath
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// UpsertStorage creates or updates a media storage root.
func (s *Store) UpsertStorage(ctx context.Context, kind, name, root, cfgJSON string) (*Storage, error) {
	if kind == "" {
		kind = "local"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO storage (kind, name, root, config)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET kind=excluded.kind, root=excluded.root, config=excluded.config
	`, kind, name, root, cfgJSON)
	if err != nil {
		return nil, err
	}
	var st Storage
	var cfg sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT id, kind, name, root, config FROM storage WHERE name = ?`, name).
		Scan(&st.ID, &st.Kind, &st.Name, &st.Root, &cfg)
	if err != nil {
		return nil, err
	}
	st.Config = cfg.String
	return &st, nil
}

func (s *Store) ListStorages(ctx context.Context) ([]Storage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, name, root, COALESCE(config, '') FROM storage ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Storage
	for rows.Next() {
		var st Storage
		if err := rows.Scan(&st.ID, &st.Kind, &st.Name, &st.Root, &st.Config); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) GetStorage(ctx context.Context, id int64) (*Storage, error) {
	var st Storage
	err := s.db.QueryRowContext(ctx, `SELECT id, kind, name, root, COALESCE(config, '') FROM storage WHERE id = ?`, id).
		Scan(&st.ID, &st.Kind, &st.Name, &st.Root, &st.Config)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &st, err
}

func (s *Store) DeleteStorage(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM storage WHERE id = ?`, id)
	return err
}

// ScannedTrackInput represents an audio file discovered by the filesystem scanner.
type ScannedTrackInput struct {
	RelPath  string
	Size     int64
	Duration *float64
	Ord      int
}

// UpsertScannedWork records a discovered work folder, preserves multi-path work_path entries (§4.2),
// updates tracks for that specific (storage_id, work_rel), and preserves existing metadata & user state.
func (s *Store) UpsertScannedWork(ctx context.Context, storageID int64, workno, fallbackTitle, workRel string, ageHint int, localCoverPath string, tracks []ScannedTrackInput) error {
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if fallbackTitle == "" {
		fallbackTitle = workno
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO work (workno, title, age_category, cover_path, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(workno) DO UPDATE SET
			cover_path = CASE
				WHEN ? != '' THEN ?
				ELSE work.cover_path
			END,
			age_category = CASE
				WHEN (work.age_category IS NULL OR work.age_category = 0) AND ? > 0 THEN ?
				ELSE work.age_category
			END
	`, workno, fallbackTitle, ageHint, localCoverPath, now, localCoverPath, localCoverPath, ageHint, ageHint)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO work_path (workno, storage_id, rel_path) VALUES (?, ?, ?)
	`, workno, storageID, workRel); err != nil {
		return err
	}

	// Keep existing track IDs stable on re-scan so play_position & listen_history stay intact!
	seenRel := make(map[string]bool, len(tracks))
	for _, tr := range tracks {
		seenRel[tr.RelPath] = true
		var dur any
		if tr.Duration != nil {
			dur = *tr.Duration
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO track (workno, storage_id, work_rel, rel_path, size, duration, ord)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(workno, storage_id, work_rel, rel_path) DO UPDATE SET
				size = excluded.size,
				duration = COALESCE(excluded.duration, track.duration),
				ord = excluded.ord
		`, workno, storageID, workRel, tr.RelPath, tr.Size, dur, tr.Ord)
		if err != nil {
			return err
		}
	}

	// Prune deleted tracks under this exact (workno, storage_id, work_rel)
	rows, err := tx.QueryContext(ctx, `SELECT id, rel_path FROM track WHERE workno = ? AND storage_id = ? AND work_rel = ?`, workno, storageID, workRel)
	if err != nil {
		return err
	}
	var staleIDs []int64
	for rows.Next() {
		var id int64
		var rp string
		if err := rows.Scan(&id, &rp); err != nil {
			rows.Close()
			return err
		}
		if !seenRel[rp] {
			staleIDs = append(staleIDs, id)
		}
	}
	rows.Close()
	for _, id := range staleIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM track WHERE id = ?`, id); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// SaveMetadata writes DLsite, asmr.one, or manually edited metadata into SQLite while preserving
// user tags (`tag.source = 'user'`) and local cover priority (§4.3: local cover > remote cover).
func (s *Store) SaveMetadata(ctx context.Context, meta *dlsite.Metadata, downloadedCoverPath string, preferLocalCover bool) error {
	if meta == nil {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := saveMetadataTx(ctx, tx, meta, downloadedCoverPath, preferLocalCover); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveMetadataBatch commits a batch of metadata items in a single SQLite transaction,
// returning newly inserted count vs duplicate/existing count (never counting duplicates as failures).
func (s *Store) SaveMetadataBatch(ctx context.Context, items []*dlsite.Metadata) (inserted int, duplicates int, err error) {
	if len(items) == 0 {
		return 0, 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	for _, meta := range items {
		if meta == nil {
			continue
		}
		existed, sErr := saveMetadataTx(ctx, tx, meta, "", true)
		if sErr != nil {
			return inserted, duplicates, sErr
		}
		if existed {
			duplicates++
		} else {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return inserted, duplicates, nil
}

func saveMetadataTx(ctx context.Context, tx *sql.Tx, meta *dlsite.Metadata, downloadedCoverPath string, preferLocalCover bool) (alreadyExisted bool, err error) {
	wno := dlsite.NormalizeWorkno(meta.Workno)
	if wno == "" {
		return false, fmt.Errorf("invalid workno in metadata: %q", meta.Workno)
	}
	now := time.Now().Unix()
	if meta.FetchedAt > 0 {
		now = meta.FetchedAt
	}

	var existingCover sql.NullString
	rowErr := tx.QueryRowContext(ctx, `SELECT cover_path FROM work WHERE workno = ?`, wno).Scan(&existingCover)
	alreadyExisted = rowErr == nil

	coverToSet := existingCover.String
	if downloadedCoverPath != "" {
		if !preferLocalCover || coverToSet == "" {
			coverToSet = downloadedCoverPath
		}
	}

	title := strings.TrimSpace(meta.Title)
	if title == "" {
		title = wno
	}
	hasSubInt := 0
	if meta.HasSubtitle {
		hasSubInt = 1
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO work (
			workno, asmrone_id, title, circle_id, circle_name, release_date, create_date,
			age_category, work_type, series_id, series_name, price, file_size, duration,
			has_subtitle, rating, rate_count, review_count, dl_count, cover_path, cover_url,
			sam_cover_url, thumbnail_cover_url, original_workno, source_type, source_url,
			work_attributes, language_editions_json, other_editions_json, translation_info_json,
			rate_detail_json, rank_json, needs_rescrape, scraped_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)
		ON CONFLICT(workno) DO UPDATE SET
			asmrone_id             = CASE WHEN excluded.asmrone_id > 0 THEN excluded.asmrone_id ELSE work.asmrone_id END,
			title                  = excluded.title,
			circle_id              = CASE WHEN excluded.circle_id != '' THEN excluded.circle_id ELSE work.circle_id END,
			circle_name            = CASE WHEN excluded.circle_name != '' THEN excluded.circle_name ELSE work.circle_name END,
			release_date           = CASE WHEN excluded.release_date != '' THEN excluded.release_date ELSE work.release_date END,
			create_date            = CASE WHEN excluded.create_date != '' THEN excluded.create_date ELSE work.create_date END,
			age_category           = CASE WHEN excluded.age_category > 0 THEN excluded.age_category ELSE work.age_category END,
			work_type              = CASE WHEN excluded.work_type != '' THEN excluded.work_type ELSE work.work_type END,
			series_id              = CASE WHEN excluded.series_id != '' THEN excluded.series_id ELSE work.series_id END,
			series_name            = CASE WHEN excluded.series_name != '' THEN excluded.series_name ELSE work.series_name END,
			price                  = CASE WHEN excluded.price > 0 THEN excluded.price ELSE work.price END,
			file_size              = CASE WHEN excluded.file_size > 0 THEN excluded.file_size ELSE work.file_size END,
			duration               = CASE WHEN excluded.duration > 0 THEN excluded.duration ELSE work.duration END,
			has_subtitle           = CASE WHEN excluded.has_subtitle > 0 THEN 1 ELSE work.has_subtitle END,
			rating                 = CASE WHEN excluded.rating > 0 THEN excluded.rating ELSE work.rating END,
			rate_count             = CASE WHEN excluded.rate_count > 0 THEN excluded.rate_count ELSE work.rate_count END,
			review_count           = CASE WHEN excluded.review_count > 0 THEN excluded.review_count ELSE work.review_count END,
			dl_count               = CASE WHEN excluded.dl_count > 0 THEN excluded.dl_count ELSE work.dl_count END,
			cover_path             = ?,
			cover_url              = CASE WHEN excluded.cover_url != '' THEN excluded.cover_url ELSE work.cover_url END,
			sam_cover_url          = CASE WHEN excluded.sam_cover_url != '' THEN excluded.sam_cover_url ELSE work.sam_cover_url END,
			thumbnail_cover_url    = CASE WHEN excluded.thumbnail_cover_url != '' THEN excluded.thumbnail_cover_url ELSE work.thumbnail_cover_url END,
			original_workno        = CASE WHEN excluded.original_workno != '' THEN excluded.original_workno ELSE work.original_workno END,
			source_type            = CASE WHEN excluded.source_type != '' THEN excluded.source_type ELSE work.source_type END,
			source_url             = CASE WHEN excluded.source_url != '' THEN excluded.source_url ELSE work.source_url END,
			work_attributes        = CASE WHEN excluded.work_attributes != '' THEN excluded.work_attributes ELSE work.work_attributes END,
			language_editions_json = CASE WHEN excluded.language_editions_json != '' THEN excluded.language_editions_json ELSE work.language_editions_json END,
			other_editions_json    = CASE WHEN excluded.other_editions_json != '' THEN excluded.other_editions_json ELSE work.other_editions_json END,
			translation_info_json  = CASE WHEN excluded.translation_info_json != '' THEN excluded.translation_info_json ELSE work.translation_info_json END,
			rate_detail_json       = CASE WHEN excluded.rate_detail_json != '' THEN excluded.rate_detail_json ELSE work.rate_detail_json END,
			rank_json              = CASE WHEN excluded.rank_json != '' THEN excluded.rank_json ELSE work.rank_json END,
			needs_rescrape         = 0,
			scraped_at             = excluded.scraped_at
	`,
		wno, meta.AsmrOneID, title, meta.CircleID, meta.CircleName, meta.ReleaseDate, meta.CreateDate,
		meta.AgeCategory, meta.WorkType, meta.SeriesID, meta.SeriesName, meta.Price, meta.FileSize, meta.Duration,
		hasSubInt, meta.Rating, meta.RateCount, meta.ReviewCount, meta.DLCount, coverToSet, meta.CoverURL,
		meta.SamCoverURL, meta.ThumbnailCoverURL, meta.OriginalWorkno, meta.SourceType, meta.SourceURL,
		meta.WorkAttributes, meta.LanguageEditionsJSON, meta.OtherEditionsJSON, meta.TranslationInfoJSON,
		meta.RateDetailJSON, meta.RankJSON, now, time.Now().Unix(), coverToSet,
	)
	if err != nil {
		return alreadyExisted, err
	}

	roleMap := []struct {
		role  string
		names []string
	}{
		{"va", meta.VAs},
		{"scenario", meta.Scenarios},
		{"illust", meta.Illusts},
		{"music", meta.Musics},
	}
	for _, rg := range roleMap {
		if len(rg.names) == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM work_person WHERE workno = ? AND role = ?`, wno, rg.role); err != nil {
			return alreadyExisted, err
		}
		for _, rawName := range rg.names {
			name := strings.TrimSpace(rawName)
			if name == "" {
				continue
			}
			pid, err := upsertPersonTx(ctx, tx, name)
			if err != nil {
				return alreadyExisted, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_person (workno, person_id, role) VALUES (?, ?, ?)`, wno, pid, rg.role); err != nil {
				return alreadyExisted, err
			}
		}
	}

	if len(meta.Tags) > 0 {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM work_tag WHERE workno = ? AND tag_id IN (SELECT id FROM tag WHERE source = 'dlsite')
		`, wno); err != nil {
			return alreadyExisted, err
		}
		for _, rawTag := range meta.Tags {
			tname := strings.TrimSpace(rawTag)
			if tname == "" {
				continue
			}
			tid, err := upsertTagTx(ctx, tx, tname, "dlsite")
			if err != nil {
				return alreadyExisted, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_tag (workno, tag_id) VALUES (?, ?)`, wno, tid); err != nil {
				return alreadyExisted, err
			}
		}
	}

	return alreadyExisted, nil
}

func upsertPersonTx(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO person (name) VALUES (?)`, name); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM person WHERE name = ?`, name).Scan(&id)
	return id, err
}

func upsertTagTx(ctx context.Context, tx *sql.Tx, name, source string) (int64, error) {
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO tag (name, source) VALUES (?, ?)`, name, source); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM tag WHERE name = ?`, name).Scan(&id)
	return id, err
}

// ListUnscrapedWorknos returns worknos needing DLsite metadata scrape (unscraped or marked needs_rescrape).
func (s *Store) ListUnscrapedWorknos(ctx context.Context, forceAll bool) ([]string, error) {
	q := `SELECT workno FROM work WHERE scraped_at IS NULL OR scraped_at = 0 OR needs_rescrape = 1 ORDER BY workno ASC`
	if forceAll {
		q = `SELECT workno FROM work ORDER BY workno ASC`
	}
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func buildAgeCondition(alias, ageMode string) string {
	col := "age_category"
	if alias != "" {
		col = alias + ".age_category"
	}
	switch ageMode {
	case "general", "":
		// Strict all-ages only (§3.4 & §12: completely hide non-all-ages works by default)
		return fmt.Sprintf("%s = 1", col)
	case "sfw_unrated":
		return fmt.Sprintf("COALESCE(%s, 0) IN (0, 1)", col)
	case "r15":
		return fmt.Sprintf("COALESCE(%s, 0) IN (1, 2)", col)
	case "r18":
		return fmt.Sprintf("%s = 3", col)
	case "all":
		return "1=1"
	default:
		return fmt.Sprintf("%s = 1", col)
	}
}

// ListWorks lists works with filtering, age isolation, sorting, and pagination.
func (s *Store) ListWorks(ctx context.Context, opts ListWorksOptions) ([]Work, int, error) {
	if opts.UserID <= 0 {
		opts.UserID = 1
	}
	if opts.Page <= 0 {
		opts.Page = 1
	}
	if opts.PageSize <= 0 || opts.PageSize > 200 {
		opts.PageSize = 40
	}

	var where []string
	var args []any

	where = append(where, buildAgeCondition("w", opts.AgeMode))

	if opts.Circle != "" {
		where = append(where, `(w.circle_name = ? OR w.circle_id = ?)`)
		args = append(args, opts.Circle, opts.Circle)
	}
	if opts.VA != "" {
		where = append(where, `EXISTS (
			SELECT 1 FROM work_person wp JOIN person p ON p.id = wp.person_id
			WHERE wp.workno = w.workno AND wp.role = 'va' AND p.name = ?
		)`)
		args = append(args, opts.VA)
	}
	if opts.Tag != "" {
		where = append(where, `EXISTS (
			SELECT 1 FROM work_tag wt JOIN tag t ON t.id = wt.tag_id
			WHERE wt.workno = w.workno AND t.name = ?
		)`)
		args = append(args, opts.Tag)
	}
	if opts.Progress != "" {
		where = append(where, `EXISTS (
			SELECT 1 FROM user_state us WHERE us.workno = w.workno AND us.user_id = ? AND us.progress = ?
		)`)
		args = append(args, opts.UserID, opts.Progress)
	}
	if opts.StorageID > 0 {
		where = append(where, `EXISTS (
			SELECT 1 FROM work_path wpth WHERE wpth.workno = w.workno AND wpth.storage_id = ?
		)`)
		args = append(args, opts.StorageID)
	}
	if opts.SubtitleOnly {
		where = append(where, `COALESCE(w.has_subtitle, 0) = 1`)
	}
	if opts.Worknos != nil {
		if len(opts.Worknos) == 0 {
			return []Work{}, 0, nil
		}
		ph := make([]string, len(opts.Worknos))
		for i, wno := range opts.Worknos {
			ph[i] = "?"
			args = append(args, wno)
		}
		where = append(where, fmt.Sprintf("w.workno IN (%s)", strings.Join(ph, ",")))
	}
	if q := strings.TrimSpace(opts.QueryLike); q != "" {
		like := "%" + q + "%"
		where = append(where, `(
			w.workno LIKE ? OR w.title LIKE ? OR COALESCE(w.circle_name, '') LIKE ? OR COALESCE(w.series_name, '') LIKE ?
			OR EXISTS (SELECT 1 FROM work_person wp JOIN person p ON p.id = wp.person_id WHERE wp.workno = w.workno AND p.name LIKE ?)
			OR EXISTS (SELECT 1 FROM work_tag wt JOIN tag t ON t.id = wt.tag_id WHERE wt.workno = w.workno AND t.name LIKE ?)
		)`)
		args = append(args, like, like, like, like, like, like)
	}

	whereSQL := strings.Join(where, " AND ")

	var total int
	countQuery := "SELECT COUNT(*) FROM work w WHERE " + whereSQL
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return []Work{}, 0, nil
	}

	dir := "DESC"
	if !opts.SortDesc {
		dir = "ASC"
	}
	orderExpr := "COALESCE(w.release_date, '') " + dir + ", w.workno DESC"
	switch opts.SortBy {
	case "created_at":
		orderExpr = "COALESCE(w.created_at, 0) " + dir + ", w.workno DESC"
	case "dl_count":
		orderExpr = "COALESCE(w.dl_count, 0) " + dir + ", w.workno DESC"
	case "rating":
		orderExpr = "COALESCE(w.rating, 0) " + dir + ", w.workno DESC"
	case "price":
		orderExpr = "COALESCE(w.price, 0) " + dir + ", w.workno DESC"
	case "workno":
		orderExpr = "w.workno " + dir
	case "title":
		orderExpr = "w.title " + dir
	}

	offset := (opts.Page - 1) * opts.PageSize
	listArgs := append([]any{opts.UserID}, args...)
	listArgs = append(listArgs, opts.PageSize, offset)

	listQuery := fmt.Sprintf(`
		SELECT
			w.workno, COALESCE(w.asmrone_id, 0), w.title, COALESCE(w.circle_id, ''), COALESCE(w.circle_name, ''),
			COALESCE(w.release_date, ''), COALESCE(w.create_date, ''), COALESCE(w.age_category, 0), COALESCE(w.work_type, ''),
			COALESCE(w.series_id, ''), COALESCE(w.series_name, ''), COALESCE(w.price, 0),
			COALESCE(w.file_size, 0), COALESCE(w.duration, 0), COALESCE(w.has_subtitle, 0),
			COALESCE(w.rating, 0), COALESCE(w.rate_count, 0),
			COALESCE(w.review_count, 0), COALESCE(w.dl_count, 0), COALESCE(w.cover_path, ''),
			COALESCE(w.cover_url, ''), COALESCE(w.sam_cover_url, ''), COALESCE(w.thumbnail_cover_url, ''),
			COALESCE(w.original_workno, ''), COALESCE(w.needs_rescrape, 0), COALESCE(w.scraped_at, 0),
			COALESCE(w.created_at, 0),
			us.rating, COALESCE(us.progress, ''),
			(SELECT COUNT(*) FROM track tr WHERE tr.workno = w.workno)
		FROM work w
		LEFT JOIN user_state us ON us.workno = w.workno AND us.user_id = ?
		WHERE %s
		ORDER BY %s
		LIMIT ? OFFSET ?
	`, whereSQL, orderExpr)

	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	var works []Work
	var worknos []string
	idxByWorkno := make(map[string]int)
	for rows.Next() {
		var w Work
		var needs, hasSub int
		var uRating sql.NullInt64
		if err := rows.Scan(
			&w.Workno, &w.AsmrOneID, &w.Title, &w.CircleID, &w.CircleName,
			&w.ReleaseDate, &w.CreateDate, &w.AgeCategory, &w.WorkType,
			&w.SeriesID, &w.SeriesName, &w.Price,
			&w.FileSize, &w.Duration, &hasSub,
			&w.Rating, &w.RateCount,
			&w.ReviewCount, &w.DLCount, &w.CoverPath,
			&w.CoverURL, &w.SamCoverURL, &w.ThumbnailCoverURL,
			&w.OriginalWorkno, &needs, &w.ScrapedAt,
			&w.CreatedAt, &uRating, &w.Progress, &w.TrackCount,
		); err != nil {
			rows.Close()
			return nil, 0, err
		}
		w.HasSubtitle = hasSub != 0
		w.NeedsRescrape = needs != 0
		if uRating.Valid {
			r := int(uRating.Int64)
			w.UserRating = &r
		}
		w.VAs = []string{}
		w.Tags = []Tag{}
		idxByWorkno[w.Workno] = len(works)
		worknos = append(worknos, w.Workno)
		works = append(works, w)
	}
	rows.Close()

	if err := s.populateWorkRelations(ctx, works, worknos, idxByWorkno); err != nil {
		return nil, 0, err
	}
	return works, total, nil
}

func (s *Store) populateWorkRelations(ctx context.Context, works []Work, worknos []string, idxByWorkno map[string]int) error {
	if len(worknos) == 0 {
		return nil
	}
	ph := make([]string, len(worknos))
	args := make([]any, len(worknos))
	for i, w := range worknos {
		ph[i] = "?"
		args[i] = w
	}
	inClause := strings.Join(ph, ",")

	vaRows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT wp.workno, p.name
		FROM work_person wp
		JOIN person p ON p.id = wp.person_id
		WHERE wp.role = 'va' AND wp.workno IN (%s)
		ORDER BY p.id ASC
	`, inClause), args...)
	if err != nil {
		return err
	}
	for vaRows.Next() {
		var wno, name string
		if err := vaRows.Scan(&wno, &name); err != nil {
			vaRows.Close()
			return err
		}
		if idx, ok := idxByWorkno[wno]; ok {
			works[idx].VAs = append(works[idx].VAs, name)
		}
	}
	vaRows.Close()

	tagRows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT wt.workno, t.id, t.name, t.source
		FROM work_tag wt
		JOIN tag t ON t.id = wt.tag_id
		WHERE wt.workno IN (%s)
		ORDER BY CASE WHEN t.source = 'user' THEN 0 ELSE 1 END, t.id ASC
	`, inClause), args...)
	if err != nil {
		return err
	}
	for tagRows.Next() {
		var wno string
		var tg Tag
		if err := tagRows.Scan(&wno, &tg.ID, &tg.Name, &tg.Source); err != nil {
			tagRows.Close()
			return err
		}
		if idx, ok := idxByWorkno[wno]; ok {
			works[idx].Tags = append(works[idx].Tags, tg)
		}
	}
	tagRows.Close()
	return nil
}

// GetWorkDetail fetches full metadata, hierarchical track tree, and per-track play_position (§4.3 & §9.5).
func (s *Store) GetWorkDetail(ctx context.Context, workno string, userID int64) (*WorkDetail, error) {
	wno := dlsite.NormalizeWorkno(workno)
	if wno == "" {
		wno = strings.ToUpper(strings.TrimSpace(workno))
	}
	if userID <= 0 {
		userID = 1
	}

	var d WorkDetail
	var needs, hasSub int
	err := s.db.QueryRowContext(ctx, `
		SELECT
			workno, COALESCE(asmrone_id, 0), title, COALESCE(circle_id, ''), COALESCE(circle_name, ''),
			COALESCE(release_date, ''), COALESCE(create_date, ''), COALESCE(age_category, 0), COALESCE(work_type, ''),
			COALESCE(series_id, ''), COALESCE(series_name, ''), COALESCE(price, 0),
			COALESCE(file_size, 0), COALESCE(duration, 0), COALESCE(has_subtitle, 0),
			COALESCE(rating, 0), COALESCE(rate_count, 0),
			COALESCE(review_count, 0), COALESCE(dl_count, 0), COALESCE(cover_path, ''),
			COALESCE(cover_url, ''), COALESCE(sam_cover_url, ''), COALESCE(thumbnail_cover_url, ''),
			COALESCE(original_workno, ''), COALESCE(source_type, ''), COALESCE(source_url, ''),
			COALESCE(work_attributes, ''), COALESCE(language_editions_json, ''), COALESCE(other_editions_json, ''),
			COALESCE(translation_info_json, ''), COALESCE(rate_detail_json, ''), COALESCE(rank_json, ''),
			COALESCE(remote_files_json, ''),
			COALESCE(needs_rescrape, 0), COALESCE(scraped_at, 0), COALESCE(created_at, 0)
		FROM work WHERE workno = ?
	`, wno).Scan(
		&d.Workno, &d.AsmrOneID, &d.Title, &d.CircleID, &d.CircleName,
		&d.ReleaseDate, &d.CreateDate, &d.AgeCategory, &d.WorkType,
		&d.SeriesID, &d.SeriesName, &d.Price,
		&d.FileSize, &d.Duration, &hasSub,
		&d.Rating, &d.RateCount,
		&d.ReviewCount, &d.DLCount, &d.CoverPath,
		&d.CoverURL, &d.SamCoverURL, &d.ThumbnailCoverURL,
		&d.OriginalWorkno, &d.SourceType, &d.SourceURL,
		&d.WorkAttributes, &d.LanguageEditionsJSON, &d.OtherEditionsJSON,
		&d.TranslationInfoJSON, &d.RateDetailJSON, &d.RankJSON,
		&d.RemoteFilesJSON,
		&needs, &d.ScrapedAt, &d.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.HasSubtitle = hasSub != 0
	d.NeedsRescrape = needs != 0
	d.VAs = []string{}
	d.Scenarios = []string{}
	d.Illusts = []string{}
	d.Musics = []string{}
	d.Tags = []Tag{}

	// Persons
	pRows, err := s.db.QueryContext(ctx, `
		SELECT wp.role, p.name FROM work_person wp JOIN person p ON p.id = wp.person_id
		WHERE wp.workno = ? ORDER BY p.id ASC
	`, wno)
	if err != nil {
		return nil, err
	}
	for pRows.Next() {
		var role, name string
		if err := pRows.Scan(&role, &name); err != nil {
			pRows.Close()
			return nil, err
		}
		switch role {
		case "va":
			d.VAs = append(d.VAs, name)
		case "scenario":
			d.Scenarios = append(d.Scenarios, name)
		case "illust":
			d.Illusts = append(d.Illusts, name)
		case "music":
			d.Musics = append(d.Musics, name)
		}
	}
	pRows.Close()

	// Tags
	tRows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.name, t.source FROM work_tag wt JOIN tag t ON t.id = wt.tag_id
		WHERE wt.workno = ? ORDER BY CASE WHEN t.source = 'user' THEN 0 ELSE 1 END, t.id ASC
	`, wno)
	if err != nil {
		return nil, err
	}
	for tRows.Next() {
		var tg Tag
		if err := tRows.Scan(&tg.ID, &tg.Name, &tg.Source); err != nil {
			tRows.Close()
			return nil, err
		}
		d.Tags = append(d.Tags, tg)
	}
	tRows.Close()

	// Paths
	pathRows, err := s.db.QueryContext(ctx, `
		SELECT wp.workno, wp.storage_id, COALESCE(st.name, ''), COALESCE(st.root, ''), wp.rel_path
		FROM work_path wp
		LEFT JOIN storage st ON st.id = wp.storage_id
		WHERE wp.workno = ?
		ORDER BY wp.storage_id ASC, wp.rel_path ASC
	`, wno)
	if err != nil {
		return nil, err
	}
	for pathRows.Next() {
		var p WorkPath
		if err := pathRows.Scan(&p.Workno, &p.StorageID, &p.StorageName, &p.StorageRoot, &p.RelPath); err != nil {
			pathRows.Close()
			return nil, err
		}
		d.Paths = append(d.Paths, p)
	}
	pathRows.Close()

	// Tracks + play_position (§9.5) — prioritize local tracks over remote tracks if both exist!
	trRows, err := s.db.QueryContext(ctx, `
		SELECT
			tr.id, tr.workno, tr.storage_id, tr.work_rel, tr.rel_path,
			COALESCE(tr.size, 0), tr.duration, COALESCE(tr.ord, 0),
			COALESCE(pp.position, 0),
			COALESCE(tr.stream_url, ''), COALESCE(tr.download_url, ''),
			COALESCE(tr.stream_low_url, ''), COALESCE(tr.subtitle_url, ''),
			COALESCE(tr.remote_hash, ''), COALESCE(st.kind, 'local')
		FROM track tr
		LEFT JOIN storage st ON st.id = tr.storage_id
		LEFT JOIN play_position pp ON pp.track_id = tr.id AND pp.user_id = ?
		WHERE tr.workno = ?
		ORDER BY CASE WHEN COALESCE(st.kind, 'local') = 'local' THEN 0 ELSE 1 END, tr.ord ASC, tr.rel_path ASC
	`, userID, wno)
	if err != nil {
		return nil, err
	}
	hasLocalTracks := false
	var allFetched []Track
	for trRows.Next() {
		var tr Track
		var dur sql.NullFloat64
		var stKind string
		if err := trRows.Scan(
			&tr.ID, &tr.Workno, &tr.StorageID, &tr.WorkRel, &tr.RelPath,
			&tr.Size, &dur, &tr.Ord, &tr.PlayPosition,
			&tr.StreamURL, &tr.DownloadURL, &tr.StreamLowURL, &tr.SubtitleURL,
			&tr.RemoteHash, &stKind,
		); err != nil {
			trRows.Close()
			return nil, err
		}
		if dur.Valid {
			v := dur.Float64
			tr.Duration = &v
		}
		tr.IsRemote = stKind == "asmrone" || tr.StreamURL != ""
		if !tr.IsRemote {
			hasLocalTracks = true
		}
		if tr.SubtitleURL != "" {
			tr.HasSubtitle = true
		}
		allFetched = append(allFetched, tr)
	}
	trRows.Close()

	// If local audio files exist for this work, show local tracks so local storage always wins; otherwise show remote tracks.
	for _, tr := range allFetched {
		if hasLocalTracks && tr.IsRemote {
			continue
		}
		d.Tracks = append(d.Tracks, tr)
	}
	d.TrackCount = len(d.Tracks)
	d.Tree = BuildTrackTree(d.Tracks)

	// User state
	var us UserState
	var uRating sql.NullInt64
	var uReview, uProgress sql.NullString
	var uUpdated sql.NullInt64
	stateErr := s.db.QueryRowContext(ctx, `
		SELECT user_id, workno, rating, review, progress, updated_at
		FROM user_state WHERE user_id = ? AND workno = ?
	`, userID, wno).Scan(&us.UserID, &us.Workno, &uRating, &uReview, &uProgress, &uUpdated)
	if stateErr == nil {
		if uRating.Valid {
			r := int(uRating.Int64)
			us.Rating = &r
			d.UserRating = &r
		}
		us.Review = uReview.String
		us.Progress = uProgress.String
		d.Progress = us.Progress
		us.UpdatedAt = uUpdated.Int64
		d.State = &us
	}

	return &d, nil
}

// BuildTrackTree builds the nested folder/audio tree preserving original hierarchy (§4.3).
func BuildTrackTree(tracks []Track) []*TrackTreeNode {
	root := &TrackTreeNode{Name: "", Type: "folder"}
	for i := range tracks {
		tr := tracks[i]
		clean := strings.ReplaceAll(tr.RelPath, "\\", "/")
		parts := strings.Split(clean, "/")
		curr := root
		var prefix []string
		for idx, part := range parts {
			if part == "" {
				continue
			}
			prefix = append(prefix, part)
			isLeaf := idx == len(parts)-1
			if isLeaf {
				trCopy := tr
				curr.Children = append(curr.Children, &TrackTreeNode{
					Name:    part,
					Type:    "audio",
					RelPath: strings.Join(prefix, "/"),
					Track:   &trCopy,
				})
			} else {
				var next *TrackTreeNode
				for _, ch := range curr.Children {
					if ch.Type == "folder" && ch.Name == part {
						next = ch
						break
					}
				}
				if next == nil {
					next = &TrackTreeNode{
						Name:    part,
						Type:    "folder",
						RelPath: strings.Join(prefix, "/"),
					}
					curr.Children = append(curr.Children, next)
				}
				curr = next
			}
		}
	}
	sortTreeNodes(root.Children)
	return root.Children
}

func sortTreeNodes(nodes []*TrackTreeNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		// Folders first, then tracks in ord/natural order
		if nodes[i].Type != nodes[j].Type {
			return nodes[i].Type == "folder"
		}
		if nodes[i].Track != nil && nodes[j].Track != nil && nodes[i].Track.Ord != nodes[j].Track.Ord {
			return nodes[i].Track.Ord < nodes[j].Track.Ord
		}
		return nodes[i].Name < nodes[j].Name
	})
	for _, n := range nodes {
		if len(n.Children) > 0 {
			sortTreeNodes(n.Children)
		}
	}
}

// ResolvedTrack holds full storage path details needed for audio streaming and subtitle matching.
type ResolvedTrack struct {
	Track
	StorageKind string
	StorageRoot string
}

func (s *Store) ResolveTrack(ctx context.Context, trackID int64) (*ResolvedTrack, error) {
	var rt ResolvedTrack
	var dur sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		SELECT
			tr.id, tr.workno, tr.storage_id, tr.work_rel, tr.rel_path,
			COALESCE(tr.size, 0), tr.duration, COALESCE(tr.ord, 0),
			COALESCE(tr.stream_url, ''), COALESCE(tr.download_url, ''),
			COALESCE(tr.stream_low_url, ''), COALESCE(tr.subtitle_url, ''),
			COALESCE(tr.remote_hash, ''),
			COALESCE(st.kind, 'local'), COALESCE(st.root, '')
		FROM track tr
		LEFT JOIN storage st ON st.id = tr.storage_id
		WHERE tr.id = ?
	`, trackID).Scan(
		&rt.ID, &rt.Workno, &rt.StorageID, &rt.WorkRel, &rt.RelPath,
		&rt.Size, &dur, &rt.Ord,
		&rt.StreamURL, &rt.DownloadURL, &rt.StreamLowURL, &rt.SubtitleURL, &rt.RemoteHash,
		&rt.StorageKind, &rt.StorageRoot,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if dur.Valid {
		v := dur.Float64
		rt.Duration = &v
	}
	rt.IsRemote = rt.StorageKind == "asmrone" || rt.StreamURL != ""
	return &rt, nil
}

// RemoteTrackInput represents a remote track discovered from asmr.one /api/tracks/{id}.
type RemoteTrackInput struct {
	RelPath      string
	Size         int64
	Duration     float64
	Ord          int
	RemoteHash   string
	StreamURL    string
	DownloadURL  string
	StreamLowURL string
	SubtitleURL  string
}

// SaveRemoteTracks persists remote asmr.one audio tracks and bonus file JSON under a dedicated
// `kind = 'asmrone'` storage without touching any local storage tracks.
func (s *Store) SaveRemoteTracks(ctx context.Context, workno string, tracks []RemoteTrackInput, remoteFilesJSON string) error {
	wno := dlsite.NormalizeWorkno(workno)
	if wno == "" {
		return fmt.Errorf("invalid workno %q", workno)
	}
	st, err := s.UpsertStorage(ctx, "asmrone", "asmr.one", "https://asmr.one", `{"remote":true}`)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	hasSub := 0
	seenRel := make(map[string]bool, len(tracks))
	for _, tr := range tracks {
		seenRel[tr.RelPath] = true
		if tr.SubtitleURL != "" {
			hasSub = 1
		}
		var durArg any
		if tr.Duration > 0 {
			durArg = tr.Duration
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO track (
				workno, storage_id, work_rel, rel_path, size, duration, ord,
				stream_url, download_url, stream_low_url, subtitle_url, remote_hash
			) VALUES (?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(workno, storage_id, work_rel, rel_path) DO UPDATE SET
				size           = excluded.size,
				duration       = COALESCE(excluded.duration, track.duration),
				ord            = excluded.ord,
				stream_url     = excluded.stream_url,
				download_url   = excluded.download_url,
				stream_low_url = excluded.stream_low_url,
				subtitle_url   = excluded.subtitle_url,
				remote_hash    = excluded.remote_hash
		`, wno, st.ID, tr.RelPath, tr.Size, durArg, tr.Ord, tr.StreamURL, tr.DownloadURL, tr.StreamLowURL, tr.SubtitleURL, tr.RemoteHash); err != nil {
			return err
		}
	}

	// Prune removed remote tracks for this work while keeping track IDs stable for play_position!
	rows, err := tx.QueryContext(ctx, `SELECT id, rel_path FROM track WHERE workno = ? AND storage_id = ?`, wno, st.ID)
	if err != nil {
		return err
	}
	var staleIDs []int64
	for rows.Next() {
		var id int64
		var rp string
		if rows.Scan(&id, &rp) == nil && !seenRel[rp] {
			staleIDs = append(staleIDs, id)
		}
	}
	rows.Close()
	for _, id := range staleIDs {
		_, _ = tx.ExecContext(ctx, `DELETE FROM track WHERE id = ?`, id)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE work SET
			remote_files_json = ?,
			has_subtitle = CASE WHEN ? = 1 THEN 1 ELSE has_subtitle END
		WHERE workno = ?
	`, remoteFilesJSON, hasSub, wno); err != nil {
		return err
	}

	return tx.Commit()
}

func (rt *ResolvedTrack) AbsFilePath() string {
	return filepath.Join(rt.StorageRoot, filepath.FromSlash(rt.WorkRel), filepath.FromSlash(rt.RelPath))
}

// UpdateTrackDuration stores browser-reported duration when header duration was unknown (§4.3).
func (s *Store) UpdateTrackDuration(ctx context.Context, trackID int64, duration float64) error {
	if duration <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE track SET duration = ? WHERE id = ? AND (duration IS NULL OR duration <= 0)`, duration, trackID)
	return err
}

// UpsertUserState updates user progress, rating, and review (§5 & §9).
func (s *Store) UpsertUserState(ctx context.Context, userID int64, workno string, rating *int, review *string, progress *string) (*UserState, error) {
	if userID <= 0 {
		userID = 1
	}
	wno := strings.ToUpper(strings.TrimSpace(workno))
	if progress != nil && !ValidProgress[*progress] {
		return nil, fmt.Errorf("invalid progress value %q", *progress)
	}
	now := time.Now().Unix()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO user_state (user_id, workno, updated_at) VALUES (?, ?, ?)
	`, userID, wno, now); err != nil {
		return nil, err
	}

	if rating != nil {
		var rVal any
		if *rating > 0 {
			rVal = *rating
		}
		if _, err := tx.ExecContext(ctx, `UPDATE user_state SET rating = ?, updated_at = ? WHERE user_id = ? AND workno = ?`, rVal, now, userID, wno); err != nil {
			return nil, err
		}
	}
	if review != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE user_state SET review = ?, updated_at = ? WHERE user_id = ? AND workno = ?`, *review, now, userID, wno); err != nil {
			return nil, err
		}
	}
	if progress != nil {
		var pVal any
		if *progress != "" {
			pVal = *progress
		}
		if _, err := tx.ExecContext(ctx, `UPDATE user_state SET progress = ?, updated_at = ? WHERE user_id = ? AND workno = ?`, pVal, now, userID, wno); err != nil {
			return nil, err
		}
	}

	var us UserState
	var uRating sql.NullInt64
	var uReview, uProgress sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT user_id, workno, rating, COALESCE(review, ''), COALESCE(progress, ''), COALESCE(updated_at, 0) FROM user_state WHERE user_id = ? AND workno = ?`, userID, wno).
		Scan(&us.UserID, &us.Workno, &uRating, &uReview, &uProgress, &us.UpdatedAt); err != nil {
		return nil, err
	}
	if uRating.Valid {
		r := int(uRating.Int64)
		us.Rating = &r
	}
	us.Review = uReview.String
	us.Progress = uProgress.String
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &us, nil
}

// SetWorkUserTags replaces user-defined tags (`tag.source = 'user'`) on a work (§5 & §9).
func (s *Store) SetWorkUserTags(ctx context.Context, workno string, userTags []string) ([]Tag, error) {
	wno := strings.ToUpper(strings.TrimSpace(workno))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM work_tag WHERE workno = ? AND tag_id IN (SELECT id FROM tag WHERE source = 'user')
	`, wno); err != nil {
		return nil, err
	}

	for _, raw := range userTags {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		// Check if tag already exists (e.g. dlsite or user)
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM tag WHERE name = ?`, name).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			res, ierr := tx.ExecContext(ctx, `INSERT INTO tag (name, source) VALUES (?, 'user')`, name)
			if ierr != nil {
				return nil, ierr
			}
			id, _ = res.LastInsertId()
		} else if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_tag (workno, tag_id) VALUES (?, ?)`, wno, id); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.name, t.source FROM work_tag wt JOIN tag t ON t.id = wt.tag_id
		WHERE wt.workno = ? ORDER BY CASE WHEN t.source = 'user' THEN 0 ELSE 1 END, t.id ASC
	`, wno)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tag
	for rows.Next() {
		var tg Tag
		if err := rows.Scan(&tg.ID, &tg.Name, &tg.Source); err != nil {
			return nil, err
		}
		out = append(out, tg)
	}
	return out, rows.Err()
}

// SearchDocument holds flattened work fields for Bleve indexing (§6).
type SearchDocument struct {
	Workno     string   `json:"workno"`
	Title      string   `json:"title"`
	CircleName string   `json:"circle_name"`
	SeriesName string   `json:"series_name"`
	VAs        []string `json:"vas"`
	Staff      []string `json:"staff"`
	Tags       []string `json:"tags"`
}

func (s *Store) GetSearchDocument(ctx context.Context, workno string) (*SearchDocument, error) {
	var doc SearchDocument
	err := s.db.QueryRowContext(ctx, `
		SELECT workno, title, COALESCE(circle_name, ''), COALESCE(series_name, '')
		FROM work WHERE workno = ?
	`, workno).Scan(&doc.Workno, &doc.Title, &doc.CircleName, &doc.SeriesName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	pRows, err := s.db.QueryContext(ctx, `
		SELECT wp.role, p.name FROM work_person wp JOIN person p ON p.id = wp.person_id WHERE wp.workno = ?
	`, workno)
	if err != nil {
		return nil, err
	}
	for pRows.Next() {
		var role, name string
		if err := pRows.Scan(&role, &name); err == nil {
			if role == "va" {
				doc.VAs = append(doc.VAs, name)
			} else {
				doc.Staff = append(doc.Staff, name)
			}
		}
	}
	pRows.Close()

	tRows, err := s.db.QueryContext(ctx, `
		SELECT t.name FROM work_tag wt JOIN tag t ON t.id = wt.tag_id WHERE wt.workno = ?
	`, workno)
	if err != nil {
		return nil, err
	}
	for tRows.Next() {
		var name string
		if err := tRows.Scan(&name); err == nil {
			doc.Tags = append(doc.Tags, name)
		}
	}
	tRows.Close()
	return &doc, nil
}

func (s *Store) IterAllSearchDocuments(ctx context.Context, fn func(SearchDocument) error) error {
	rows, err := s.db.QueryContext(ctx, `SELECT workno FROM work ORDER BY workno ASC`)
	if err != nil {
		return err
	}
	var worknos []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			rows.Close()
			return err
		}
		worknos = append(worknos, w)
	}
	rows.Close()

	for _, wno := range worknos {
		doc, err := s.GetSearchDocument(ctx, wno)
		if err != nil {
			continue
		}
		if err := fn(*doc); err != nil {
			return err
		}
	}
	return nil
}

// VacuumAndTruncateWAL forces SQLite to rewrite the database pages AND truncate any WAL pages
// so deleted rows leave zero residual bytes on disk (§9.6 & §12).
func (s *Store) VacuumAndTruncateWAL(ctx context.Context) error {
	_, _ = s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE);`)
	if _, err := s.db.ExecContext(ctx, `VACUUM;`); err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE);`)
	return nil
}

// User & Session management (§9)
func (s *Store) UpsertUser(ctx context.Context, username, passwordHash string, isAdmin, mustReset bool) (*User, error) {
	now := time.Now().Unix()
	adminInt := 0
	if isAdmin {
		adminInt = 1
	}
	resetInt := 0
	if mustReset {
		resetInt = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app_user (username, password_hash, is_admin, must_reset_password, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(username) DO UPDATE SET
			password_hash       = CASE WHEN excluded.password_hash != '' THEN excluded.password_hash ELSE app_user.password_hash END,
			is_admin            = excluded.is_admin,
			must_reset_password = excluded.must_reset_password
	`, username, passwordHash, adminInt, resetInt, now)
	if err != nil {
		return nil, err
	}
	return s.GetUserByUsername(ctx, username)
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	var u User
	var adminInt, resetInt int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, is_admin, must_reset_password, created_at
		FROM app_user WHERE username = ?
	`, username).Scan(&u.ID, &u.Username, &u.PasswordHash, &adminInt, &resetInt, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	u.IsAdmin = adminInt != 0
	u.MustResetPassword = resetInt != 0
	return &u, err
}

func (s *Store) GetUserByID(ctx context.Context, id int64) (*User, error) {
	var u User
	var adminInt, resetInt int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, username, password_hash, is_admin, must_reset_password, created_at
		FROM app_user WHERE id = ?
	`, id).Scan(&u.ID, &u.Username, &u.PasswordHash, &adminInt, &resetInt, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	u.IsAdmin = adminInt != 0
	u.MustResetPassword = resetInt != 0
	return &u, err
}

func (s *Store) UpdateUserPassword(ctx context.Context, userID int64, passwordHash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE app_user SET password_hash = ?, must_reset_password = 0 WHERE id = ?`, passwordHash, userID)
	return err
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, is_admin, must_reset_password, created_at FROM app_user ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var a, r int
		if err := rows.Scan(&u.ID, &u.Username, &a, &r, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.IsAdmin = a != 0
		u.MustResetPassword = r != 0
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) CreateSession(ctx context.Context, token string, userID int64, expiresAt int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_session (token, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		token, userID, expiresAt, time.Now().Unix())
	return err
}

func (s *Store) GetUserBySession(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	var uid int64
	var exp int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id, expires_at FROM user_session WHERE token = ?`, token).Scan(&uid, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if exp < time.Now().Unix() {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM user_session WHERE token = ?`, token)
		return nil, ErrNotFound
	}
	return s.GetUserByID(ctx, uid)
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM user_session WHERE token = ?`, token)
	return err
}

// FacetCount represents filter sidebar counts (circles, VAs, tags).
type FacetCount struct {
	Name   string `json:"name"`
	Source string `json:"source,omitempty"`
	Count  int    `json:"count"`
}

func (s *Store) ListFacets(ctx context.Context, ageMode string) (circles, vas, tags []FacetCount, err error) {
	ageCond := buildAgeCondition("w", ageMode)

	cRows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT w.circle_name, COUNT(*) AS cnt FROM work w
		WHERE COALESCE(w.circle_name, '') != '' AND %s
		GROUP BY w.circle_name ORDER BY cnt DESC, w.circle_name ASC LIMIT 100
	`, ageCond))
	if err != nil {
		return nil, nil, nil, err
	}
	for cRows.Next() {
		var fc FacetCount
		if err := cRows.Scan(&fc.Name, &fc.Count); err == nil {
			circles = append(circles, fc)
		}
	}
	cRows.Close()

	vRows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT p.name, COUNT(*) AS cnt
		FROM work_person wp
		JOIN person p ON p.id = wp.person_id
		JOIN work w ON w.workno = wp.workno
		WHERE wp.role = 'va' AND %s
		GROUP BY p.id ORDER BY cnt DESC, p.name ASC LIMIT 100
	`, ageCond))
	if err != nil {
		return nil, nil, nil, err
	}
	for vRows.Next() {
		var fc FacetCount
		if err := vRows.Scan(&fc.Name, &fc.Count); err == nil {
			vas = append(vas, fc)
		}
	}
	vRows.Close()

	tRows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT t.name, t.source, COUNT(*) AS cnt
		FROM work_tag wt
		JOIN tag t ON t.id = wt.tag_id
		JOIN work w ON w.workno = wt.workno
		WHERE %s
		GROUP BY t.id ORDER BY CASE WHEN t.source = 'user' THEN 0 ELSE 1 END, cnt DESC, t.name ASC LIMIT 150
	`, ageCond))
	if err != nil {
		return nil, nil, nil, err
	}
	for tRows.Next() {
		var fc FacetCount
		if err := tRows.Scan(&fc.Name, &fc.Source, &fc.Count); err == nil {
			tags = append(tags, fc)
		}
	}
	tRows.Close()
	return circles, vas, tags, nil
}
