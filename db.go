package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
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
	// Extended fields (nullable)
	AltitudeM *float64 `json:"altitude_m,omitempty"` // meters
	AccuracyM *float64 `json:"accuracy_m,omitempty"` // meters
	SpeedKmh  *float64 `json:"speed_kmh,omitempty"`  // km/h
	Source    *string  `json:"source,omitempty"`     // GPS, WIFI, CELL, etc.
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

	if err := runMigrations(db); err != nil {
		return nil, fmt.Errorf("migrations failed: %w", err)
	}

	return &DB{db}, nil
}

func (db *DB) InsertLocation(loc Location) error {
	_, err := db.Exec(
		`INSERT OR IGNORE INTO locations (timestamp, user_id, device_id, lat, lon, altitude_m, accuracy_m, speed_kmh, source) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		loc.Timestamp, loc.UserID, loc.DeviceID, loc.Lat, loc.Lon, loc.AltitudeM, loc.AccuracyM, loc.SpeedKmh, loc.Source,
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

func (db *DB) LatestLocation() (*Location, error) {
	row := db.QueryRow(`SELECT timestamp, user_id, device_id, lat, lon FROM locations ORDER BY timestamp DESC LIMIT 1`)
	var loc Location
	err := row.Scan(&loc.Timestamp, &loc.UserID, &loc.DeviceID, &loc.Lat, &loc.Lon)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &loc, nil
}

// LocationSource links a location to its source (e.g., Immich asset)
type LocationSource struct {
	Timestamp  int64  `json:"timestamp"`
	DeviceID   string `json:"device_id"`
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
	Metadata   string `json:"metadata,omitempty"`
}

// InsertLocationBatch inserts multiple locations in a single transaction
// Returns count of inserted and skipped (duplicate) locations
func (db *DB) InsertLocationBatch(locs []Location) (inserted, skipped int, err error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO locations (timestamp, user_id, device_id, lat, lon, altitude_m, accuracy_m, speed_kmh, source) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, 0, err
	}
	defer stmt.Close()

	for _, loc := range locs {
		result, err := stmt.Exec(loc.Timestamp, loc.UserID, loc.DeviceID, loc.Lat, loc.Lon, loc.AltitudeM, loc.AccuracyM, loc.SpeedKmh, loc.Source)
		if err != nil {
			return inserted, skipped, err
		}
		affected, _ := result.RowsAffected()
		if affected > 0 {
			inserted++
		} else {
			skipped++
		}
	}

	err = tx.Commit()
	return inserted, skipped, err
}

// InsertLocationWithSource inserts a location and its source metadata
func (db *DB) InsertLocationWithSource(loc Location, source LocationSource) (inserted bool, err error) {
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// Insert location
	result, err := tx.Exec(
		`INSERT OR IGNORE INTO locations (timestamp, user_id, device_id, lat, lon, altitude_m, accuracy_m, speed_kmh, source) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		loc.Timestamp, loc.UserID, loc.DeviceID, loc.Lat, loc.Lon, loc.AltitudeM, loc.AccuracyM, loc.SpeedKmh, loc.Source,
	)
	if err != nil {
		return false, err
	}

	affected, _ := result.RowsAffected()
	if affected > 0 {
		// Also insert source metadata
		_, err = tx.Exec(
			`INSERT OR REPLACE INTO location_sources (timestamp, device_id, source_type, source_id, metadata) VALUES (?, ?, ?, ?, ?)`,
			source.Timestamp, source.DeviceID, source.SourceType, source.SourceID, source.Metadata,
		)
		if err != nil {
			return false, err
		}
		inserted = true
	}

	err = tx.Commit()
	return inserted, err
}

// GetLocationSource retrieves source metadata for a location
func (db *DB) GetLocationSource(timestamp int64, deviceID string) (*LocationSource, error) {
	row := db.QueryRow(
		`SELECT timestamp, device_id, source_type, source_id, metadata FROM location_sources WHERE timestamp = ? AND device_id = ?`,
		timestamp, deviceID,
	)
	var src LocationSource
	var metadata sql.NullString
	err := row.Scan(&src.Timestamp, &src.DeviceID, &src.SourceType, &src.SourceID, &metadata)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	src.Metadata = metadata.String
	return &src, nil
}

// Bounds represents a geographic bounding box
type Bounds struct {
	MinLat float64 `json:"min_lat"`
	MaxLat float64 `json:"max_lat"`
	MinLon float64 `json:"min_lon"`
	MaxLon float64 `json:"max_lon"`
}

// GetBoundsForTimestampRange returns the bounding box for all locations in a time range
func (db *DB) GetBoundsForTimestampRange(start, end int64) (*Bounds, error) {
	row := db.QueryRow(
		`SELECT MIN(lat), MAX(lat), MIN(lon), MAX(lon) FROM locations WHERE timestamp >= ? AND timestamp <= ?`,
		start, end,
	)
	var minLat, maxLat, minLon, maxLon sql.NullFloat64
	err := row.Scan(&minLat, &maxLat, &minLon, &maxLon)
	if err != nil {
		return nil, err
	}
	if !minLat.Valid {
		return nil, nil
	}
	return &Bounds{
		MinLat: minLat.Float64,
		MaxLat: maxLat.Float64,
		MinLon: minLon.Float64,
		MaxLon: maxLon.Float64,
	}, nil
}

// GetLocationSourceByTimestamp retrieves source metadata by timestamp only
// Used when device_id is not available (e.g., from path points)
func (db *DB) GetLocationSourceByTimestamp(timestamp int64) (*LocationSource, error) {
	row := db.QueryRow(
		`SELECT timestamp, device_id, source_type, source_id, metadata FROM location_sources WHERE timestamp = ? LIMIT 1`,
		timestamp,
	)
	var src LocationSource
	var metadata sql.NullString
	err := row.Scan(&src.Timestamp, &src.DeviceID, &src.SourceType, &src.SourceID, &metadata)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	src.Metadata = metadata.String
	return &src, nil
}

// ImportJob represents a background import job
type ImportJob struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	StartedAt   int64   `json:"started_at"`
	CompletedAt *int64  `json:"completed_at,omitempty"`
	Total       *int    `json:"total,omitempty"`
	Processed   int     `json:"processed"`
	Imported    int     `json:"imported"`
	Skipped     int     `json:"skipped"`
	Errors      int     `json:"errors"`
	LastPage    int     `json:"last_page"`
	ConfigJSON  string  `json:"config_json"`
	LastError   *string `json:"last_error,omitempty"`
}

// CreateImportJob creates a new import job record
func (db *DB) CreateImportJob(job ImportJob) error {
	_, err := db.Exec(
		`INSERT INTO import_jobs (id, status, started_at, total_assets, processed, imported, skipped, errors, last_page, config_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Status, job.StartedAt, job.Total, job.Processed, job.Imported, job.Skipped, job.Errors, job.LastPage, job.ConfigJSON,
	)
	return err
}

// GetImportJob retrieves an import job by ID
func (db *DB) GetImportJob(id string) (*ImportJob, error) {
	row := db.QueryRow(
		`SELECT id, status, started_at, completed_at, total_assets, processed, imported, skipped, errors, last_page, config_json, last_error
		 FROM import_jobs WHERE id = ?`, id,
	)
	var job ImportJob
	var completedAt, total sql.NullInt64
	var lastError sql.NullString
	err := row.Scan(&job.ID, &job.Status, &job.StartedAt, &completedAt, &total, &job.Processed, &job.Imported, &job.Skipped, &job.Errors, &job.LastPage, &job.ConfigJSON, &lastError)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if completedAt.Valid {
		job.CompletedAt = &completedAt.Int64
	}
	if total.Valid {
		t := int(total.Int64)
		job.Total = &t
	}
	if lastError.Valid {
		job.LastError = &lastError.String
	}
	return &job, nil
}

// UpdateImportJob updates an import job's progress
func (db *DB) UpdateImportJob(job ImportJob) error {
	_, err := db.Exec(
		`UPDATE import_jobs SET status = ?, completed_at = ?, total_assets = ?, processed = ?, imported = ?, skipped = ?, errors = ?, last_page = ?, last_error = ? WHERE id = ?`,
		job.Status, job.CompletedAt, job.Total, job.Processed, job.Imported, job.Skipped, job.Errors, job.LastPage, job.LastError, job.ID,
	)
	return err
}

// ListImportJobs returns all import jobs, most recent first
func (db *DB) ListImportJobs() ([]ImportJob, error) {
	rows, err := db.Query(
		`SELECT id, status, started_at, completed_at, total_assets, processed, imported, skipped, errors, last_page, config_json, last_error
		 FROM import_jobs ORDER BY started_at DESC LIMIT 50`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []ImportJob
	for rows.Next() {
		var job ImportJob
		var completedAt, total sql.NullInt64
		var lastError sql.NullString
		err := rows.Scan(&job.ID, &job.Status, &job.StartedAt, &completedAt, &total, &job.Processed, &job.Imported, &job.Skipped, &job.Errors, &job.LastPage, &job.ConfigJSON, &lastError)
		if err != nil {
			return nil, err
		}
		if completedAt.Valid {
			job.CompletedAt = &completedAt.Int64
		}
		if total.Valid {
			t := int(total.Int64)
			job.Total = &t
		}
		if lastError.Valid {
			job.LastError = &lastError.String
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// GetSyncState retrieves the last sync timestamp
func (db *DB) GetSyncState() (*int64, error) {
	row := db.QueryRow(`SELECT last_sync FROM sync_state WHERE id = 'immich'`)
	var lastSync int64
	err := row.Scan(&lastSync)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &lastSync, nil
}

// SetSyncState updates the last sync timestamp
func (db *DB) SetSyncState(lastSync int64) error {
	_, err := db.Exec(
		`INSERT OR REPLACE INTO sync_state (id, last_sync) VALUES ('immich', ?)`,
		lastSync,
	)
	return err
}

// PhotoLocation represents a photo with GPS coordinates from Immich
type PhotoLocation struct {
	Timestamp int64   `json:"timestamp"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	SourceID  string  `json:"source_id"`
	WebURL    string  `json:"web_url"`
	Filename  string  `json:"filename"`
}

// QueryPhotoLocations returns all photos with GPS coordinates in a time range
func (db *DB) QueryPhotoLocations(start, end int64) ([]PhotoLocation, error) {
	rows, err := db.Query(`
		SELECT l.timestamp, l.lat, l.lon, ls.source_id, ls.metadata
		FROM locations l
		JOIN location_sources ls ON l.timestamp = ls.timestamp AND l.device_id = ls.device_id
		WHERE l.timestamp >= ? AND l.timestamp <= ?
		ORDER BY l.timestamp`,
		start, end,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var photos []PhotoLocation
	for rows.Next() {
		var p PhotoLocation
		var metadata sql.NullString
		if err := rows.Scan(&p.Timestamp, &p.Lat, &p.Lon, &p.SourceID, &metadata); err != nil {
			return nil, err
		}
		// Parse metadata JSON for web_url and filename
		if metadata.Valid && metadata.String != "" {
			var meta map[string]string
			if json.Unmarshal([]byte(metadata.String), &meta) == nil {
				p.WebURL = meta["web_url"]
				p.Filename = meta["filename"]
			}
		}
		photos = append(photos, p)
	}
	return photos, rows.Err()
}
