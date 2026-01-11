-- Import job tracking with checkpoints for resumable imports
CREATE TABLE IF NOT EXISTS import_jobs (
    id           TEXT PRIMARY KEY,
    status       TEXT NOT NULL,      -- pending, running, completed, failed, interrupted
    started_at   INTEGER NOT NULL,
    completed_at INTEGER,
    
    -- Progress tracking
    total_assets INTEGER,
    processed    INTEGER DEFAULT 0,
    imported     INTEGER DEFAULT 0,
    skipped      INTEGER DEFAULT 0,
    errors       INTEGER DEFAULT 0,
    
    -- Checkpoint for resume
    last_page    INTEGER DEFAULT 0,
    
    -- Filters used (JSON) - allows resume with same parameters
    config_json  TEXT NOT NULL,
    
    -- Error information
    last_error   TEXT
);

CREATE INDEX IF NOT EXISTS idx_import_jobs_status ON import_jobs(status);

-- Continuous sync state tracking
CREATE TABLE IF NOT EXISTS sync_state (
    id        TEXT PRIMARY KEY DEFAULT 'immich',
    last_sync INTEGER NOT NULL,      -- Unix timestamp of last successful sync
    metadata  TEXT                   -- JSON for additional state
);

-- Source metadata linking locations back to Immich assets
-- Enables photo thumbnails and links on the map
CREATE TABLE IF NOT EXISTS location_sources (
    timestamp   INTEGER NOT NULL,
    device_id   TEXT NOT NULL,
    source_type TEXT NOT NULL,       -- 'immich'
    source_id   TEXT NOT NULL,       -- Immich asset UUID
    metadata    TEXT,                -- JSON: filename, dimensions, etc.
    PRIMARY KEY (timestamp, device_id)
);

CREATE INDEX IF NOT EXISTS idx_location_sources_source ON location_sources(source_type, source_id);
