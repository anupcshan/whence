package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ImmichHandlers holds handlers for Immich-related endpoints
type ImmichHandlers struct {
	config    *Config
	client    *ImmichClient
	manager   *BackfillManager
	db        *DB
	templates *Templates
}

// NewImmichHandlers creates handlers for Immich endpoints
func NewImmichHandlers(cfg *Config, db *DB, templates *Templates) *ImmichHandlers {
	h := &ImmichHandlers{
		config:    cfg,
		db:        db,
		templates: templates,
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
		w.Header().Set("Content-Type", "text/html")
		h.templates.Render(w, "partials/error.html", map[string]any{
			"Title":     "Immich not configured",
			"Message":   "Add immich section to config.yaml",
			"ShowRetry": false,
		})
		return false
	}
	return true
}

// ImmichStatusData holds data for the status template
type ImmichStatusData struct {
	Configured bool
	Connected  bool
	URL        string
	Version    string
	Error      string
}

// HandleStatus returns Immich connection status as HTML
// GET /api/immich/status
func (h *ImmichHandlers) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/html")

	if h.client == nil {
		h.templates.Render(w, "partials/immich-status.html", ImmichStatusData{
			Configured: false,
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	info, err := h.client.ValidateConnection(ctx)
	if err != nil {
		h.templates.Render(w, "partials/immich-status.html", ImmichStatusData{
			Configured: true,
			Connected:  false,
			URL:        h.client.BaseURL,
			Error:      err.Error(),
		})
		return
	}

	h.templates.Render(w, "partials/immich-status.html", ImmichStatusData{
		Configured: true,
		Connected:  true,
		URL:        h.client.BaseURL,
		Version:    info.Version,
	})
}

// HandlePreview streams preview results via SSE with HTML fragments
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

	afterStr := r.URL.Query().Get("after")
	beforeStr := r.URL.Query().Get("before")

	if afterStr != "" {
		t, err := time.Parse("2006-01-02", afterStr)
		if err == nil {
			config.After = &t
		}
	}
	if beforeStr != "" {
		t, err := time.Parse("2006-01-02", beforeStr)
		if err == nil {
			// Set to end of day
			t = t.Add(24*time.Hour - time.Second)
			config.Before = &t
		}
	}

	// Set up SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()

	h.manager.Preview(ctx, config, func(progress PreviewProgress) {
		if progress.Error != "" {
			// Send error as HTML fragment
			fmt.Fprintf(w, "event: error\ndata: <div class=\"status-box error\"><strong>Scan failed</strong><p>%s</p></div>\n\n", progress.Error)
			flusher.Flush()
			return
		}

		// Send progress update
		percent := int(progress.Percent)
		fmt.Fprintf(w, "event: progress\ndata: style=\"width: %d%%\">%d%%\n\n", percent, percent)
		flusher.Flush()

		// Send status update
		fmt.Fprintf(w, "event: status\ndata: Scanned %d photos, found %d with GPS\n\n",
			progress.Scanned, progress.PhotosWithGPS)
		flusher.Flush()

		if progress.Complete {
			// Build camera table data
			type CameraData struct {
				DeviceID string
				Count    int
				Earliest string
				Latest   string
			}

			cameras := make([]CameraData, len(progress.Cameras))
			for i, cam := range progress.Cameras {
				cameras[i] = CameraData{
					DeviceID: cam.DeviceID,
					Count:    cam.Count,
					Earliest: cam.Earliest.Format("Jan 2, 2006"),
					Latest:   cam.Latest.Format("Jan 2, 2006"),
				}
			}

			data := map[string]any{
				"Scanned": progress.Scanned,
				"WithGPS": progress.PhotosWithGPS,
				"Cameras": cameras,
				"After":   afterStr,
				"Before":  beforeStr,
			}

			// Render the camera table template to a string
			var html string
			err := renderToString(h.templates, "partials/camera-table.html", data, &html)
			if err != nil {
				fmt.Fprintf(w, "event: error\ndata: <div class=\"status-box error\">Template error: %s</div>\n\n", err.Error())
				flusher.Flush()
				return
			}

			// Send the complete event with the HTML
			fmt.Fprintf(w, "event: complete\ndata: %s\n\n", escapeSSEData(html))
			flusher.Flush()
		}
	})
}

// escapeSSEData escapes newlines for SSE data format
func escapeSSEData(s string) string {
	// SSE data can't contain newlines, so we need to send each line separately
	// or use a single line. For simplicity, replace newlines with a marker
	// and let HTMX parse it
	result := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			result = append(result, ' ')
		} else {
			result = append(result, s[i])
		}
	}
	return string(result)
}

// renderToString renders a template to a string
func renderToString(t *Templates, name string, data any, out *string) error {
	var buf stringWriter
	if err := t.Render(&buf, name, data); err != nil {
		return err
	}
	*out = buf.String()
	return nil
}

type stringWriter struct {
	data []byte
}

func (w *stringWriter) Write(p []byte) (n int, err error) {
	w.data = append(w.data, p...)
	return len(p), nil
}

func (w *stringWriter) String() string {
	return string(w.data)
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

	// Parse form data
	if err := r.ParseForm(); err != nil {
		w.Header().Set("Content-Type", "text/html")
		h.templates.Render(w, "partials/error.html", map[string]any{
			"Title":     "Invalid request",
			"Message":   err.Error(),
			"ShowRetry": true,
		})
		return
	}

	config := ImportConfig{
		Cameras: r.Form["cameras"],
		UserID:  h.config.DefaultUser,
	}
	if config.UserID == "" {
		config.UserID = "default"
	}

	if afterStr := r.FormValue("after"); afterStr != "" {
		t, err := time.Parse("2006-01-02", afterStr)
		if err == nil {
			config.After = &t
		}
	}
	if beforeStr := r.FormValue("before"); beforeStr != "" {
		t, err := time.Parse("2006-01-02", beforeStr)
		if err == nil {
			t = t.Add(24*time.Hour - time.Second)
			config.Before = &t
		}
	}

	jobID, err := h.manager.StartImport(config)
	if err != nil {
		w.Header().Set("Content-Type", "text/html")
		h.templates.Render(w, "partials/error.html", map[string]any{
			"Title":     "Failed to start import",
			"Message":   err.Error(),
			"ShowRetry": true,
		})
		return
	}

	// Return the progress view
	w.Header().Set("Content-Type", "text/html")
	h.templates.Render(w, "partials/import-progress.html", map[string]any{
		"JobID":    jobID,
		"Percent":  0,
		"Imported": 0,
		"Skipped":  0,
		"Errors":   0,
	})
}

// JobListData holds data for the job list template
type JobListData struct {
	ID        string
	Status    string
	StartedAt string
	Imported  int
	Skipped   int
}

// HandleJobs lists all import jobs as HTML
// GET /api/immich/jobs
func (h *ImmichHandlers) HandleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	jobs, err := h.db.ListImportJobs()
	if err != nil {
		w.Header().Set("Content-Type", "text/html")
		h.templates.Render(w, "partials/error.html", map[string]any{
			"Title":     "Failed to load jobs",
			"Message":   err.Error(),
			"ShowRetry": false,
		})
		return
	}

	// Convert to template data, limit to 5 recent jobs
	jobData := make([]JobListData, 0, 5)
	for i, job := range jobs {
		if i >= 5 {
			break
		}
		jobData = append(jobData, JobListData{
			ID:        job.ID,
			Status:    job.Status,
			StartedAt: time.Unix(job.StartedAt, 0).Format("Jan 2, 2006 3:04 PM"),
			Imported:  job.Imported,
			Skipped:   job.Skipped,
		})
	}

	w.Header().Set("Content-Type", "text/html")
	h.templates.Render(w, "partials/job-list.html", map[string]any{
		"Jobs": jobData,
	})
}

// HandleJob returns status of a specific job as HTML
// GET /api/immich/jobs/{id}
func (h *ImmichHandlers) HandleJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract job ID from path
	jobID := r.URL.Path[len("/api/immich/jobs/"):]
	if jobID == "" {
		w.Header().Set("Content-Type", "text/html")
		h.templates.Render(w, "partials/error.html", map[string]any{
			"Title":     "Job ID required",
			"Message":   "No job ID specified",
			"ShowRetry": true,
		})
		return
	}

	// Check for sub-paths like /resume or /cancel
	if len(jobID) > 36 {
		return
	}

	progress, err := h.manager.GetJobProgress(jobID)
	if err == ErrJobNotFound {
		w.Header().Set("Content-Type", "text/html")
		h.templates.Render(w, "partials/error.html", map[string]any{
			"Title":     "Job not found",
			"Message":   "The specified job does not exist",
			"ShowRetry": true,
		})
		return
	}
	if err != nil {
		w.Header().Set("Content-Type", "text/html")
		h.templates.Render(w, "partials/error.html", map[string]any{
			"Title":     "Error loading job",
			"Message":   err.Error(),
			"ShowRetry": true,
		})
		return
	}

	w.Header().Set("Content-Type", "text/html")

	// Return different templates based on status
	switch progress.Status {
	case "completed":
		h.templates.Render(w, "partials/import-complete.html", map[string]any{
			"Imported": progress.Imported,
			"Skipped":  progress.Skipped,
			"Errors":   progress.Errors,
		})
	case "cancelled":
		h.templates.Render(w, "partials/import-cancelled.html", map[string]any{
			"Imported": progress.Imported,
			"Skipped":  progress.Skipped,
		})
	case "failed":
		h.templates.Render(w, "partials/error.html", map[string]any{
			"Title":     "Import failed",
			"Message":   progress.Error,
			"ShowRetry": true,
		})
	default:
		// Still running - return progress view that will poll again
		h.templates.Render(w, "partials/import-progress.html", map[string]any{
			"JobID":    progress.JobID,
			"Percent":  int(progress.Percent),
			"Imported": progress.Imported,
			"Skipped":  progress.Skipped,
			"Errors":   progress.Errors,
		})
	}
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
	jobID := path[len("/api/immich/jobs/") : len(path)-len("/resume")]

	err := h.manager.ResumeImport(jobID)
	if err == ErrJobNotFound {
		w.Header().Set("Content-Type", "text/html")
		h.templates.Render(w, "partials/error.html", map[string]any{
			"Title":     "Job not found",
			"Message":   "The specified job does not exist",
			"ShowRetry": true,
		})
		return
	}
	if err == ErrJobNotResumable {
		w.Header().Set("Content-Type", "text/html")
		h.templates.Render(w, "partials/error.html", map[string]any{
			"Title":     "Cannot resume job",
			"Message":   "This job cannot be resumed",
			"ShowRetry": true,
		})
		return
	}
	if err != nil {
		w.Header().Set("Content-Type", "text/html")
		h.templates.Render(w, "partials/error.html", map[string]any{
			"Title":     "Resume failed",
			"Message":   err.Error(),
			"ShowRetry": true,
		})
		return
	}

	// Return progress view
	w.Header().Set("Content-Type", "text/html")
	h.templates.Render(w, "partials/import-progress.html", map[string]any{
		"JobID":    jobID,
		"Percent":  0,
		"Imported": 0,
		"Skipped":  0,
		"Errors":   0,
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
	jobID := path[len("/api/immich/jobs/") : len(path)-len("/cancel")]

	err := h.manager.CancelImport(jobID)
	if err == ErrJobNotFound {
		w.Header().Set("Content-Type", "text/html")
		h.templates.Render(w, "partials/error.html", map[string]any{
			"Title":     "Job not found",
			"Message":   "The specified job does not exist",
			"ShowRetry": true,
		})
		return
	}
	if err != nil {
		w.Header().Set("Content-Type", "text/html")
		h.templates.Render(w, "partials/error.html", map[string]any{
			"Title":     "Cancel failed",
			"Message":   err.Error(),
			"ShowRetry": true,
		})
		return
	}

	// Return cancelled view
	progress, _ := h.manager.GetJobProgress(jobID)
	imported := 0
	skipped := 0
	if progress != nil {
		imported = progress.Imported
		skipped = progress.Skipped
	}

	w.Header().Set("Content-Type", "text/html")
	h.templates.Render(w, "partials/import-cancelled.html", map[string]any{
		"Imported": imported,
		"Skipped":  skipped,
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
	w.Header().Set("Cache-Control", "public, max-age=86400")
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	if lastSync == nil {
		fmt.Fprint(w, "<p>Never synced</p>")
	} else {
		fmt.Fprintf(w, "<p>Last sync: %s</p>", time.Unix(*lastSync, 0).Format("Jan 2, 2006 3:04 PM"))
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
		w.Header().Set("Content-Type", "text/html")
		h.templates.Render(w, "partials/error.html", map[string]any{
			"Title":     "Sync failed",
			"Message":   err.Error(),
			"ShowRetry": true,
		})
		return
	}

	config := ImportConfig{
		UserID: h.config.DefaultUser,
	}
	if config.UserID == "" {
		config.UserID = "default"
	}

	if lastSync != nil {
		t := time.Unix(*lastSync, 0)
		config.After = &t
	}

	jobID, err := h.manager.StartImport(config)
	if err != nil {
		w.Header().Set("Content-Type", "text/html")
		h.templates.Render(w, "partials/error.html", map[string]any{
			"Title":     "Sync failed",
			"Message":   err.Error(),
			"ShowRetry": true,
		})
		return
	}

	// Update sync state to now
	now := time.Now().Unix()
	if err := h.db.SetSyncState(now); err != nil {
		// Log but don't fail
		fmt.Printf("warning: failed to update sync state: %v\n", err)
	}

	// Return progress view
	w.Header().Set("Content-Type", "text/html")
	h.templates.Render(w, "partials/import-progress.html", map[string]any{
		"JobID":    jobID,
		"Percent":  0,
		"Imported": 0,
		"Skipped":  0,
		"Errors":   0,
	})
}

// HandleImportPage serves the import page
// GET /import
func (h *ImmichHandlers) HandleImportPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.templates.Render(w, "import.html", nil)
}

// Helper functions
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// Ensure url is imported (used in escaping)
var _ = url.QueryEscape
