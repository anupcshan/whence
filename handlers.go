package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	db            *DB
	defaultUserID string
}

// OwnTracks JSON format
type OwnTracksPayload struct {
	Type      string  `json:"_type"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	Timestamp int64   `json:"tst"`
	TrackerID string  `json:"tid"`
}

// POST /owntracks - OwnTracks compatible endpoint
func (s *Server) handleOwnTracks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload OwnTracksPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	// Ignore non-location messages
	if payload.Type != "location" {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{})
		return
	}

	userID := r.Header.Get("X-Limit-U")
	if userID == "" {
		userID = s.defaultUserID
	}

	loc := Location{
		Timestamp: payload.Timestamp,
		UserID:    userID,
		DeviceID:  payload.TrackerID,
		Lat:       payload.Lat,
		Lon:       payload.Lon,
	}

	if err := s.db.InsertLocation(loc); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{})
}

// GET /gpslogger - GPSLogger compatible endpoint
func (s *Server) handleGPSLogger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	latStr := r.URL.Query().Get("lat")
	lonStr := r.URL.Query().Get("lon")
	timeStr := r.URL.Query().Get("time")

	lat, err := strconv.ParseFloat(latStr, 64)
	if err != nil {
		http.Error(w, "invalid lat", http.StatusBadRequest)
		return
	}

	lon, err := strconv.ParseFloat(lonStr, 64)
	if err != nil {
		http.Error(w, "invalid lon", http.StatusBadRequest)
		return
	}

	var timestamp int64
	if timeStr != "" {
		// Try parsing as Unix timestamp first
		if ts, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
			timestamp = ts
		} else {
			// Try ISO 8601 format
			if t, err := time.Parse(time.RFC3339, timeStr); err == nil {
				timestamp = t.Unix()
			} else {
				timestamp = time.Now().Unix()
			}
		}
	} else {
		timestamp = time.Now().Unix()
	}

	loc := Location{
		Timestamp: timestamp,
		UserID:    s.defaultUserID,
		DeviceID:  "gpslogger",
		Lat:       lat,
		Lon:       lon,
	}

	if err := s.db.InsertLocation(loc); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// GeoJSON types
type GeoJSONFeatureCollection struct {
	Type     string           `json:"type"`
	Features []GeoJSONFeature `json:"features"`
}

type GeoJSONFeature struct {
	Type       string         `json:"type"`
	Geometry   GeoJSONPoint   `json:"geometry"`
	Properties map[string]any `json:"properties"`
}

type GeoJSONPoint struct {
	Type        string    `json:"type"`
	Coordinates []float64 `json:"coordinates"`
}

// GET /api/locations - GeoJSON endpoint for Leaflet
func (s *Server) handleAPILocations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bboxStr := r.URL.Query().Get("bbox")
	if bboxStr == "" {
		http.Error(w, "bbox required", http.StatusBadRequest)
		return
	}

	// Parse bbox: sw_lng,sw_lat,ne_lng,ne_lat
	parts := strings.Split(bboxStr, ",")
	if len(parts) != 4 {
		http.Error(w, "invalid bbox format", http.StatusBadRequest)
		return
	}

	bbox := BBox{}
	var err error
	bbox.SwLng, err = strconv.ParseFloat(parts[0], 64)
	if err != nil {
		http.Error(w, "invalid bbox", http.StatusBadRequest)
		return
	}
	bbox.SwLat, err = strconv.ParseFloat(parts[1], 64)
	if err != nil {
		http.Error(w, "invalid bbox", http.StatusBadRequest)
		return
	}
	bbox.NeLng, err = strconv.ParseFloat(parts[2], 64)
	if err != nil {
		http.Error(w, "invalid bbox", http.StatusBadRequest)
		return
	}
	bbox.NeLat, err = strconv.ParseFloat(parts[3], 64)
	if err != nil {
		http.Error(w, "invalid bbox", http.StatusBadRequest)
		return
	}

	var start, end *int64
	if startStr := r.URL.Query().Get("start"); startStr != "" {
		if v, err := strconv.ParseInt(startStr, 10, 64); err == nil {
			start = &v
		}
	}
	if endStr := r.URL.Query().Get("end"); endStr != "" {
		if v, err := strconv.ParseInt(endStr, 10, 64); err == nil {
			end = &v
		}
	}

	locations, err := s.db.QueryLocations(bbox, start, end)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	fc := GeoJSONFeatureCollection{
		Type:     "FeatureCollection",
		Features: make([]GeoJSONFeature, 0, len(locations)),
	}

	for _, loc := range locations {
		fc.Features = append(fc.Features, GeoJSONFeature{
			Type: "Feature",
			Geometry: GeoJSONPoint{
				Type:        "Point",
				Coordinates: []float64{loc.Lon, loc.Lat},
			},
			Properties: map[string]any{
				"timestamp": loc.Timestamp,
				"user_id":   loc.UserID,
				"device_id": loc.DeviceID,
			},
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fc)
}

// GET /api/latest - Returns the most recent location
func (s *Server) handleAPILatest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	loc, err := s.db.LatestLocation()
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if loc == nil {
		w.Write([]byte("null"))
		return
	}
	json.NewEncoder(w).Encode(loc)
}
