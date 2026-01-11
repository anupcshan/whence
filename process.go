package main

import "math"

const (
	stayRadiusMeters   = 50.0
	stayMinDurationSec = 5 * 60 // 5 minutes
	earthRadiusMeters  = 6371000.0
	simplifyTolerance  = 0.0001 // ~11 meters in degrees
)

type Stay struct {
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Start int64   `json:"start"`
	End   int64   `json:"end"`
	Count int     `json:"count"`
}

type PathPoint struct {
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	Timestamp int64   `json:"timestamp"`
}

type Timeline struct {
	Stays   []Stay        `json:"stays"`
	Paths   [][]PathPoint `json:"paths"`
	Current *PathPoint    `json:"current"`
}

// haversine calculates the distance in meters between two points.
func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	lat1Rad := lat1 * math.Pi / 180
	lat2Rad := lat2 * math.Pi / 180
	deltaLat := (lat2 - lat1) * math.Pi / 180
	deltaLon := (lon2 - lon1) * math.Pi / 180

	a := math.Sin(deltaLat/2)*math.Sin(deltaLat/2) +
		math.Cos(lat1Rad)*math.Cos(lat2Rad)*math.Sin(deltaLon/2)*math.Sin(deltaLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	return earthRadiusMeters * c
}

type cluster struct {
	points []Location
	lat    float64
	lon    float64
}

func newCluster(loc Location) *cluster {
	return &cluster{
		points: []Location{loc},
		lat:    loc.Lat,
		lon:    loc.Lon,
	}
}

func (c *cluster) add(loc Location) {
	c.points = append(c.points, loc)
	// Update centroid as running average
	n := float64(len(c.points))
	c.lat = c.lat*(n-1)/n + loc.Lat/n
	c.lon = c.lon*(n-1)/n + loc.Lon/n
}

func (c *cluster) duration() int64 {
	if len(c.points) == 0 {
		return 0
	}
	return c.points[len(c.points)-1].Timestamp - c.points[0].Timestamp
}

func (c *cluster) toStay() Stay {
	return Stay{
		Lat:   c.lat,
		Lon:   c.lon,
		Start: c.points[0].Timestamp,
		End:   c.points[len(c.points)-1].Timestamp,
		Count: len(c.points),
	}
}

func (c *cluster) toPathPoints() []PathPoint {
	points := make([]PathPoint, len(c.points))
	for i, loc := range c.points {
		points[i] = PathPoint{
			Lat:       loc.Lat,
			Lon:       loc.Lon,
			Timestamp: loc.Timestamp,
		}
	}
	return points
}

// ProcessLocations converts raw location points into a timeline of stays and paths.
func ProcessLocations(locations []Location) Timeline {
	timeline := Timeline{
		Stays: []Stay{},
		Paths: [][]PathPoint{},
	}

	if len(locations) == 0 {
		return timeline
	}

	var currentCluster *cluster
	var currentPath []PathPoint

	finalizeCluster := func() {
		if currentCluster == nil {
			return
		}

		if currentCluster.duration() >= stayMinDurationSec {
			// This is a stay
			timeline.Stays = append(timeline.Stays, currentCluster.toStay())

			// Save the current path if it has points
			if len(currentPath) > 0 {
				timeline.Paths = append(timeline.Paths, simplifyPath(currentPath))
				currentPath = nil
			}

			// Start a new path from the stay location
			stay := currentCluster.toStay()
			currentPath = []PathPoint{{
				Lat:       stay.Lat,
				Lon:       stay.Lon,
				Timestamp: stay.End,
			}}
		} else {
			// Not a stay, merge into current path
			currentPath = append(currentPath, currentCluster.toPathPoints()...)
		}

		currentCluster = nil
	}

	for _, loc := range locations {
		if currentCluster == nil {
			currentCluster = newCluster(loc)
			continue
		}

		dist := haversine(currentCluster.lat, currentCluster.lon, loc.Lat, loc.Lon)

		if dist <= stayRadiusMeters {
			currentCluster.add(loc)
		} else {
			finalizeCluster()
			currentCluster = newCluster(loc)
		}
	}

	// Handle the final cluster
	if currentCluster != nil {
		if currentCluster.duration() >= stayMinDurationSec {
			// Final cluster is a stay
			timeline.Stays = append(timeline.Stays, currentCluster.toStay())
			if len(currentPath) > 0 {
				timeline.Paths = append(timeline.Paths, simplifyPath(currentPath))
			}
		} else {
			// Final cluster is not a stay - add to path and set current location
			currentPath = append(currentPath, currentCluster.toPathPoints()...)
			if len(currentPath) > 0 {
				simplified := simplifyPath(currentPath)
				if len(simplified) > 1 {
					timeline.Paths = append(timeline.Paths, simplified)
				}
				last := currentPath[len(currentPath)-1]
				timeline.Current = &last
			}
		}
	} else if len(currentPath) > 0 {
		simplified := simplifyPath(currentPath)
		if len(simplified) > 1 {
			timeline.Paths = append(timeline.Paths, simplified)
		}
		last := currentPath[len(currentPath)-1]
		timeline.Current = &last
	}

	return timeline
}

// simplifyPath reduces the number of points using the Douglas-Peucker algorithm.
func simplifyPath(points []PathPoint) []PathPoint {
	if len(points) <= 2 {
		return points
	}

	// Find the point with the maximum distance from the line between first and last
	maxDist := 0.0
	maxIdx := 0

	first := points[0]
	last := points[len(points)-1]

	for i := 1; i < len(points)-1; i++ {
		dist := perpendicularDistance(points[i], first, last)
		if dist > maxDist {
			maxDist = dist
			maxIdx = i
		}
	}

	// If max distance is greater than tolerance, recursively simplify
	if maxDist > simplifyTolerance {
		left := simplifyPath(points[:maxIdx+1])
		right := simplifyPath(points[maxIdx:])

		// Combine results, avoiding duplicate point at maxIdx
		result := make([]PathPoint, 0, len(left)+len(right)-1)
		result = append(result, left[:len(left)-1]...)
		result = append(result, right...)
		return result
	}

	// All points are within tolerance, return just endpoints
	return []PathPoint{first, last}
}

// perpendicularDistance calculates the perpendicular distance from a point to a line.
func perpendicularDistance(point, lineStart, lineEnd PathPoint) float64 {
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
