ALTER TABLE track ADD COLUMN source_track_id INTEGER REFERENCES track(id) ON DELETE CASCADE;
ALTER TABLE track ADD COLUMN start_seconds REAL CHECK (start_seconds >= 0);
ALTER TABLE track ADD COLUMN end_seconds REAL CHECK (end_seconds > start_seconds);

CREATE UNIQUE INDEX idx_track_source_segment ON track(source_track_id, ord) WHERE source_track_id IS NOT NULL;
