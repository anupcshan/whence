-- Pre-computed paths for efficient spatial queries
-- Paths group locations by user and calendar day (in local timezone)
CREATE TABLE IF NOT EXISTS paths (
    id        INTEGER PRIMARY KEY,
    user_id   TEXT NOT NULL,
    date      TEXT NOT NULL,           -- Local date YYYY-MM-DD
    start_ts  INTEGER NOT NULL,        -- First point timestamp
    end_ts    INTEGER NOT NULL,        -- Last point timestamp

    -- Bounding box for spatial queries
    min_lat   REAL NOT NULL,
    max_lat   REAL NOT NULL,
    min_lon   REAL NOT NULL,
    max_lon   REAL NOT NULL,

    -- Point count for display hints
    point_count INTEGER NOT NULL,

    UNIQUE(user_id, date)
);

-- Spatial index for bounding box queries
CREATE INDEX IF NOT EXISTS idx_paths_bbox ON paths(min_lat, max_lat, min_lon, max_lon);
CREATE INDEX IF NOT EXISTS idx_paths_time ON paths(start_ts, end_ts);
CREATE INDEX IF NOT EXISTS idx_paths_user_date ON paths(user_id, date);

-- Path points store the ordered sequence of locations in a path
-- We denormalize lat/lon here to avoid joins during rendering
CREATE TABLE IF NOT EXISTS path_points (
    path_id   INTEGER NOT NULL,
    seq       INTEGER NOT NULL,        -- Order within path (0-indexed)
    timestamp INTEGER NOT NULL,
    lat       REAL NOT NULL,
    lon       REAL NOT NULL,
    PRIMARY KEY (path_id, seq),
    FOREIGN KEY (path_id) REFERENCES paths(id) ON DELETE CASCADE
) WITHOUT ROWID;
