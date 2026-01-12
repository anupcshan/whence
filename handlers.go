package main

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
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

// PhotoCluster represents a group of nearby photos for the map
type PhotoCluster struct {
	Lat          float64 `json:"lat"`
	Lon          float64 `json:"lon"`
	Count        int     `json:"count"`
	ThumbnailURL string  `json:"thumbnail_url"`
	PopupHTML    string  `json:"popup_html"`
}

// PhotosResponse is the response for /api/photos
type PhotosResponse struct {
	Clusters []PhotoCluster `json:"clusters"`
}

// clusterRadiusFromBBox calculates clustering radius based on viewport size
func clusterRadiusFromBBox(bbox BBox) float64 {
	latSpan := bbox.NeLat - bbox.SwLat
	lonSpan := bbox.NeLng - bbox.SwLng

	minSpan := latSpan
	if lonSpan < minSpan {
		minSpan = lonSpan
	}

	// Use 2% of viewport as cluster radius
	radius := minSpan * 0.02

	// Clamp to reasonable bounds
	// Min: ~50m (0.0005 degrees)
	// Max: ~10km (0.1 degrees)
	if radius < 0.0005 {
		radius = 0.0005
	}
	if radius > 0.1 {
		radius = 0.1
	}

	return radius
}

// photoDist calculates approximate distance between two points in degrees
func photoDist(lat1, lon1, lat2, lon2 float64) float64 {
	dLat := lat2 - lat1
	dLon := lon2 - lon1
	return math.Sqrt(dLat*dLat + dLon*dLon)
}

// photoClusterData is an internal struct for building clusters
type photoClusterData struct {
	lat    float64
	lon    float64
	photos []PhotoLocation
}

// clusterPhotos groups photos by proximity
func clusterPhotos(photos []PhotoLocation, radius float64) []photoClusterData {
	var clusters []photoClusterData

	for _, photo := range photos {
		added := false
		for i := range clusters {
			if photoDist(clusters[i].lat, clusters[i].lon, photo.Lat, photo.Lon) < radius {
				// Add to existing cluster and update centroid
				n := float64(len(clusters[i].photos))
				clusters[i].lat = (clusters[i].lat*n + photo.Lat) / (n + 1)
				clusters[i].lon = (clusters[i].lon*n + photo.Lon) / (n + 1)
				clusters[i].photos = append(clusters[i].photos, photo)
				added = true
				break
			}
		}
		if !added {
			clusters = append(clusters, photoClusterData{
				lat:    photo.Lat,
				lon:    photo.Lon,
				photos: []PhotoLocation{photo},
			})
		}
	}

	return clusters
}

// buildPopupHTML generates the HTML for the photo grid popup
func buildPopupHTML(photos []PhotoLocation) string {
	var popup strings.Builder
	popup.WriteString(`<div class="photo-grid">`)
	for _, photo := range photos {
		previewURL := fmt.Sprintf("/api/immich/assets/%s/thumbnail?size=preview", photo.SourceID)
		popup.WriteString(fmt.Sprintf(
			`<a href="%s" target="_blank" rel="noopener" title="%s"><img src="%s" alt=""></a>`,
			html.EscapeString(photo.WebURL),
			html.EscapeString(photo.Filename),
			previewURL,
		))
	}
	popup.WriteString(`</div>`)
	return popup.String()
}

// GET /api/photos - Returns clustered photos for a time range and bounding box
func (s *Server) handleAPIPhotos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse time range
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

	// Parse bbox for clustering radius
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

	// Query photos from database
	photos, err := s.db.QueryPhotoLocations(start, end)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	// Cluster photos based on viewport
	radius := clusterRadiusFromBBox(bbox)
	clusters := clusterPhotos(photos, radius)

	// Build response with pre-rendered HTML
	var response []PhotoCluster
	for _, cluster := range clusters {
		// Key photo is the last one (most recent, since photos are sorted by timestamp)
		keyPhoto := cluster.photos[len(cluster.photos)-1]

		response = append(response, PhotoCluster{
			Lat:          keyPhoto.Lat,
			Lon:          keyPhoto.Lon,
			Count:        len(cluster.photos),
			ThumbnailURL: fmt.Sprintf("/api/immich/assets/%s/thumbnail", keyPhoto.SourceID),
			PopupHTML:    buildPopupHTML(cluster.photos),
		})
	}

	resp := PhotosResponse{
		Clusters: response,
	}
	if resp.Clusters == nil {
		resp.Clusters = []PhotoCluster{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
