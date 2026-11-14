package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SiIverAsh/otoha/internal/config"
	"github.com/SiIverAsh/otoha/internal/db"
)

// QueueSnapshot stores the exact ordered track IDs and active index so resuming a work
// with parallel directories (e.g. `SE有り` vs `SE無し`) never mixes versions (§9.5).
type QueueSnapshot struct {
	TrackIDs     []int64 `json:"track_ids"`
	CurrentIndex int     `json:"current_index"`
	FolderScope  string  `json:"folder_scope,omitempty"`
}

// HeartbeatRequest represents a 30s periodic or event-triggered playback heartbeat (§9.5).
type HeartbeatRequest struct {
	UserID        int64          `json:"user_id"`
	Workno        string         `json:"workno"`
	TrackID       int64          `json:"track_id"`
	Position      float64        `json:"position"`
	ListenedDelta float64        `json:"listened_delta"`
	TrackDuration float64        `json:"track_duration,omitempty"`
	Queue         *QueueSnapshot `json:"queue,omitempty"`
	Incognito     bool           `json:"incognito,omitempty"`
	NowUnix       int64          `json:"-"`
}

// HeartbeatResult returns updated session & auto-advanced progress status.
type HeartbeatResult struct {
	Recorded        bool   `json:"recorded"`
	SessionID       int64  `json:"session_id,omitempty"`
	Progress        string `json:"progress,omitempty"`
	AutoAdvanced    bool   `json:"auto_advanced,omitempty"`
}

// Service manages session-aggregated listen history, privacy controls, and retention (§9.5 & §9.6).
type Service struct {
	store *db.Store
	cfg   *config.Config
}

func NewService(store *db.Store, cfg *config.Config) *Service {
	return &Service{store: store, cfg: cfg}
}

// RecordHeartbeat records a playback heartbeat if history is enabled and not in incognito mode.
func (s *Service) RecordHeartbeat(ctx context.Context, req HeartbeatRequest) (*HeartbeatResult, error) {
	snap := s.cfg.Snapshot()

	// Always allow browser to fill missing track header duration (§4.3), independent of user history.
	if req.TrackID > 0 && req.TrackDuration > 0 {
		_ = s.store.UpdateTrackDuration(ctx, req.TrackID, req.TrackDuration)
	}

	// §9.6: Global privacy toggle & temporary incognito mode write ZERO rows!
	if !snap.HistoryEnabled || req.Incognito {
		return &HeartbeatResult{Recorded: false}, nil
	}

	userID := req.UserID
	if userID <= 0 {
		userID = 1
	}
	workno := strings.ToUpper(strings.TrimSpace(req.Workno))
	if workno == "" {
		return nil, errors.New("workno is required")
	}
	now := req.NowUnix
	if now <= 0 {
		now = time.Now().Unix()
	}
	if req.Position < 0 {
		req.Position = 0
	}
	if req.ListenedDelta < 0 {
		req.ListenedDelta = 0
	}
	// Clamp implausible single-heartbeat listened delta (e.g. clock jump) to 5 minutes
	if req.ListenedDelta > 300 {
		req.ListenedDelta = 300
	}

	var queueJSON string
	if req.Queue != nil && len(req.Queue.TrackIDs) > 0 {
		if raw, err := json.Marshal(req.Queue); err == nil {
			queueJSON = string(raw)
		}
	}

	mergeWindow := int64(snap.SessionMergeSec)
	if mergeWindow <= 0 {
		mergeWindow = 1800 // 30 minutes (§9.5)
	}

	conn := s.store.DB()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Update per-track play_position (§9.5)
	if req.TrackID > 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO play_position (user_id, track_id, position, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(user_id, track_id) DO UPDATE SET
				position = excluded.position,
				updated_at = excluded.updated_at
		`, userID, req.TrackID, req.Position, now); err != nil {
			return nil, err
		}
	}

	// 2. Session aggregation: merge if latest session for (user_id, workno) has last_at within 30 minutes (§9.5)
	var sessionID int64
	var prevLastAt int64
	err = tx.QueryRowContext(ctx, `
		SELECT id, last_at FROM listen_history
		WHERE user_id = ? AND workno = ?
		ORDER BY last_at DESC, id DESC LIMIT 1
	`, userID, workno).Scan(&sessionID, &prevLastAt)

	if err == nil && now-prevLastAt >= 0 && now-prevLastAt < mergeWindow {
		var trackArg any
		if req.TrackID > 0 {
			trackArg = req.TrackID
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE listen_history SET
				track_id  = COALESCE(?, track_id),
				last_at   = ?,
				position  = ?,
				listened  = COALESCE(listened, 0) + ?,
				queue_ctx = CASE WHEN ? != '' THEN ? ELSE queue_ctx END
			WHERE id = ?
		`, trackArg, now, req.Position, req.ListenedDelta, queueJSON, queueJSON, sessionID); err != nil {
			return nil, err
		}
	} else if err == nil || errors.Is(err, sql.ErrNoRows) {
		var trackArg any
		if req.TrackID > 0 {
			trackArg = req.TrackID
		}
		res, insErr := tx.ExecContext(ctx, `
			INSERT INTO listen_history (user_id, workno, track_id, started_at, last_at, position, listened, queue_ctx)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, userID, workno, trackArg, now, now, req.Position, req.ListenedDelta, queueJSON)
		if insErr != nil {
			return nil, insErr
		}
		sessionID, _ = res.LastInsertId()
	} else {
		return nil, err
	}

	// 3. Drive user_state.progress from cumulative `listened` (NOT seek `position`, §9.5)
	progress, autoAdvanced, err := advanceProgressTx(ctx, tx, userID, workno, req.ListenedDelta, req.Queue, now)
	if err != nil {
		return nil, err
	}

	// 4. Retention policy pruning (§9.6: max days & max entries)
	if snap.HistoryMaxDays > 0 {
		cutoff := now - int64(snap.HistoryMaxDays)*86400
		_, _ = tx.ExecContext(ctx, `DELETE FROM listen_history WHERE user_id = ? AND last_at < ?`, userID, cutoff)
	}
	if snap.HistoryMaxEntries > 0 {
		_, _ = tx.ExecContext(ctx, `
			DELETE FROM listen_history
			WHERE user_id = ? AND id NOT IN (
				SELECT id FROM listen_history WHERE user_id = ? ORDER BY last_at DESC, id DESC LIMIT ?
			)
		`, userID, userID, snap.HistoryMaxEntries)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return &HeartbeatResult{
		Recorded:     true,
		SessionID:    sessionID,
		Progress:     progress,
		AutoAdvanced: autoAdvanced,
	}, nil
}

func advanceProgressTx(ctx context.Context, tx *sql.Tx, userID int64, workno string, delta float64, queue *QueueSnapshot, now int64) (string, bool, error) {
	var curProgress sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT progress FROM user_state WHERE user_id = ? AND workno = ?`, userID, workno).Scan(&curProgress)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}

	prog := curProgress.String
	// Start listening automatically if unmarked or marked
	if delta > 0 && (prog == "" || prog == "marked") {
		prog = "listening"
		if exists {
			if _, err := tx.ExecContext(ctx, `UPDATE user_state SET progress = 'listening', updated_at = ? WHERE user_id = ? AND workno = ?`, now, userID, workno); err != nil {
				return "", false, err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `INSERT INTO user_state (user_id, workno, progress, updated_at) VALUES (?, ?, 'listening', ?)`, userID, workno, now); err != nil {
				return "", false, err
			}
		}
	}

	if prog != "listening" {
		return prog, false, nil
	}

	var totalListened float64
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(listened), 0) FROM listen_history WHERE user_id = ? AND workno = ?`, userID, workno).Scan(&totalListened)
	if totalListened <= 0 {
		return prog, false, nil
	}

	// Calculate reference duration from active queue if available (handles SE有り/SE無し parallel versions!)
	var targetDuration float64
	if queue != nil && len(queue.TrackIDs) > 0 {
		ph := make([]string, len(queue.TrackIDs))
		args := make([]any, len(queue.TrackIDs))
		for i, id := range queue.TrackIDs {
			ph[i] = "?"
			args[i] = id
		}
		_ = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COALESCE(SUM(duration), 0) FROM track WHERE id IN (%s)`, strings.Join(ph, ",")), args...).Scan(&targetDuration)
	}
	if targetDuration <= 0 {
		_ = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(duration), 0) FROM track WHERE workno = ?`, workno).Scan(&targetDuration)
	}

	if targetDuration > 0 && totalListened >= targetDuration*0.85 {
		if _, err := tx.ExecContext(ctx, `UPDATE user_state SET progress = 'listened', updated_at = ? WHERE user_id = ? AND workno = ?`, now, userID, workno); err != nil {
			return prog, false, err
		}
		return "listened", true, nil
	}
	return prog, false, nil
}

func ageSQLFilter(ageMode string) string {
	switch ageMode {
	case "general", "":
		return "w.age_category = 1"
	case "sfw_unrated":
		return "COALESCE(w.age_category, 0) IN (0, 1)"
	case "r15":
		return "COALESCE(w.age_category, 0) IN (1, 2)"
	case "r18":
		return "w.age_category = 3"
	case "all":
		return "1=1"
	default:
		return "w.age_category = 1"
	}
}

// ListHistory returns paginated session history ordered by last_at DESC while strictly enforcing
// age_category filtering so hidden R18 works never leak titles (§9.5 & §9.6).
func (s *Service) ListHistory(ctx context.Context, userID int64, ageMode string, page, pageSize int) ([]db.HistoryEntry, int, error) {
	if userID <= 0 {
		userID = 1
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 30
	}
	ageCond := ageSQLFilter(ageMode)

	var total int
	countSQL := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM listen_history lh
		JOIN work w ON w.workno = lh.workno
		WHERE lh.user_id = ? AND %s
	`, ageCond)
	if err := s.store.DB().QueryRowContext(ctx, countSQL, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return []db.HistoryEntry{}, 0, nil
	}

	offset := (page - 1) * pageSize
	q := fmt.Sprintf(`
		SELECT
			lh.id, lh.user_id, lh.workno, w.title, COALESCE(w.circle_name, ''), COALESCE(w.age_category, 0),
			COALESCE(lh.track_id, 0), COALESCE(tr.rel_path, ''), COALESCE(tr.duration, 0),
			lh.started_at, lh.last_at, COALESCE(lh.position, 0), COALESCE(lh.listened, 0), COALESCE(lh.queue_ctx, '')
		FROM listen_history lh
		JOIN work w ON w.workno = lh.workno
		LEFT JOIN track tr ON tr.id = lh.track_id
		WHERE lh.user_id = ? AND %s
		ORDER BY lh.last_at DESC, lh.id DESC
		LIMIT ? OFFSET ?
	`, ageCond)

	rows, err := s.store.DB().QueryContext(ctx, q, userID, pageSize, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []db.HistoryEntry
	for rows.Next() {
		var e db.HistoryEntry
		if err := rows.Scan(
			&e.ID, &e.UserID, &e.Workno, &e.WorkTitle, &e.CircleName, &e.AgeCategory,
			&e.TrackID, &e.TrackName, &e.TrackDur,
			&e.StartedAt, &e.LastAt, &e.Position, &e.Listened, &e.QueueCtx,
		); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// ListRecent returns deduplicated most-recent works for the home/dashboard view (§9.5),
// also enforcing age_category isolation (§9.6).
func (s *Service) ListRecent(ctx context.Context, userID int64, ageMode string, limit int) ([]db.HistoryEntry, error) {
	if userID <= 0 {
		userID = 1
	}
	if limit <= 0 || limit > 100 {
		limit = 12
	}
	ageCond := ageSQLFilter(ageMode)

	q := fmt.Sprintf(`
		SELECT
			lh.id, lh.user_id, lh.workno, w.title, COALESCE(w.circle_name, ''), COALESCE(w.age_category, 0),
			COALESCE(lh.track_id, 0), COALESCE(tr.rel_path, ''), COALESCE(tr.duration, 0),
			lh.started_at, lh.last_at, COALESCE(lh.position, 0), COALESCE(lh.listened, 0), COALESCE(lh.queue_ctx, '')
		FROM listen_history lh
		JOIN (
			SELECT workno, MAX(id) AS latest_id
			FROM (
				SELECT id, workno, last_at FROM listen_history
				WHERE user_id = ?
			) sub
			GROUP BY workno
		) latest ON latest.workno = lh.workno AND lh.id = (
			SELECT id FROM listen_history lh2
			WHERE lh2.user_id = ? AND lh2.workno = latest.workno
			ORDER BY lh2.last_at DESC, lh2.id DESC LIMIT 1
		)
		JOIN work w ON w.workno = lh.workno
		LEFT JOIN track tr ON tr.id = lh.track_id
		WHERE lh.user_id = ? AND %s
		ORDER BY lh.last_at DESC, lh.id DESC
		LIMIT ?
	`, ageCond)

	rows, err := s.store.DB().QueryContext(ctx, q, userID, userID, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []db.HistoryEntry
	for rows.Next() {
		var e db.HistoryEntry
		if err := rows.Scan(
			&e.ID, &e.UserID, &e.Workno, &e.WorkTitle, &e.CircleName, &e.AgeCategory,
			&e.TrackID, &e.TrackName, &e.TrackDur,
			&e.StartedAt, &e.LastAt, &e.Position, &e.Listened, &e.QueueCtx,
		); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteEntry deletes a single history session and runs VACUUM + WAL truncate (§9.6).
func (s *Service) DeleteEntry(ctx context.Context, userID, entryID int64) error {
	if userID <= 0 {
		userID = 1
	}
	var wno string
	var trackID sql.NullInt64
	_ = s.store.DB().QueryRowContext(ctx, `SELECT workno, track_id FROM listen_history WHERE id = ? AND user_id = ?`, entryID, userID).Scan(&wno, &trackID)
	if _, err := s.store.DB().ExecContext(ctx, `DELETE FROM listen_history WHERE id = ? AND user_id = ?`, entryID, userID); err != nil {
		return err
	}
	if trackID.Valid {
		_, _ = s.store.DB().ExecContext(ctx, `DELETE FROM play_position WHERE user_id = ? AND track_id = ?`, userID, trackID.Int64)
	}
	return s.store.VacuumAndTruncateWAL(ctx)
}

// ClearOptions specifies optional time bounds when clearing history (§9 & §9.6).
type ClearOptions struct {
	FromUnix int64 // if >0, delete last_at >= FromUnix
	ToUnix   int64 // if >0, delete last_at <= ToUnix
}

// Clear deletes matching history (and play_position) and MUST execute VACUUM + WAL truncate
// so zero deleted bytes remain in the SQLite file (§9.6 & §12).
func (s *Service) Clear(ctx context.Context, userID int64, opts ClearOptions) (int64, error) {
	if userID <= 0 {
		userID = 1
	}
	where := []string{"user_id = ?"}
	args := []any{userID}
	posWhere := []string{"user_id = ?"}
	posArgs := []any{userID}

	if opts.FromUnix > 0 {
		where = append(where, "last_at >= ?")
		args = append(args, opts.FromUnix)
		posWhere = append(posWhere, "updated_at >= ?")
		posArgs = append(posArgs, opts.FromUnix)
	}
	if opts.ToUnix > 0 {
		where = append(where, "last_at <= ?")
		args = append(args, opts.ToUnix)
		posWhere = append(posWhere, "updated_at <= ?")
		posArgs = append(posArgs, opts.ToUnix)
	}

	res, err := s.store.DB().ExecContext(ctx, "DELETE FROM listen_history WHERE "+strings.Join(where, " AND "), args...)
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	_, _ = s.store.DB().ExecContext(ctx, "DELETE FROM play_position WHERE "+strings.Join(posWhere, " AND "), posArgs...)

	if err := s.store.VacuumAndTruncateWAL(ctx); err != nil {
		return affected, err
	}
	return affected, nil
}
