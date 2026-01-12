package main

import (
	"database/sql"
	"math"
	"time"
)

// SimplifyPath reduces the number of points using the Douglas-Peucker algorithm.
// tolerance is in degrees - points deviating less than this from the line are removed.
func SimplifyPath(points []PathPoint, tolerance float64) []PathPoint {
	if len(points) <= 2 {
		return points
	}

	// Find the point with the maximum distance from the line between first and last
	maxDist := 0.0
	maxIdx := 0

	first := points[0]
	last := points[len(points)-1]

	for i := 1; i < len(points)-1; i++ {
		dist := perpendicularDistanceDeg(points[i], first, last)
		if dist > maxDist {
			maxDist = dist
			maxIdx = i
		}
	}

	// If max distance is greater than tolerance, recursively simplify
	if maxDist > tolerance {
		left := SimplifyPath(points[:maxIdx+1], tolerance)
		right := SimplifyPath(points[maxIdx:], tolerance)

		// Combine results, avoiding duplicate point at maxIdx
		result := make([]PathPoint, 0, len(left)+len(right)-1)
		result = append(result, left[:len(left)-1]...)
		result = append(result, right...)
		return result
	}

	// All points are within tolerance, return just endpoints
	return []PathPoint{first, last}
}

// perpendicularDistanceDeg calculates the perpendicular distance in degrees.
func perpendicularDistanceDeg(point, lineStart, lineEnd PathPoint) float64 {
	dx := lineEnd.Lon - lineStart.Lon
	dy := lineEnd.Lat - lineStart.Lat

	if dx == 0 && dy == 0 {
		// Line is a point
		dLon := point.Lon - lineStart.Lon
		dLat := point.Lat - lineStart.Lat
		return math.Sqrt(dLon*dLon + dLat*dLat)
	}

	// Calculate perpendicular distance using cross product
	num := math.Abs(dy*point.Lon - dx*point.Lat + lineEnd.Lon*lineStart.Lat - lineEnd.Lat*lineStart.Lon)
	den := math.Sqrt(dy*dy + dx*dx)

	return num / den
}

// ToleranceFromBBox calculates an appropriate simplification tolerance based on viewport size.
// Returns tolerance in degrees - smaller viewport = smaller tolerance = more detail.
func ToleranceFromBBox(bbox BBox) float64 {
	// Calculate the viewport size in degrees
	latSpan := bbox.NeLat - bbox.SwLat
	lonSpan := bbox.NeLng - bbox.SwLng

	// Use the smaller dimension to determine tolerance
	// A point deviation of ~0.1% of viewport size is imperceptible
	minSpan := latSpan
	if lonSpan < minSpan {
		minSpan = lonSpan
	}

	// Tolerance: 0.1% of viewport, with min/max bounds
	tolerance := minSpan * 0.001

	// Minimum tolerance ~1 meter (roughly 0.00001 degrees)
	if tolerance < 0.00001 {
		tolerance = 0.00001
	}
	// Maximum tolerance ~100 meters
	if tolerance > 0.001 {
		tolerance = 0.001
	}

	return tolerance
}

// Path represents a pre-computed path for a user on a specific day
type Path struct {
	ID         int64       `json:"id"`
	UserID     string      `json:"user_id"`
	Date       string      `json:"date"` // YYYY-MM-DD in local timezone
	StartTS    int64       `json:"start_ts"`
	EndTS      int64       `json:"end_ts"`
	MinLat     float64     `json:"min_lat"`
	MaxLat     float64     `json:"max_lat"`
	MinLon     float64     `json:"min_lon"`
	MaxLon     float64     `json:"max_lon"`
	PointCount int         `json:"point_count"`
	Points     []PathPoint `json:"points,omitempty"`
}

// TimezoneFromCoords returns a time.Location based on longitude.
// Uses a simple 15-degree-per-hour approximation.
// For more accuracy, this could be replaced with a proper timezone database.
func TimezoneFromCoords(lat, lon float64) *time.Location {
	// Each 15 degrees of longitude = 1 hour offset from UTC
	// This is a rough approximation that works reasonably well for most locations
	offsetHours := int(math.Round(lon / 15.0))

	// Clamp to valid range
	if offsetHours < -12 {
		offsetHours = -12
	} else if offsetHours > 14 {
		offsetHours = 14
	}

	return time.FixedZone("", offsetHours*3600)
}

// LocalDateFromTimestamp returns the local date (YYYY-MM-DD) for a timestamp at given coordinates
func LocalDateFromTimestamp(ts int64, lat, lon float64) string {
	loc := TimezoneFromCoords(lat, lon)
	t := time.Unix(ts, 0).In(loc)
	return t.Format("2006-01-02")
}

// ComputePathsForLocations groups locations by user+day and creates paths
// Returns a map of userID+date -> Path
func ComputePathsForLocations(locations []Location) map[string]*Path {
	paths := make(map[string]*Path)

	for _, loc := range locations {
		date := LocalDateFromTimestamp(loc.Timestamp, loc.Lat, loc.Lon)
		key := loc.UserID + "|" + date

		path, exists := paths[key]
		if !exists {
			path = &Path{
				UserID:     loc.UserID,
				Date:       date,
				StartTS:    loc.Timestamp,
				EndTS:      loc.Timestamp,
				MinLat:     loc.Lat,
				MaxLat:     loc.Lat,
				MinLon:     loc.Lon,
				MaxLon:     loc.Lon,
				PointCount: 0,
				Points:     []PathPoint{},
			}
			paths[key] = path
		}

		// Update temporal bounds
		if loc.Timestamp < path.StartTS {
			path.StartTS = loc.Timestamp
		}
		if loc.Timestamp > path.EndTS {
			path.EndTS = loc.Timestamp
		}

		// Update spatial bounds
		if loc.Lat < path.MinLat {
			path.MinLat = loc.Lat
		}
		if loc.Lat > path.MaxLat {
			path.MaxLat = loc.Lat
		}
		if loc.Lon < path.MinLon {
			path.MinLon = loc.Lon
		}
		if loc.Lon > path.MaxLon {
			path.MaxLon = loc.Lon
		}

		path.Points = append(path.Points, PathPoint{
			Lat:       loc.Lat,
			Lon:       loc.Lon,
			Timestamp: loc.Timestamp,
		})
		path.PointCount++
	}

	// Sort points within each path by timestamp
	for _, path := range paths {
		sortPathPoints(path.Points)
	}

	return paths
}

// sortPathPoints sorts path points by timestamp (insertion sort, typically small arrays)
func sortPathPoints(points []PathPoint) {
	for i := 1; i < len(points); i++ {
		key := points[i]
		j := i - 1
		for j >= 0 && points[j].Timestamp > key.Timestamp {
			points[j+1] = points[j]
			j--
		}
		points[j+1] = key
	}
}

// DB methods for paths

// CreateOrUpdatePath creates a new path or updates an existing one for user+date
func (db *DB) CreateOrUpdatePath(path *Path) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// Check if path exists for this user+date
	var existingID int64
	err = tx.QueryRow(
		`SELECT id FROM paths WHERE user_id = ? AND date = ?`,
		path.UserID, path.Date,
	).Scan(&existingID)

	if err == sql.ErrNoRows {
		// Insert new path
		result, err := tx.Exec(
			`INSERT INTO paths (user_id, date, start_ts, end_ts, min_lat, max_lat, min_lon, max_lon, point_count)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			path.UserID, path.Date, path.StartTS, path.EndTS,
			path.MinLat, path.MaxLat, path.MinLon, path.MaxLon, path.PointCount,
		)
		if err != nil {
			return err
		}
		path.ID, err = result.LastInsertId()
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		// Update existing path
		path.ID = existingID
		_, err = tx.Exec(
			`UPDATE paths SET start_ts = ?, end_ts = ?, min_lat = ?, max_lat = ?, min_lon = ?, max_lon = ?, point_count = ?
			 WHERE id = ?`,
			path.StartTS, path.EndTS, path.MinLat, path.MaxLat, path.MinLon, path.MaxLon, path.PointCount,
			path.ID,
		)
		if err != nil {
			return err
		}

		// Delete existing points
		_, err = tx.Exec(`DELETE FROM path_points WHERE path_id = ?`, path.ID)
		if err != nil {
			return err
		}
	}

	// Insert path points
	stmt, err := tx.Prepare(
		`INSERT INTO path_points (path_id, seq, timestamp, lat, lon) VALUES (?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for i, pt := range path.Points {
		_, err = stmt.Exec(path.ID, i, pt.Timestamp, pt.Lat, pt.Lon)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// QueryPathsByBBox returns all paths that intersect the given bounding box
func (db *DB) QueryPathsByBBox(bbox BBox, start, end *int64) ([]Path, error) {
	query := `SELECT id, user_id, date, start_ts, end_ts, min_lat, max_lat, min_lon, max_lon, point_count
			  FROM paths
			  WHERE max_lat >= ? AND min_lat <= ? AND max_lon >= ? AND min_lon <= ?`
	args := []any{bbox.SwLat, bbox.NeLat, bbox.SwLng, bbox.NeLng}

	if start != nil {
		query += " AND end_ts >= ?"
		args = append(args, *start)
	}
	if end != nil {
		query += " AND start_ts <= ?"
		args = append(args, *end)
	}

	query += " ORDER BY start_ts"

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var paths []Path
	for rows.Next() {
		var p Path
		if err := rows.Scan(&p.ID, &p.UserID, &p.Date, &p.StartTS, &p.EndTS,
			&p.MinLat, &p.MaxLat, &p.MinLon, &p.MaxLon, &p.PointCount); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}

	return paths, rows.Err()
}

// GetPathPoints retrieves all points for a given path ID
func (db *DB) GetPathPoints(pathID int64) ([]PathPoint, error) {
	rows, err := db.Query(
		`SELECT timestamp, lat, lon FROM path_points WHERE path_id = ? ORDER BY seq`,
		pathID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []PathPoint
	for rows.Next() {
		var pt PathPoint
		if err := rows.Scan(&pt.Timestamp, &pt.Lat, &pt.Lon); err != nil {
			return nil, err
		}
		points = append(points, pt)
	}

	return points, rows.Err()
}

// QueryPathsWithPoints returns paths with their points loaded and simplified for the viewport
func (db *DB) QueryPathsWithPoints(bbox BBox, start, end *int64) ([]Path, error) {
	paths, err := db.QueryPathsByBBox(bbox, start, end)
	if err != nil {
		return nil, err
	}

	// Calculate simplification tolerance based on viewport
	tolerance := ToleranceFromBBox(bbox)

	for i := range paths {
		points, err := db.GetPathPoints(paths[i].ID)
		if err != nil {
			return nil, err
		}
		// Simplify points for the current viewport
		paths[i].Points = SimplifyPath(points, tolerance)
	}

	return paths, nil
}

// RebuildAllPaths recomputes all paths from scratch
// Useful after algorithm changes or data corrections
func (db *DB) RebuildAllPaths() error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// Clear existing paths
	_, err = tx.Exec(`DELETE FROM path_points`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM paths`)
	if err != nil {
		return err
	}

	if err = tx.Commit(); err != nil {
		return err
	}

	// Query all locations
	rows, err := db.Query(`SELECT timestamp, user_id, device_id, lat, lon FROM locations ORDER BY timestamp`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var locations []Location
	for rows.Next() {
		var loc Location
		if err := rows.Scan(&loc.Timestamp, &loc.UserID, &loc.DeviceID, &loc.Lat, &loc.Lon); err != nil {
			return err
		}
		locations = append(locations, loc)
	}
	if err = rows.Err(); err != nil {
		return err
	}

	// Compute paths
	paths := ComputePathsForLocations(locations)

	// Store paths
	for _, path := range paths {
		if err := db.CreateOrUpdatePath(path); err != nil {
			return err
		}
	}

	return nil
}

// UpdatePathsForLocations updates paths for a batch of new locations
func (db *DB) UpdatePathsForLocations(locations []Location) error {
	if len(locations) == 0 {
		return nil
	}

	// Group locations by user+date
	byUserDate := make(map[string][]Location)
	for _, loc := range locations {
		date := LocalDateFromTimestamp(loc.Timestamp, loc.Lat, loc.Lon)
		key := loc.UserID + "|" + date
		byUserDate[key] = append(byUserDate[key], loc)
	}

	// For each user+date, fetch existing path and merge
	for key, locs := range byUserDate {
		userID := locs[0].UserID
		date := LocalDateFromTimestamp(locs[0].Timestamp, locs[0].Lat, locs[0].Lon)

		// Fetch all locations for this user+date from DB
		// We need to recompute the entire path for that day
		allLocs, err := db.QueryLocationsByUserDate(userID, date)
		if err != nil {
			return err
		}

		// Compute the path
		paths := ComputePathsForLocations(allLocs)
		path := paths[key]
		if path == nil {
			continue // Should not happen
		}

		// Store/update the path
		if err := db.CreateOrUpdatePath(path); err != nil {
			return err
		}
	}

	return nil
}

// QueryLocationsByUserDate returns all locations for a user on a specific date
func (db *DB) QueryLocationsByUserDate(userID, date string) ([]Location, error) {
	// Parse date to get timestamp range
	// We need to find all locations that fall on this date in their local timezone
	// This is tricky because we stored timestamps in UTC but dates are local
	// For now, we'll query a wide range and filter

	// Parse the date
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil, err
	}

	// Query with a wide time window (date +-1 day in any timezone)
	// Then filter by actual local date
	startTS := t.Add(-36 * time.Hour).Unix()
	endTS := t.Add(48 * time.Hour).Unix()

	rows, err := db.Query(
		`SELECT timestamp, user_id, device_id, lat, lon FROM locations
		 WHERE user_id = ? AND timestamp >= ? AND timestamp <= ?
		 ORDER BY timestamp`,
		userID, startTS, endTS,
	)
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
		// Filter by actual local date
		localDate := LocalDateFromTimestamp(loc.Timestamp, loc.Lat, loc.Lon)
		if localDate == date {
			locations = append(locations, loc)
		}
	}

	return locations, rows.Err()
}
