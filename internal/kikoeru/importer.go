package kikoeru

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SiIverAsh/otoha/internal/db"
	"github.com/SiIverAsh/otoha/internal/dlsite"
	_ "modernc.org/sqlite"
)

const KnownKikoeruMigrationVersion = "20210502081522"

// ConflictRecord documents a work where kikoeru's t_work.id mismatched the actual workno on disk (§14.3).
type ConflictRecord struct {
	KikoeruID     int    `json:"kikoeru_id"`
	PaddedWorkno  string `json:"padded_workno"`
	DiskWorkno    string `json:"disk_workno"`
	Title         string `json:"title"`
	RootFolder    string `json:"root_folder"`
	Dir           string `json:"dir"`
	Reason        string `json:"reason"`
}

// ImportReport summarizes dry-run or committed kikoeru database migration (§14.5).
type ImportReport struct {
	SourceDB            string           `json:"source_db"`
	DryRun              bool             `json:"dry_run"`
	GeneratedAt         string           `json:"generated_at"`
	KnexLatestMigration string           `json:"knex_latest_migration,omitempty"`
	AzuseHistoryFound   bool             `json:"azuse_history_found"`
	SourceWorksTotal    int              `json:"source_works_total"`
	UniqueWorksImported int              `json:"unique_works_imported"`
	ConflictsDetected   int              `json:"conflicts_detected"`
	DuplicateCollapsed  int              `json:"duplicate_collapsed"`
	NeedsRescrapeCount  int              `json:"needs_rescrape_count"`
	UsersImported       int              `json:"users_imported"`
	ReviewsImported     int              `json:"reviews_imported"`
	HistoryImported     int              `json:"history_imported"`
	CoversLinked        int              `json:"covers_linked"`
	UnmappedRootFolders []string         `json:"unmapped_root_folders,omitempty"`
	Warnings            []string         `json:"warnings,omitempty"`
	RescrapeWorknos     []string         `json:"rescrape_worknos,omitempty"`
	Conflicts           []ConflictRecord `json:"conflicts,omitempty"`
	ReportPath          string           `json:"report_path,omitempty"`
}

// ImportOptions configures `otoha import-kikoeru` (§14.5).
type ImportOptions struct {
	SourceDBPath   string
	CoversDir      string
	DestCoversDir  string
	RootMap        map[string]string // kikoeru root_folder alias -> real local path
	DryRun         bool
	ReportFilePath string
}

// OpenReadOnlySQLite opens the kikoeru SQLite database in strict read-only query_only mode
// so the user's legacy database is guaranteed never to be modified (§14.5 & §12).
func OpenReadOnlySQLite(srcPath string) (*sql.DB, error) {
	abs, err := filepath.Abs(srcPath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("source kikoeru db not found: %w", err)
	}
	slash := filepath.ToSlash(abs)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	dsn := (&url.URL{
		Scheme:   "file",
		Path:     slash,
		RawQuery: "mode=ro&_pragma=query_only(1)",
	}).String()

	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(`PRAGMA query_only = ON;`); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

type kikoeruWorkRow struct {
	ID          int
	RootFolder  string
	Dir         string
	Title       string
	CircleID    int
	CircleName  string
	NSFW        bool
	Release     string
	DLCount     int
	Price       int
	ReviewCount int
	RateCount   int
	RateAvg     float64
}

type kikoeruReviewRow struct {
	UserName   string
	WorkID     int
	Rating     sql.NullInt64
	ReviewText sql.NullString
	Progress   sql.NullString
	UpdatedAt  int64
}

type mergedWork struct {
	Workno        string
	Title         string
	CircleID      string
	CircleName    string
	AgeCategory   int
	ReleaseDate   string
	DLCount       int
	Price         int
	ReviewCount   int
	RateCount     int
	Rating        float64
	NeedsRescrape bool
	HasConflict   bool
	SourceCount   int
	Paths         [][2]string // [rootFolder, relDir]
	Tags          map[string]bool
	VAs           map[string]bool
	CoverSrc      string
}

type kikoeruUserRow struct {
	Name    string
	IsAdmin bool
}

type azuseHistRow struct {
	UserName  string
	WorkID    int
	PlayTime  float64
	UpdatedAt int64
}

// RunImport executes or dry-runs kikoeru -> Otoha migration and writes a detailed report file (§14).
func RunImport(ctx context.Context, dstStore *db.Store, opts ImportOptions) (*ImportReport, error) {
	srcConn, err := OpenReadOnlySQLite(opts.SourceDBPath)
	if err != nil {
		return nil, err
	}
	defer srcConn.Close()

	report := &ImportReport{
		SourceDB:    opts.SourceDBPath,
		DryRun:      opts.DryRun,
		GeneratedAt: time.Now().Format(time.RFC3339),
	}

	// 1. Validate knex_migrations (§14.5: warn on unknown version rather than hard-failing)
	if hasTable(ctx, srcConn, "knex_migrations") {
		var latest string
		_ = srcConn.QueryRowContext(ctx, `SELECT COALESCE(name, '') FROM knex_migrations ORDER BY id DESC LIMIT 1`).Scan(&latest)
		report.KnexLatestMigration = latest
		if latest != "" && !strings.Contains(latest, KnownKikoeruMigrationVersion) && !strings.HasPrefix(latest, "2020") && !strings.HasPrefix(latest, "2021") {
			report.Warnings = append(report.Warnings, fmt.Sprintf("unrecognized knex_migrations version %q (expected %s); proceeding with best-effort schema mapping", latest, KnownKikoeruMigrationVersion))
		}
	} else {
		report.Warnings = append(report.Warnings, "table knex_migrations not found in source database; proceeding with schema inspection")
	}

	// 2. Circles
	circles := make(map[int]string)
	if hasTable(ctx, srcConn, "t_circle") {
		rows, err := srcConn.QueryContext(ctx, `SELECT id, COALESCE(name, '') FROM t_circle`)
		if err != nil {
			return nil, fmt.Errorf("read t_circle: %w", err)
		}
		for rows.Next() {
			var id int
			var name string
			if err := rows.Scan(&id, &name); err == nil {
				circles[id] = strings.TrimSpace(name)
			}
		}
		rows.Close()
	}

	// 3. Tags & VAs by kikoeru work_id
	tagsByWorkID := make(map[int][]string)
	if hasTable(ctx, srcConn, "t_tag") && hasTable(ctx, srcConn, "r_tag_work") {
		rows, err := srcConn.QueryContext(ctx, `
			SELECT r.work_id, t.name FROM r_tag_work r JOIN t_tag t ON t.id = r.tag_id WHERE COALESCE(t.name, '') != ''
		`)
		if err == nil {
			for rows.Next() {
				var wid int
				var name string
				if rows.Scan(&wid, &name) == nil {
					tagsByWorkID[wid] = append(tagsByWorkID[wid], strings.TrimSpace(name))
				}
			}
			rows.Close()
		}
	}

	vasByWorkID := make(map[int][]string)
	if hasTable(ctx, srcConn, "t_va") && hasTable(ctx, srcConn, "r_va_work") {
		rows, err := srcConn.QueryContext(ctx, `
			SELECT r.work_id, v.name FROM r_va_work r JOIN t_va v ON v.id = r.va_id WHERE COALESCE(v.name, '') != ''
		`)
		if err == nil {
			for rows.Next() {
				var wid int
				var name string
				if rows.Scan(&wid, &name) == nil {
					// §14.2: discard UUID, keep clean VA name
					if trimmed := strings.TrimSpace(name); trimmed != "" && !strings.EqualFold(trimmed, "N/A") {
						vasByWorkID[wid] = append(vasByWorkID[wid], trimmed)
					}
				}
			}
			rows.Close()
		}
	}

	// 4. Read t_work and resolve canonical workno via disk/path §3.1 regex (§14.3)
	if !hasTable(ctx, srcConn, "t_work") {
		return nil, errors.New("source database does not contain t_work table")
	}
	wRows, err := srcConn.QueryContext(ctx, `
		SELECT
			id, COALESCE(root_folder, ''), COALESCE(dir, ''), COALESCE(title, ''),
			COALESCE(circle_id, 0), COALESCE(nsfw, 0), COALESCE("release", ''),
			COALESCE(dl_count, 0), COALESCE(price, 0), COALESCE(review_count, 0),
			COALESCE(rate_count, 0), COALESCE(rate_average_2dp, 0)
		FROM t_work ORDER BY id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("read t_work: %w", err)
	}
	var rawWorks []kikoeruWorkRow
	for wRows.Next() {
		var r kikoeruWorkRow
		var nsfwAny any
		if err := wRows.Scan(
			&r.ID, &r.RootFolder, &r.Dir, &r.Title,
			&r.CircleID, &nsfwAny, &r.Release,
			&r.DLCount, &r.Price, &r.ReviewCount,
			&r.RateCount, &r.RateAvg,
		); err != nil {
			wRows.Close()
			return nil, err
		}
		r.NSFW = parseSQLiteBool(nsfwAny)
		r.CircleName = circles[r.CircleID]
		rawWorks = append(rawWorks, r)
	}
	wRows.Close()
	report.SourceWorksTotal = len(rawWorks)

	mergedByWorkno := make(map[string]*mergedWork)
	resolvedWorknoByKikoeruID := make(map[int]string, len(rawWorks))
	unmappedSet := make(map[string]bool)

	for _, r := range rawWorks {
		padded := dlsite.WorknoFromKikoeruID(r.ID)
		mappedRoot := ""
		if opts.RootMap != nil {
			mappedRoot = opts.RootMap[r.RootFolder]
		}
		if mappedRoot == "" && r.RootFolder != "" {
			unmappedSet[r.RootFolder] = true
		}

		checkPath := r.Dir
		if mappedRoot != "" {
			checkPath = filepath.Join(mappedRoot, filepath.FromSlash(r.Dir))
		}
		diskWorkno := dlsite.ExtractWorknoFromPath(checkPath)
		finalWorkno := padded
		conflict := false

		if diskWorkno != "" && diskWorkno != padded {
			conflict = true
			finalWorkno = diskWorkno
			report.Conflicts = append(report.Conflicts, ConflictRecord{
				KikoeruID:    r.ID,
				PaddedWorkno: padded,
				DiskWorkno:   diskWorkno,
				Title:        r.Title,
				RootFolder:   r.RootFolder,
				Dir:          r.Dir,
				Reason:       fmt.Sprintf("kikoeru id=%d (%s) differs from disk path workno %s (likely 8-digit RJ truncation)", r.ID, padded, diskWorkno),
			})
		}

		resolvedWorknoByKikoeruID[r.ID] = finalWorkno
		is8Digit := len(finalWorkno) == 10

		age := dlsite.AgeGeneral
		if r.NSFW {
			age = dlsite.AgeR18
		}
		circleCode := ""
		if r.CircleID > 0 {
			circleCode = fmt.Sprintf("RG%05d", r.CircleID)
		}

		existing, dup := mergedByWorkno[finalWorkno]
		if dup {
			// §14.3 rule 3: multiple kikoeru records resolving to same workno -> discard polluted DLsite metadata and force rescrape!
			existing.SourceCount++
			existing.NeedsRescrape = true
			existing.HasConflict = existing.HasConflict || conflict
			existing.Title = finalWorkno
			existing.Tags = make(map[string]bool)
			existing.VAs = make(map[string]bool)
			existing.Paths = append(existing.Paths, [2]string{r.RootFolder, filepath.ToSlash(r.Dir)})
			report.DuplicateCollapsed++
			continue
		}

		mw := &mergedWork{
			Workno:        finalWorkno,
			Title:         r.Title,
			CircleID:      circleCode,
			CircleName:    r.CircleName,
			AgeCategory:   age,
			ReleaseDate:   r.Release,
			DLCount:       r.DLCount,
			Price:         r.Price,
			ReviewCount:   r.ReviewCount,
			RateCount:     r.RateCount,
			Rating:        r.RateAvg,
			NeedsRescrape: conflict || is8Digit, // §14.3 rule 4: force rescrape for all conflicts AND 8-digit works
			HasConflict:   conflict,
			SourceCount:   1,
			Paths:         [][2]string{{r.RootFolder, filepath.ToSlash(r.Dir)}},
			Tags:          make(map[string]bool),
			VAs:           make(map[string]bool),
		}
		if conflict {
			// Conflicted row had wrong 6-digit metadata from DLsite; keep fallback folder name until rescrape
			if base := filepath.Base(filepath.FromSlash(r.Dir)); base != "" && base != "." {
				mw.Title = base
			}
		} else {
			for _, tg := range tagsByWorkID[r.ID] {
				mw.Tags[tg] = true
			}
			for _, va := range vasByWorkID[r.ID] {
				mw.VAs[va] = true
			}
			if opts.CoversDir != "" {
				mw.CoverSrc = findKikoeruCover(opts.CoversDir, finalWorkno, padded)
			}
		}
		mergedByWorkno[finalWorkno] = mw
	}

	report.ConflictsDetected = len(report.Conflicts)
	report.UniqueWorksImported = len(mergedByWorkno)

	for rf := range unmappedSet {
		report.UnmappedRootFolders = append(report.UnmappedRootFolders, rf)
	}
	sort.Strings(report.UnmappedRootFolders)
	if len(report.UnmappedRootFolders) > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("root_folder aliases %v were not mapped via --root-map; stored placeholder root paths that can be updated in Storages", report.UnmappedRootFolders))
	}

	var orderedWorknos []string
	for wno, mw := range mergedByWorkno {
		orderedWorknos = append(orderedWorknos, wno)
		if mw.NeedsRescrape {
			report.RescrapeWorknos = append(report.RescrapeWorknos, wno)
		}
		if mw.CoverSrc != "" {
			report.CoversLinked++
		}
	}
	sort.Strings(orderedWorknos)
	sort.Strings(report.RescrapeWorknos)
	report.NeedsRescrapeCount = len(report.RescrapeWorknos)

	// 5. Read users (§14.4: NEVER migrate MD5 passwords)
	var usernames []kikoeruUserRow
	if hasTable(ctx, srcConn, "t_user") {
		uRows, err := srcConn.QueryContext(ctx, `SELECT name, COALESCE("group", '') FROM t_user`)
		if err == nil {
			for uRows.Next() {
				var name, grp string
				if uRows.Scan(&name, &grp) == nil && strings.TrimSpace(name) != "" {
					usernames = append(usernames, kikoeruUserRow{
						Name:    strings.TrimSpace(name),
						IsAdmin: strings.EqualFold(grp, "administrator") || strings.EqualFold(name, "admin"),
					})
				}
			}
			uRows.Close()
		}
	}
	report.UsersImported = len(usernames)

	// 6. Read reviews (`t_review` -> `user_state`, §14.2)
	var reviews []kikoeruReviewRow
	if hasTable(ctx, srcConn, "t_review") {
		rRows, err := srcConn.QueryContext(ctx, `
			SELECT COALESCE(user_name, 'admin'), work_id, rating, review_text, progress
			FROM t_review
		`)
		if err == nil {
			for rRows.Next() {
				var rv kikoeruReviewRow
				if rRows.Scan(&rv.UserName, &rv.WorkID, &rv.Rating, &rv.ReviewText, &rv.Progress) == nil {
					reviews = append(reviews, rv)
				}
			}
			rRows.Close()
		}
	}
	report.ReviewsImported = len(reviews)

	// 7. Probe optional azuse fork `t_history` table (§14.4 exception)
	var azuseRows []azuseHistRow
	if hasTable(ctx, srcConn, "t_history") {
		report.AzuseHistoryFound = true
		hRows, err := srcConn.QueryContext(ctx, `
			SELECT COALESCE(user_name, 'admin'), COALESCE(work_id, '0'), COALESCE(play_time, 0), COALESCE(updated_at, '')
			FROM t_history ORDER BY id ASC
		`)
		if err == nil {
			for hRows.Next() {
				var uname, widStr, updStr string
				var ptime float64
				if hRows.Scan(&uname, &widStr, &ptime, &updStr) == nil {
					wid, _ := strconv.Atoi(strings.TrimSpace(widStr))
					ts := parseFlexibleTime(updStr)
					azuseRows = append(azuseRows, azuseHistRow{
						UserName:  uname,
						WorkID:    wid,
						PlayTime:  ptime,
						UpdatedAt: ts,
					})
				}
			}
			hRows.Close()
		}
	}
	report.HistoryImported = len(azuseRows)

	// 8. If NOT dry-run, commit everything inside a SINGLE atomic transaction (§14.5)
	if !opts.DryRun && dstStore != nil {
		if err := commitImportTransaction(ctx, dstStore, opts, orderedWorknos, mergedByWorkno, resolvedWorknoByKikoeruID, usernames, reviews, azuseRows); err != nil {
			return nil, err
		}
	}

	// 9. Write report file to disk (§14.5: report must be saved to file, not just stdout)
	reportPath := opts.ReportFilePath
	if reportPath == "" {
		reportPath = fmt.Sprintf("kikoeru-import-report-%s.json", time.Now().Format("20060102-150405"))
	}
	if err := writeReportFile(reportPath, report); err != nil {
		return report, fmt.Errorf("write import report %s: %w", reportPath, err)
	}
	report.ReportPath = reportPath
	return report, nil
}

func commitImportTransaction(
	ctx context.Context,
	dstStore *db.Store,
	opts ImportOptions,
	orderedWorknos []string,
	mergedByWorkno map[string]*mergedWork,
	resolvedWorknoByKikoeruID map[int]string,
	usernames []kikoeruUserRow,
	reviews []kikoeruReviewRow,
	azuseRows []azuseHistRow,
) error {
	now := time.Now().Unix()
	tx, err := dstStore.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Users (password_hash = '', must_reset_password = 1 per §14.4)
	userIDByName := map[string]int64{"default": 1}
	for _, u := range usernames {
		adminInt := 0
		if u.IsAdmin {
			adminInt = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO app_user (username, password_hash, is_admin, must_reset_password, created_at)
			VALUES (?, '', ?, 1, ?)
			ON CONFLICT(username) DO NOTHING
		`, u.Name, adminInt, now); err != nil {
			return err
		}
		var uid int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM app_user WHERE username = ?`, u.Name).Scan(&uid); err != nil {
			return err
		}
		userIDByName[u.Name] = uid
	}

	ensureUserTx := func(uname string) (int64, error) {
		uname = strings.TrimSpace(uname)
		if uname == "" {
			return 1, nil
		}
		if id, ok := userIDByName[uname]; ok {
			return id, nil
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO app_user (username, password_hash, is_admin, must_reset_password, created_at)
			VALUES (?, '', 0, 1, ?)
			ON CONFLICT(username) DO NOTHING
		`, uname, now); err != nil {
			return 0, err
		}
		var uid int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM app_user WHERE username = ?`, uname).Scan(&uid); err != nil {
			return 0, err
		}
		userIDByName[uname] = uid
		return uid, nil
	}

	storageIDByAlias := make(map[string]int64)
	ensureStorageTx := func(alias string) (int64, error) {
		if alias == "" {
			alias = "kikoeru_default"
		}
		if sid, ok := storageIDByAlias[alias]; ok {
			return sid, nil
		}
		rootPath := alias
		if opts.RootMap != nil && opts.RootMap[alias] != "" {
			rootPath = opts.RootMap[alias]
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO storage (kind, name, root, config)
			VALUES ('local', ?, ?, '{"migrated_from":"kikoeru"}')
			ON CONFLICT(name) DO UPDATE SET root = CASE WHEN excluded.root != excluded.name THEN excluded.root ELSE storage.root END
		`, alias, rootPath); err != nil {
			return 0, err
		}
		var sid int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM storage WHERE name = ?`, alias).Scan(&sid); err != nil {
			return 0, err
		}
		storageIDByAlias[alias] = sid
		return sid, nil
	}

	for _, wno := range orderedWorknos {
		mw := mergedByWorkno[wno]
		needsInt := 0
		scrapedAt := now
		if mw.NeedsRescrape {
			needsInt = 1
			scrapedAt = 0
		}

		coverDst := ""
		if mw.CoverSrc != "" {
			if opts.DestCoversDir != "" {
				if copied, cpErr := copyCover(mw.CoverSrc, opts.DestCoversDir, wno); cpErr == nil {
					coverDst = copied
				}
			} else {
				coverDst = mw.CoverSrc
			}
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO work (
				workno, title, circle_id, circle_name, release_date, age_category,
				price, rating, rate_count, review_count, dl_count, cover_path,
				needs_rescrape, scraped_at, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(workno) DO UPDATE SET
				title          = excluded.title,
				circle_id      = CASE WHEN excluded.circle_id != '' THEN excluded.circle_id ELSE work.circle_id END,
				circle_name    = CASE WHEN excluded.circle_name != '' THEN excluded.circle_name ELSE work.circle_name END,
				release_date   = CASE WHEN excluded.release_date != '' THEN excluded.release_date ELSE work.release_date END,
				age_category   = excluded.age_category,
				price          = excluded.price,
				rating         = excluded.rating,
				rate_count     = excluded.rate_count,
				review_count   = excluded.review_count,
				dl_count       = excluded.dl_count,
				cover_path     = CASE WHEN excluded.cover_path != '' THEN excluded.cover_path ELSE work.cover_path END,
				needs_rescrape = excluded.needs_rescrape
		`, wno, mw.Title, mw.CircleID, mw.CircleName, mw.ReleaseDate, mw.AgeCategory,
			mw.Price, mw.Rating, mw.RateCount, mw.ReviewCount, mw.DLCount, coverDst,
			needsInt, scrapedAt, now); err != nil {
			return err
		}

		for _, p := range mw.Paths {
			sid, err := ensureStorageTx(p[0])
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_path (workno, storage_id, rel_path) VALUES (?, ?, ?)`, wno, sid, p[1]); err != nil {
				return err
			}
		}

		for tg := range mw.Tags {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO tag (name, source) VALUES (?, 'dlsite')`, tg); err != nil {
				return err
			}
			var tid int64
			if err := tx.QueryRowContext(ctx, `SELECT id FROM tag WHERE name = ?`, tg).Scan(&tid); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_tag (workno, tag_id) VALUES (?, ?)`, wno, tid); err != nil {
				return err
			}
		}

		for va := range mw.VAs {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO person (name) VALUES (?)`, va); err != nil {
				return err
			}
			var pid int64
			if err := tx.QueryRowContext(ctx, `SELECT id FROM person WHERE name = ?`, va).Scan(&pid); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_person (workno, person_id, role) VALUES (?, ?, 'va')`, wno, pid); err != nil {
				return err
			}
		}
	}

	// Reviews -> user_state (§14.2 & §14.3: preserve user ratings/reviews/progress even across collapsed works!)
	for _, rv := range reviews {
		wno := resolvedWorknoByKikoeruID[rv.WorkID]
		if wno == "" {
			wno = dlsite.WorknoFromKikoeruID(rv.WorkID)
		}
		uid, err := ensureUserTx(rv.UserName)
		if err != nil {
			return err
		}
		var ratingArg any
		if rv.Rating.Valid && rv.Rating.Int64 > 0 {
			ratingArg = rv.Rating.Int64
		}
		prog := strings.TrimSpace(rv.Progress.String)
		if !db.ValidProgress[prog] {
			prog = ""
		}
		var progArg any
		if prog != "" {
			progArg = prog
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO user_state (user_id, workno, rating, review, progress, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id, workno) DO UPDATE SET
				rating     = COALESCE(excluded.rating, user_state.rating),
				review     = CASE WHEN COALESCE(excluded.review, '') != '' THEN excluded.review ELSE user_state.review END,
				progress   = COALESCE(excluded.progress, user_state.progress),
				updated_at = excluded.updated_at
		`, uid, wno, ratingArg, rv.ReviewText.String, progArg, now); err != nil {
			return err
		}
	}

	// Optional azuse fork t_history -> listen_history (§14.4)
	for _, hr := range azuseRows {
		wno := resolvedWorknoByKikoeruID[hr.WorkID]
		if wno == "" && hr.WorkID > 0 {
			wno = dlsite.WorknoFromKikoeruID(hr.WorkID)
		}
		if wno == "" {
			continue
		}
		uid, err := ensureUserTx(hr.UserName)
		if err != nil {
			return err
		}
		ts := hr.UpdatedAt
		if ts <= 0 {
			ts = now
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO listen_history (user_id, workno, started_at, last_at, position, listened)
			VALUES (?, ?, ?, ?, ?, ?)
		`, uid, wno, ts, ts, hr.PlayTime, hr.PlayTime); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func hasTable(ctx context.Context, conn *sql.DB, tableName string) bool {
	var cnt int
	err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, tableName).Scan(&cnt)
	return err == nil && cnt > 0
}

func parseSQLiteBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int64:
		return x != 0
	case int:
		return x != 0
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		return s == "1" || s == "true"
	default:
		return false
	}
}

func parseFlexibleTime(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if sec, err := strconv.ParseInt(s, 10, 64); err == nil {
		if sec > 1e12 {
			return sec / 1000
		}
		return sec
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func findKikoeruCover(coversDir, finalWorkno, paddedWorkno string) string {
	candidates := []string{
		finalWorkno + "_img_main.jpg",
		finalWorkno + "_img_sam.jpg",
		paddedWorkno + "_img_main.jpg",
		paddedWorkno + "_img_sam.jpg",
	}
	for _, name := range candidates {
		p := filepath.Join(coversDir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 0 {
			return p
		}
	}
	return ""
}

func copyCover(src, dstDir, workno string) (string, error) {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dstDir, workno+filepath.Ext(src))
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return "", err
	}
	return dst, out.Close()
}

func writeReportFile(path string, report *ImportReport) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}
