CREATE TABLE IF NOT EXISTS work (
  workno         TEXT PRIMARY KEY,
  title          TEXT NOT NULL,
  circle_id      TEXT,
  circle_name    TEXT,
  release_date   TEXT,
  age_category   INTEGER DEFAULT 0,
  work_type      TEXT,
  series_id      TEXT,
  series_name    TEXT,
  price          INTEGER DEFAULT 0,
  file_size      INTEGER DEFAULT 0,
  rating         REAL DEFAULT 0,
  rate_count     INTEGER DEFAULT 0,
  review_count   INTEGER DEFAULT 0,
  dl_count       INTEGER DEFAULT 0,
  cover_path     TEXT,
  cover_url      TEXT,
  needs_rescrape INTEGER DEFAULT 0,
  scraped_at     INTEGER,
  created_at     INTEGER
);
CREATE INDEX IF NOT EXISTS idx_work_age ON work(age_category);
CREATE INDEX IF NOT EXISTS idx_work_release ON work(release_date DESC);
CREATE INDEX IF NOT EXISTS idx_work_circle ON work(circle_name);

CREATE TABLE IF NOT EXISTS work_path (
  workno     TEXT NOT NULL,
  storage_id INTEGER NOT NULL,
  rel_path   TEXT NOT NULL,
  PRIMARY KEY (workno, storage_id, rel_path)
);

CREATE TABLE IF NOT EXISTS track (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  workno     TEXT NOT NULL,
  storage_id INTEGER NOT NULL DEFAULT 0,
  work_rel   TEXT NOT NULL DEFAULT '',
  rel_path   TEXT NOT NULL,
  size       INTEGER,
  duration   REAL,
  ord        INTEGER,
  UNIQUE(workno, storage_id, work_rel, rel_path)
);
CREATE INDEX IF NOT EXISTS idx_track_work ON track(workno, ord);

CREATE TABLE IF NOT EXISTS person (
  id   INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS work_person (
  workno    TEXT NOT NULL,
  person_id INTEGER NOT NULL,
  role      TEXT NOT NULL,
  PRIMARY KEY (workno, person_id, role)
);
CREATE INDEX IF NOT EXISTS idx_work_person_pid ON work_person(person_id, role);

CREATE TABLE IF NOT EXISTS tag (
  id     INTEGER PRIMARY KEY AUTOINCREMENT,
  name   TEXT NOT NULL UNIQUE,
  source TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS work_tag (
  workno TEXT NOT NULL,
  tag_id INTEGER NOT NULL,
  PRIMARY KEY (workno, tag_id)
);
CREATE INDEX IF NOT EXISTS idx_work_tag_tid ON work_tag(tag_id);

CREATE TABLE IF NOT EXISTS app_user (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  username            TEXT NOT NULL UNIQUE,
  password_hash       TEXT NOT NULL DEFAULT '',
  is_admin            INTEGER NOT NULL DEFAULT 0,
  must_reset_password INTEGER NOT NULL DEFAULT 0,
  created_at          INTEGER NOT NULL
);

INSERT OR IGNORE INTO app_user (id, username, password_hash, is_admin, must_reset_password, created_at)
VALUES (1, 'default', '', 1, 0, strftime('%s', 'now'));

CREATE TABLE IF NOT EXISTS user_session (
  token      TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_session_user ON user_session(user_id);

CREATE TABLE IF NOT EXISTS user_state (
  user_id    INTEGER NOT NULL,
  workno     TEXT NOT NULL,
  rating     INTEGER,
  review     TEXT,
  progress   TEXT,
  updated_at INTEGER,
  PRIMARY KEY (user_id, workno)
);

CREATE TABLE IF NOT EXISTS play_position (
  user_id    INTEGER NOT NULL,
  track_id   INTEGER NOT NULL,
  position   REAL NOT NULL,
  updated_at INTEGER,
  PRIMARY KEY (user_id, track_id)
);

CREATE TABLE IF NOT EXISTS listen_history (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL,
  workno     TEXT NOT NULL,
  track_id   INTEGER,
  started_at INTEGER NOT NULL,
  last_at    INTEGER NOT NULL,
  position   REAL,
  listened   REAL,
  queue_ctx  TEXT
);
CREATE INDEX IF NOT EXISTS idx_history_recent ON listen_history(user_id, last_at DESC);
CREATE INDEX IF NOT EXISTS idx_history_work   ON listen_history(user_id, workno, last_at DESC);

CREATE TABLE IF NOT EXISTS storage (
  id     INTEGER PRIMARY KEY AUTOINCREMENT,
  kind   TEXT NOT NULL,
  name   TEXT NOT NULL UNIQUE,
  root   TEXT NOT NULL,
  config TEXT
);
