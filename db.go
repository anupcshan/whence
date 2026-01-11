package main

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Location struct {
	Timestamp int64   `json:"timestamp"`
	UserID    string  `json:"user_id"`
	DeviceID  string  `json:"device_id"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
}

type DB struct {
	*sql.DB
}

func OpenDB(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		return nil, err
	}

	if _, err := db.Exec(`
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
	`); err != nil {
		return nil, err
	}

	return &DB{db}, nil
}

func (db *DB) InsertLocation(loc Location) error {
	_, err := db.Exec(
		`INSERT OR IGNORE INTO locations (timestamp, user_id, device_id, lat, lon) VALUES (?, ?, ?, ?, ?)`,
		loc.Timestamp, loc.UserID, loc.DeviceID, loc.Lat, loc.Lon,
	)
	return err
}

type BBox struct {
	SwLng, SwLat, NeLng, NeLat float64
}

func (db *DB) QueryLocations(bbox BBox, start, end *int64) ([]Location, error) {
	query := `SELECT timestamp, user_id, device_id, lat, lon FROM locations WHERE lat >= ? AND lat <= ? AND lon >= ? AND lon <= ?`
	args := []any{bbox.SwLat, bbox.NeLat, bbox.SwLng, bbox.NeLng}

	if start != nil {
		query += " AND timestamp >= ?"
		args = append(args, *start)
	}
	if end != nil {
		query += " AND timestamp <= ?"
		args = append(args, *end)
	}

	query += " ORDER BY timestamp"

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var locations []Location
	for rows.Next() {
		var loc Location
		if err := rows.Scan(&loc.Timestamp, &loc.UserID, &loc.DeviceID, &loc.Lat, &loc.Lon); err != nil {
			return nil, err
		}
		locations = append(locations, loc)
	}
	return locations, rows.Err()
}
