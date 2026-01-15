package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// GeocodingService handles reverse geocoding using Nominatim API
type GeocodingService struct {
	db          *DB
	httpClient  *http.Client
	lastRequest time.Time
	rateMu      sync.Mutex
}

// GeocodedPlace represents a reverse geocoded result
type GeocodedPlace struct {
	PlaceName   string  `json:"place_name"`
	PlaceType   string  `json:"place_type,omitempty"`
	DisplayName string  `json:"display_name,omitempty"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
}

// LatLon is a simple lat/lon pair for batch operations
type LatLon struct {
	Lat float64
	Lon float64
}

// NewGeocodingService creates a new geocoding service
func NewGeocodingService(db *DB) *GeocodingService {
	return &GeocodingService{
		db: db,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ReverseGeocodeBatch geocodes multiple points using Nominatim API
// Respects Nominatim's 1 request/second rate limit
func (g *GeocodingService) ReverseGeocodeBatch(ctx context.Context, points []LatLon) (map[int]*GeocodedPlace, error) {
	results := make(map[int]*GeocodedPlace)

	if len(points) == 0 {
		return results, nil
	}

	for i, pt := range points {
		// Check database cache first
		cached, err := g.lookupCache(pt.Lat, pt.Lon)
		if err == nil && cached != nil {
			results[i] = cached
			continue
		}

		// Rate limit: 1 request per second
		g.rateMu.Lock()
		elapsed := time.Since(g.lastRequest)
		if elapsed < time.Second {
			time.Sleep(time.Second - elapsed)
		}
		g.lastRequest = time.Now()
		g.rateMu.Unlock()

		// Fetch from Nominatim
		place, err := g.fetchFromNominatim(ctx, pt.Lat, pt.Lon)
		if err != nil {
			fmt.Printf("[nominatim] ERROR for (%.6f,%.6f): %v\n", pt.Lat, pt.Lon, err)
			continue
		}

		if place != nil {
			results[i] = place
		}
	}

	return results, nil
}

// lookupCache checks if a point falls within any cached bounding box
func (g *GeocodingService) lookupCache(lat, lon float64) (*GeocodedPlace, error) {
	row := g.db.QueryRow(`
		SELECT place_name, place_type, display_name
		FROM geocache
		WHERE ? >= min_lat AND ? <= max_lat AND ? >= min_lon AND ? <= max_lon
		LIMIT 1
	`, lat, lat, lon, lon)

	var placeName, placeType, displayName string
	err := row.Scan(&placeName, &placeType, &displayName)
	if err != nil {
		return nil, err
	}

	return &GeocodedPlace{
		PlaceName:   placeName,
		PlaceType:   placeType,
		DisplayName: displayName,
		Lat:         lat,
		Lon:         lon,
	}, nil
}

// insertCache stores a geocoding result with its bounding box
func (g *GeocodingService) insertCache(minLat, maxLat, minLon, maxLon float64, place *GeocodedPlace) error {
	_, err := g.db.Exec(`
		INSERT OR IGNORE INTO geocache (min_lat, max_lat, min_lon, max_lon, place_name, place_type, display_name, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, minLat, maxLat, minLon, maxLon, place.PlaceName, place.PlaceType, place.DisplayName, time.Now().Unix())
	return err
}

// nominatimResponse represents the JSON response from Nominatim reverse API
type nominatimResponse struct {
	PlaceID     int64    `json:"place_id"`
	Lat         string   `json:"lat"`
	Lon         string   `json:"lon"`
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Type        string   `json:"type"`
	Category    string   `json:"category"`
	BoundingBox []string `json:"boundingbox"` // [min_lat, max_lat, min_lon, max_lon]
	Address     address  `json:"address"`
}

type address struct {
	Amenity       string `json:"amenity,omitempty"`
	Shop          string `json:"shop,omitempty"`
	Tourism       string `json:"tourism,omitempty"`
	Leisure       string `json:"leisure,omitempty"`
	Building      string `json:"building,omitempty"`
	HouseNumber   string `json:"house_number,omitempty"`
	Road          string `json:"road,omitempty"`
	Neighbourhood string `json:"neighbourhood,omitempty"`
	Suburb        string `json:"suburb,omitempty"`
	City          string `json:"city,omitempty"`
	Town          string `json:"town,omitempty"`
	Village       string `json:"village,omitempty"`
	State         string `json:"state,omitempty"`
	Country       string `json:"country,omitempty"`
}

// fetchFromNominatim queries Nominatim for reverse geocoding
func (g *GeocodingService) fetchFromNominatim(ctx context.Context, lat, lon float64) (*GeocodedPlace, error) {
	// Build URL - zoom=18 gives building-level detail
	reqURL := fmt.Sprintf(
		"https://nominatim.openstreetmap.org/reverse?lat=%.6f&lon=%.6f&format=jsonv2&zoom=18&addressdetails=1",
		lat, lon,
	)

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	// Required by Nominatim ToS
	req.Header.Set("User-Agent", "Whence/1.0 (location-history-app)")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nominatim request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nominatim returned status %d", resp.StatusCode)
	}

	var nr nominatimResponse
	if err := json.NewDecoder(resp.Body).Decode(&nr); err != nil {
		return nil, fmt.Errorf("failed to parse nominatim response: %w", err)
	}

	// Extract best place name
	placeName := extractPlaceName(nr)
	if placeName == "" {
		return nil, nil // No useful result
	}

	place := &GeocodedPlace{
		PlaceName:   placeName,
		PlaceType:   nr.Type,
		DisplayName: nr.DisplayName,
		Lat:         lat,
		Lon:         lon,
	}

	// Cache the result using bounding box from Nominatim
	// Expand bbox to include query point if needed
	if len(nr.BoundingBox) == 4 {
		minLat, _ := strconv.ParseFloat(nr.BoundingBox[0], 64)
		maxLat, _ := strconv.ParseFloat(nr.BoundingBox[1], 64)
		minLon, _ := strconv.ParseFloat(nr.BoundingBox[2], 64)
		maxLon, _ := strconv.ParseFloat(nr.BoundingBox[3], 64)

		// Expand bbox to include the query point
		if lat < minLat {
			minLat = lat
		}
		if lat > maxLat {
			maxLat = lat
		}
		if lon < minLon {
			minLon = lon
		}
		if lon > maxLon {
			maxLon = lon
		}

		if err := g.insertCache(minLat, maxLat, minLon, maxLon, place); err != nil {
			fmt.Printf("[geocache] INSERT ERROR: %v\n", err)
		}
	}

	fmt.Printf("[nominatim] (%.6f,%.6f) -> %q\n", lat, lon, placeName)

	return place, nil
}

// extractPlaceName gets the most useful place name from a Nominatim response
func extractPlaceName(nr nominatimResponse) string {
	// Prefer specific named places
	if nr.Name != "" {
		return nr.Name
	}

	// Check address components for named places
	addr := nr.Address
	if addr.Amenity != "" {
		return addr.Amenity
	}
	if addr.Shop != "" {
		return addr.Shop
	}
	if addr.Tourism != "" {
		return addr.Tourism
	}
	if addr.Leisure != "" {
		return addr.Leisure
	}
	if addr.Building != "" && addr.Building != "yes" {
		return addr.Building
	}

	// Fall back to street address
	if addr.Road != "" {
		if addr.HouseNumber != "" {
			return addr.HouseNumber + " " + addr.Road
		}
		return addr.Road
	}

	// Fall back to neighborhood/suburb
	if addr.Neighbourhood != "" {
		return addr.Neighbourhood
	}
	if addr.Suburb != "" {
		return addr.Suburb
	}

	// Fall back to city/town
	if addr.City != "" {
		return addr.City
	}
	if addr.Town != "" {
		return addr.Town
	}
	if addr.Village != "" {
		return addr.Village
	}

	return ""
}
