-- Geocache table for storing reverse geocoded place names
-- Uses bounding box from Nominatim to enable spatial cache lookups
CREATE TABLE IF NOT EXISTS geocache (
    id INTEGER PRIMARY KEY,
    min_lat REAL NOT NULL,
    max_lat REAL NOT NULL,
    min_lon REAL NOT NULL,
    max_lon REAL NOT NULL,
    place_name TEXT NOT NULL,
    place_type TEXT,
    display_name TEXT,
    created_at INTEGER NOT NULL,
    UNIQUE(min_lat, max_lat, min_lon, max_lon)
);

-- Index for bounding box containment queries
-- Query pattern: WHERE ? BETWEEN min_lat AND max_lat AND ? BETWEEN min_lon AND max_lon
CREATE INDEX IF NOT EXISTS idx_geocache_bbox ON geocache(min_lat, max_lat, min_lon, max_lon);
