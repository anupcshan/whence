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

// StationaryCluster represents a period where the user was stationary at one location.
// Used for timeline features and path simplification.
type StationaryCluster struct {
	Lat        float64 `json:"lat"`         // Anchor point latitude (first point in cluster)
	Lon        float64 `json:"lon"`         // Anchor point longitude
	StartTS    int64   `json:"start_ts"`    // First point timestamp
	EndTS      int64   `json:"end_ts"`      // Last point timestamp
	PointCount int     `json:"point_count"` // Number of raw points in cluster
}

// PruneResult contains the simplified path, detected stationary clusters, and removed points.
type PruneResult struct {
	Points   []PathPoint         `json:"points"`
	Removed  []PathPoint         `json:"removed"`
	Clusters []StationaryCluster `json:"clusters"`
}

// haversineMeters calculates the distance in meters between two lat/lon points.
func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadius = 6371000 // meters

	lat1Rad := lat1 * math.Pi / 180
	lat2Rad := lat2 * math.Pi / 180
	deltaLat := (lat2 - lat1) * math.Pi / 180
	deltaLon := (lon2 - lon1) * math.Pi / 180

	a := math.Sin(deltaLat/2)*math.Sin(deltaLat/2) +
		math.Cos(lat1Rad)*math.Cos(lat2Rad)*
			math.Sin(deltaLon/2)*math.Sin(deltaLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	return earthRadius * c
}

// PruneStationaryPoints removes redundant points when the user is stationary.
// Points within minDistMeters of the cluster anchor are considered stationary.
// Returns the simplified path, removed points, and detected stationary clusters.
func PruneStationaryPoints(points []PathPoint, minDistMeters float64) PruneResult {
	if len(points) == 0 {
		return PruneResult{}
	}
	if len(points) == 1 {
		return PruneResult{
			Points: points,
			Clusters: []StationaryCluster{{
				Lat:        points[0].Lat,
				Lon:        points[0].Lon,
				StartTS:    points[0].Timestamp,
				EndTS:      points[0].Timestamp,
				PointCount: 1,
			}},
		}
	}

	var result []PathPoint
	var removed []PathPoint
	var clusters []StationaryCluster

	// Start first cluster with first point
	cluster := StationaryCluster{
		Lat:        points[0].Lat,
		Lon:        points[0].Lon,
		StartTS:    points[0].Timestamp,
		EndTS:      points[0].Timestamp,
		PointCount: 1,
	}

	for i := 1; i < len(points); i++ {
		pt := points[i]
		dist := haversineMeters(cluster.Lat, cluster.Lon, pt.Lat, pt.Lon)

		if dist < minDistMeters {
			// Point is within threshold - add to current cluster
			cluster.EndTS = pt.Timestamp
			cluster.PointCount++
			// Track this as a removed point
			removed = append(removed, pt)
		} else {
			// Point is outside threshold - finalize cluster and start new one
			// Emit representative point for the cluster
			result = append(result, PathPoint{
				Lat:       cluster.Lat,
				Lon:       cluster.Lon,
				Timestamp: cluster.StartTS,
			})
			clusters = append(clusters, cluster)

			// Start new cluster
			cluster = StationaryCluster{
				Lat:        pt.Lat,
				Lon:        pt.Lon,
				StartTS:    pt.Timestamp,
				EndTS:      pt.Timestamp,
				PointCount: 1,
			}
		}
	}

	// Finalize last cluster
	result = append(result, PathPoint{
		Lat:       cluster.Lat,
		Lon:       cluster.Lon,
		Timestamp: cluster.StartTS,
	})
	clusters = append(clusters, cluster)

	return PruneResult{
		Points:   result,
		Removed:  removed,
		Clusters: clusters,
	}
}

// SpikeResult contains the filtered path and removed spike points.
type SpikeResult struct {
	Points  []PathPoint `json:"points"`
	Removed []PathPoint `json:"removed"`
}

// RemoveSpikes detects and removes outlier points that form spikes.
// A spike is point B where: dist(A,B) > threshold AND dist(B,C) > threshold,
// but dist(A,C) < threshold (B sticks out while A and C are close).
// Returns kept points and removed spike points separately.
func RemoveSpikes(points []PathPoint, thresholdMeters float64) SpikeResult {
	if len(points) < 3 {
		return SpikeResult{Points: points}
	}

	// We need to handle consecutive spikes, so we use a sliding window approach
	// that compares against the last KEPT point, not the last point in sequence.
	kept := []PathPoint{points[0]} // Always keep first point
	var removed []PathPoint

	i := 1
	for i < len(points)-1 {
		A := kept[len(kept)-1] // Last kept point
		B := points[i]
		C := points[i+1]

		distAB := haversineMeters(A.Lat, A.Lon, B.Lat, B.Lon)
		distBC := haversineMeters(B.Lat, B.Lon, C.Lat, C.Lon)
		distAC := haversineMeters(A.Lat, A.Lon, C.Lat, C.Lon)

		// B is a spike if it's far from both A and C, but A and C are close
		if distAB > thresholdMeters && distBC > thresholdMeters && distAC < thresholdMeters {
			removed = append(removed, B)
		} else {
			kept = append(kept, B)
		}
		i++
	}

	// Always keep last point
	kept = append(kept, points[len(points)-1])

	return SpikeResult{
		Points:  kept,
		Removed: removed,
	}
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

// SimplifyOptions configures the path simplification pipeline.
type SimplifyOptions struct {
	PruneMeters float64  // Stationary point pruning threshold (0 = disabled)
	SpikeMeters float64  // Spike detection threshold (0 = disabled)
	Order       []string // Order of operations, e.g. ["stationary", "spikes"]
}

// RemovedPoints tracks points removed by each simplification stage.
type RemovedPoints struct {
	Stationary []PathPoint `json:"stationary"`
	Spikes     []PathPoint `json:"spikes"`
}

// PathsResult contains paths and information about removed points.
type PathsResult struct {
	Paths   []Path        `json:"paths"`
	Removed RemovedPoints `json:"removed"`
}

// QueryPathsWithPoints returns paths with their points loaded and simplified for the viewport.
// The simplification pipeline is configured via SimplifyOptions.
func (db *DB) QueryPathsWithPoints(bbox BBox, start, end *int64, opts SimplifyOptions) (PathsResult, error) {
	paths, err := db.QueryPathsByBBox(bbox, start, end)
	if err != nil {
		return PathsResult{}, err
	}

	// Calculate simplification tolerance based on viewport
	tolerance := ToleranceFromBBox(bbox)

	var allRemovedStationary []PathPoint
	var allRemovedSpikes []PathPoint

	for i := range paths {
		points, err := db.GetPathPoints(paths[i].ID)
		if err != nil {
			return PathsResult{}, err
		}

		// Apply simplification stages in specified order
		for _, stage := range opts.Order {
			switch stage {
			case "stationary":
				if opts.PruneMeters > 0 {
					result := PruneStationaryPoints(points, opts.PruneMeters)
					points = result.Points
					allRemovedStationary = append(allRemovedStationary, result.Removed...)
				}
			case "spikes":
				if opts.SpikeMeters > 0 {
					result := RemoveSpikes(points, opts.SpikeMeters)
					points = result.Points
					allRemovedSpikes = append(allRemovedSpikes, result.Removed...)
				}
			}
		}

		// Finally, apply Douglas-Peucker simplification for viewport
		paths[i].Points = SimplifyPath(points, tolerance)
	}

	return PathsResult{
		Paths: paths,
		Removed: RemovedPoints{
			Stationary: allRemovedStationary,
			Spikes:     allRemovedSpikes,
		},
	}, nil
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
