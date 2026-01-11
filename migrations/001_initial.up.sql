CREATE TABLE IF NOT EXISTS locations (
    timestamp INTEGER NOT NULL,
    user_id   TEXT NOT NULL,
    device_id TEXT NOT NULL,
    lat       REAL NOT NULL,
    lon       REAL NOT NULL,
    PRIMARY KEY (timestamp, device_id)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_locations_lat_lon ON locations(lat, lon);
CREATE INDEX IF NOT EXISTS idx_locations_timestamp ON locations(timestamp);
