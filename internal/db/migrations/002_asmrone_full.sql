ALTER TABLE work ADD COLUMN asmrone_id INTEGER DEFAULT 0;
ALTER TABLE work ADD COLUMN source_type TEXT DEFAULT '';
ALTER TABLE work ADD COLUMN source_url TEXT DEFAULT '';
ALTER TABLE work ADD COLUMN duration INTEGER DEFAULT 0;
ALTER TABLE work ADD COLUMN has_subtitle INTEGER DEFAULT 0;
ALTER TABLE work ADD COLUMN create_date TEXT DEFAULT '';
ALTER TABLE work ADD COLUMN original_workno TEXT DEFAULT '';
ALTER TABLE work ADD COLUMN language_editions_json TEXT DEFAULT '';
ALTER TABLE work ADD COLUMN other_editions_json TEXT DEFAULT '';
ALTER TABLE work ADD COLUMN translation_info_json TEXT DEFAULT '';
ALTER TABLE work ADD COLUMN rate_detail_json TEXT DEFAULT '';
ALTER TABLE work ADD COLUMN rank_json TEXT DEFAULT '';
ALTER TABLE work ADD COLUMN work_attributes TEXT DEFAULT '';
ALTER TABLE work ADD COLUMN sam_cover_url TEXT DEFAULT '';
ALTER TABLE work ADD COLUMN thumbnail_cover_url TEXT DEFAULT '';
ALTER TABLE work ADD COLUMN remote_files_json TEXT DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_work_asmrone_id ON work(asmrone_id);
CREATE INDEX IF NOT EXISTS idx_work_has_subtitle ON work(has_subtitle);

ALTER TABLE track ADD COLUMN stream_url TEXT DEFAULT '';
ALTER TABLE track ADD COLUMN download_url TEXT DEFAULT '';
ALTER TABLE track ADD COLUMN stream_low_url TEXT DEFAULT '';
ALTER TABLE track ADD COLUMN subtitle_url TEXT DEFAULT '';
ALTER TABLE track ADD COLUMN remote_hash TEXT DEFAULT '';
