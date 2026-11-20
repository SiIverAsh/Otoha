CREATE TABLE asmr18_work (
  workno TEXT PRIMARY KEY REFERENCES work(workno) ON DELETE CASCADE,
  page_url TEXT NOT NULL,
  fetched_at INTEGER NOT NULL
);

CREATE TABLE track_chapter (
  track_id INTEGER NOT NULL REFERENCES track(id) ON DELETE CASCADE,
  ord INTEGER NOT NULL,
  title TEXT NOT NULL,
  start REAL NOT NULL CHECK(start >= 0),
  PRIMARY KEY(track_id, ord)
);
