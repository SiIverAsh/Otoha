package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/SiIverAsh/otoha/internal/dlsite"
)

// SaveASMR18Work commits a work, its source, tracks and chapters atomically.
// Existing metadata and other providers' tracks are preserved. The canonical RJ
// primary key and source-scoped track key make retries idempotent.
func (s *Store) SaveASMR18Work(ctx context.Context, meta *dlsite.Metadata, pageURL string, tracks []RemoteTrackInput) (bool, error) {
	if meta == nil || len(tracks) == 0 {
		return false, fmt.Errorf("missing ASMR18 work or tracks")
	}
	wno := dlsite.NormalizeWorkno(meta.Workno)
	if wno == "" {
		return false, fmt.Errorf("invalid RJ")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO storage(kind,name,root,config) VALUES('asmr18','asmr18.fans','https://asmr18.fans','{"remote":true}') ON CONFLICT(name) DO NOTHING`); err != nil {
		return false, err
	}
	var storageID int64
	var kind string
	if err = tx.QueryRowContext(ctx, `SELECT id,kind FROM storage WHERE name='asmr18.fans'`).Scan(&storageID, &kind); err != nil {
		return false, err
	}
	if kind != "asmr18" {
		return false, fmt.Errorf("storage name asmr18.fans is already used by another source")
	}
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM work WHERE workno=?`, wno).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = saveMetadataTx(ctx, tx, meta, "", true); err != nil {
			return false, err
		}
	} else if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO asmr18_work(workno,page_url,fetched_at) VALUES(?,?,?) ON CONFLICT(workno) DO UPDATE SET page_url=excluded.page_url,fetched_at=excluded.fetched_at`, wno, pageURL, time.Now().Unix()); err != nil {
		return false, err
	}
	if err = saveRemoteTracksTx(ctx, tx, wno, storageID, tracks); err != nil {
		return false, err
	}
	if len(tracks) == 1 && tracks[0].StreamURL != "" && len(tracks[0].Chapters) > 1 {
		if err := saveASMR18NativeSegments(ctx, tx, wno, storageID, tracks[0]); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return exists == 0, nil
}

func saveASMR18NativeSegments(ctx context.Context, tx *sql.Tx, workno string, storageID int64, track RemoteTrackInput) error {
	source := asmr18SegmentSource{relPath: track.RelPath}
	var alreadySplit bool
	if err := tx.QueryRowContext(ctx, `
		SELECT t.id, COALESCE(t.duration, 0),
			EXISTS (SELECT 1 FROM track segment WHERE segment.source_track_id = t.id)
		FROM track t WHERE t.workno = ? AND t.storage_id = ? AND t.work_rel = '' AND t.rel_path = ?
	`, workno, storageID, track.RelPath).Scan(&source.id, &source.duration, &alreadySplit); err != nil {
		return err
	}
	if alreadySplit {
		return nil
	}
	segments := ASMR18SegmentsFromChapters(track.Chapters, source.duration)
	return insertASMR18TrackSegments(ctx, tx, source, segments)
}

// ListASMR18Worknos returns completed ASMR18 sources, not just works present from
// other providers. Duration probing may revisit sources whose duration is unknown.
func (s *Store) ListASMR18Worknos(ctx context.Context, requireDuration bool) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT aw.workno FROM asmr18_work aw
 WHERE EXISTS(SELECT 1 FROM track t JOIN storage st ON st.id=t.storage_id WHERE t.workno=aw.workno AND st.kind='asmr18' AND COALESCE(t.stream_url,'')!='')
 AND (?=0 OR NOT EXISTS(SELECT 1 FROM track t JOIN storage st ON st.id=t.storage_id WHERE t.workno=aw.workno AND st.kind='asmr18' AND t.source_track_id IS NULL AND COALESCE(t.duration,0)<=0))`, requireDuration)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
