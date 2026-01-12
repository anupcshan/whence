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

	// Update paths for this location (ignore errors - location is already saved)
	_ = s.db.UpdatePathsForLocations([]Location{loc})

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

	// Update paths for this location (ignore errors - location is already saved)
	_ = s.db.UpdatePathsForLocations([]Location{loc})

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// PathsResponse is the API response for /api/paths
type PathsResponse struct {
	Paths   []Path     `json:"paths"`
	Current *PathPoint `json:"current"`
}

// parseBBox parses a bounding box string in format sw_lng,sw_lat,ne_lng,ne_lat
func parseBBox(bboxStr string) (BBox, error) {
	parts := strings.Split(bboxStr, ",")
	if len(parts) != 4 {
		return BBox{}, errInvalidBBox
	}

	var bbox BBox
	var err error
	bbox.SwLng, err = strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return BBox{}, errInvalidBBox
	}
	bbox.SwLat, err = strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return BBox{}, errInvalidBBox
	}
	bbox.NeLng, err = strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return BBox{}, errInvalidBBox
	}
	bbox.NeLat, err = strconv.ParseFloat(parts[3], 64)
	if err != nil {
		return BBox{}, errInvalidBBox
	}
	return bbox, nil
}

var errInvalidBBox = &httpError{code: http.StatusBadRequest, msg: "invalid bbox format"}

type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

// GET /api/paths - Returns pre-computed paths intersecting the bounding box
func (s *Server) handleAPIPaths(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bboxStr := r.URL.Query().Get("bbox")
	if bboxStr == "" {
		http.Error(w, "bbox required", http.StatusBadRequest)
		return
	}

	bbox, err := parseBBox(bboxStr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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

	paths, err := s.db.QueryPathsWithPoints(bbox, start, end)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	// Get current location
	var current *PathPoint
	loc, err := s.db.LatestLocation()
	if err == nil && loc != nil {
		current = &PathPoint{
			Lat:       loc.Lat,
			Lon:       loc.Lon,
			Timestamp: loc.Timestamp,
		}
	}

	resp := PathsResponse{
		Paths:   paths,
		Current: current,
	}

	if resp.Paths == nil {
		resp.Paths = []Path{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// POST /api/paths/rebuild - Rebuilds all paths from scratch
func (s *Server) handleAPIPathsRebuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := s.db.RebuildAllPaths(); err != nil {
		http.Error(w, "rebuild failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// GET /api/bounds - Returns the bounding box for locations in a time range
func (s *Server) handleAPIBounds(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")
	if startStr == "" || endStr == "" {
		http.Error(w, "start and end timestamps required", http.StatusBadRequest)
		return
	}

	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid start timestamp", http.StatusBadRequest)
		return
	}
	end, err := strconv.ParseInt(endStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid end timestamp", http.StatusBadRequest)
		return
	}

	bounds, err := s.db.GetBoundsForTimestampRange(start, end)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if bounds == nil {
		w.Write([]byte("null"))
		return
	}
	json.NewEncoder(w).Encode(bounds)
}

// LocationSourceResponse is the API response for /api/location/source
type LocationSourceResponse struct {
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
	WebURL     string `json:"web_url,omitempty"`
	Filename   string `json:"filename,omitempty"`
	Make       string `json:"make,omitempty"`
	Model      string `json:"model,omitempty"`
}

// GET /api/location/source - Returns source metadata for a location point
func (s *Server) handleAPILocationSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tsStr := r.URL.Query().Get("timestamp")
	deviceID := r.URL.Query().Get("device_id")

	if tsStr == "" {
		http.Error(w, "timestamp required", http.StatusBadRequest)
		return
	}

	timestamp, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid timestamp", http.StatusBadRequest)
		return
	}

	var source *LocationSource
	if deviceID != "" {
		source, err = s.db.GetLocationSource(timestamp, deviceID)
	} else {
		source, err = s.db.GetLocationSourceByTimestamp(timestamp)
	}
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if source == nil {
		w.Write([]byte("null"))
		return
	}

	// Parse metadata JSON to extract fields
	resp := LocationSourceResponse{
		SourceType: source.SourceType,
		SourceID:   source.SourceID,
	}

	if source.Metadata != "" {
		var meta map[string]string
		if err := json.Unmarshal([]byte(source.Metadata), &meta); err == nil {
			resp.WebURL = meta["web_url"]
			resp.Filename = meta["filename"]
			resp.Make = meta["make"]
			resp.Model = meta["model"]
		}
	}

	json.NewEncoder(w).Encode(resp)
}
