package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ImmichHandlers holds handlers for Immich-related endpoints
type ImmichHandlers struct {
	config  *Config
	client  *ImmichClient
	manager *BackfillManager
	db      *DB
}

// NewImmichHandlers creates handlers for Immich endpoints
func NewImmichHandlers(cfg *Config, db *DB) *ImmichHandlers {
	h := &ImmichHandlers{
		config: cfg,
		db:     db,
	}

	if cfg != nil && cfg.ImmichConfigured() {
		h.client = NewImmichClient(cfg.Immich.URL, cfg.Immich.APIKey)
		h.manager = NewBackfillManager(db, h.client)
	}

	return h
}

// requireImmich checks if Immich is configured and returns error if not
func (h *ImmichHandlers) requireImmich(w http.ResponseWriter) bool {
	if h.client == nil {
		http.Error(w, `{"error":"Immich not configured. Add immich section to config.yaml"}`, http.StatusServiceUnavailable)
		return false
	}
	return true
}

// HandleStatus returns Immich connection status
// GET /api/immich/status
func (h *ImmichHandlers) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	if h.client == nil {
		json.NewEncoder(w).Encode(map[string]any{
			"configured": false,
			"message":    "Add immich section to config.yaml",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	info, err := h.client.ValidateConnection(ctx)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]any{
			"configured": true,
			"connected":  false,
			"error":      err.Error(),
			"url":        h.client.BaseURL,
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{
		"configured": true,
		"connected":  true,
		"url":        h.client.BaseURL,
		"version":    info.Version,
	})
}

// HandlePreview streams preview results via SSE
// GET /api/immich/preview?after=...&before=...
func (h *ImmichHandlers) HandlePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !h.requireImmich(w) {
		return
	}

	// Parse query parameters
	config := ImportConfig{
		UserID: h.config.DefaultUser,
	}
	if config.UserID == "" {
		config.UserID = "default"
	}

	if afterStr := r.URL.Query().Get("after"); afterStr != "" {
		t, err := time.Parse(time.RFC3339, afterStr)
		if err == nil {
			config.After = &t
		}
	}
	if beforeStr := r.URL.Query().Get("before"); beforeStr != "" {
		t, err := time.Parse(time.RFC3339, beforeStr)
		if err == nil {
			config.Before = &t
		}
	}

	// Set up SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()

	h.manager.Preview(ctx, config, func(progress PreviewProgress) {
		data, _ := json.Marshal(progress)
		eventType := "progress"
		if progress.Complete {
			eventType = "complete"
		}
		if progress.Error != "" {
			eventType = "error"
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
		flusher.Flush()
	})
}

// ImportRequest represents a request to start an import
type ImportRequest struct {
	After   string   `json:"after,omitempty"`
	Before  string   `json:"before,omitempty"`
	Cameras []string `json:"cameras,omitempty"`
	UserID  string   `json:"user_id,omitempty"`
}

// HandleImport starts a new import job
// POST /api/immich/import
func (h *ImmichHandlers) HandleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !h.requireImmich(w) {
		return
	}

	var req ImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	config := ImportConfig{
		Cameras: req.Cameras,
		UserID:  req.UserID,
	}
	if config.UserID == "" {
		config.UserID = h.config.DefaultUser
		if config.UserID == "" {
			config.UserID = "default"
		}
	}

	if req.After != "" {
		t, err := time.Parse(time.RFC3339, req.After)
		if err == nil {
			config.After = &t
		}
	}
	if req.Before != "" {
		t, err := time.Parse(time.RFC3339, req.Before)
		if err == nil {
			config.Before = &t
		}
	}

	jobID, err := h.manager.StartImport(config)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"job_id": jobID,
		"status": "running",
	})
}

// HandleJobs lists all import jobs
// GET /api/immich/jobs
func (h *ImmichHandlers) HandleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	jobs, err := h.db.ListImportJobs()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"jobs": jobs,
	})
}

// HandleJob returns status of a specific job
// GET /api/immich/jobs/{id}
func (h *ImmichHandlers) HandleJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract job ID from path
	jobID := r.URL.Path[len("/api/immich/jobs/"):]
	if jobID == "" {
		http.Error(w, `{"error":"job ID required"}`, http.StatusBadRequest)
		return
	}

	// Check for sub-paths like /resume or /cancel
	if len(jobID) > 36 {
		// Route to appropriate handler based on suffix
		return
	}

	progress, err := h.manager.GetJobProgress(jobID)
	if err == ErrJobNotFound {
		http.Error(w, `{"error":"job not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(progress)
}

// HandleJobResume resumes an interrupted job
// POST /api/immich/jobs/{id}/resume
func (h *ImmichHandlers) HandleJobResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !h.requireImmich(w) {
		return
	}

	// Extract job ID from path
	path := r.URL.Path
	// Path is /api/immich/jobs/{id}/resume
	jobID := path[len("/api/immich/jobs/") : len(path)-len("/resume")]

	err := h.manager.ResumeImport(jobID)
	if err == ErrJobNotFound {
		http.Error(w, `{"error":"job not found"}`, http.StatusNotFound)
		return
	}
	if err == ErrJobNotResumable {
		http.Error(w, `{"error":"job cannot be resumed"}`, http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "resumed",
	})
}

// HandleJobCancel cancels a running job
// POST /api/immich/jobs/{id}/cancel
func (h *ImmichHandlers) HandleJobCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract job ID from path
	path := r.URL.Path
	// Path is /api/immich/jobs/{id}/cancel
	jobID := path[len("/api/immich/jobs/") : len(path)-len("/cancel")]

	err := h.manager.CancelImport(jobID)
	if err == ErrJobNotFound {
		http.Error(w, `{"error":"job not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "cancelled",
	})
}

// HandleThumbnail proxies thumbnail requests to Immich
// GET /api/immich/assets/{id}/thumbnail
func (h *ImmichHandlers) HandleThumbnail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !h.requireImmich(w) {
		return
	}

	// Extract asset ID from path
	// Path is /api/immich/assets/{id}/thumbnail
	path := r.URL.Path
	prefix := "/api/immich/assets/"
	suffix := "/thumbnail"
	if !hasPrefix(path, prefix) || !hasSuffix(path, suffix) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	assetID := path[len(prefix) : len(path)-len(suffix)]

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	data, contentType, err := h.client.GetThumbnail(ctx, assetID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400") // Cache for 1 day
	w.Write(data)
}

// HandleSyncStatus returns the last sync time
// GET /api/immich/sync/status
func (h *ImmichHandlers) HandleSyncStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	lastSync, err := h.db.GetSyncState()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if lastSync == nil {
		json.NewEncoder(w).Encode(map[string]any{
			"last_sync": nil,
		})
	} else {
		json.NewEncoder(w).Encode(map[string]any{
			"last_sync": *lastSync,
		})
	}
}

// HandleSync triggers an incremental sync
// POST /api/immich/sync
func (h *ImmichHandlers) HandleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !h.requireImmich(w) {
		return
	}

	// Get last sync time
	lastSync, err := h.db.GetSyncState()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	config := ImportConfig{
		UserID: h.config.DefaultUser,
	}
	if config.UserID == "" {
		config.UserID = "default"
	}

	// If we have a last sync time, only fetch photos after that
	if lastSync != nil {
		t := time.Unix(*lastSync, 0)
		config.After = &t
	}

	jobID, err := h.manager.StartImport(config)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// Update sync state to now
	now := time.Now().Unix()
	if err := h.db.SetSyncState(now); err != nil {
		// Log but don't fail - the import is already started
		fmt.Printf("warning: failed to update sync state: %v\n", err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"job_id": jobID,
		"status": "running",
	})
}

// Helper functions
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
