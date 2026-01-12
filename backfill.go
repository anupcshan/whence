package main

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ImportConfig holds the configuration for an import job
type ImportConfig struct {
	After   *time.Time `json:"after,omitempty"`
	Before  *time.Time `json:"before,omitempty"`
	Cameras []string   `json:"cameras,omitempty"` // Empty means all cameras
	UserID  string     `json:"user_id"`
}

// CameraPreview holds aggregated stats for a camera during preview
type CameraPreview struct {
	DeviceID string    `json:"device_id"`
	Count    int       `json:"count"`
	Earliest time.Time `json:"earliest"`
	Latest   time.Time `json:"latest"`
}

// PreviewProgress represents progress during preview scanning
type PreviewProgress struct {
	Scanned        int             `json:"scanned"`
	TotalEstimated int             `json:"total_estimated"`
	Percent        float64         `json:"percent"`
	PhotosWithGPS  int             `json:"photos_with_gps"`
	Cameras        []CameraPreview `json:"cameras"`
	Complete       bool            `json:"complete"`
	Error          string          `json:"error,omitempty"`
}

// ImportProgress represents progress during import
type ImportProgress struct {
	JobID     string  `json:"job_id"`
	Status    string  `json:"status"`
	Total     int     `json:"total"`
	Processed int     `json:"processed"`
	Imported  int     `json:"imported"`
	Skipped   int     `json:"skipped"`
	Errors    int     `json:"errors"`
	Percent   float64 `json:"percent"`
	Error     string  `json:"error,omitempty"`
}

// BackfillManager manages import jobs
type BackfillManager struct {
	db      *DB
	client  *ImmichClient
	jobs    map[string]context.CancelFunc
	streams map[string][]chan ImportProgress // SSE subscribers per job
	mu      sync.RWMutex
}

// NewBackfillManager creates a new backfill manager
func NewBackfillManager(db *DB, client *ImmichClient) *BackfillManager {
	bm := &BackfillManager{
		db:      db,
		client:  client,
		jobs:    make(map[string]context.CancelFunc),
		streams: make(map[string][]chan ImportProgress),
	}

	// Mark any previously running jobs as interrupted
	bm.markInterruptedJobs()

	return bm
}

// Subscribe returns a channel that receives progress updates for a job.
// The returned function should be called to unsubscribe when done.
func (bm *BackfillManager) Subscribe(jobID string) (<-chan ImportProgress, func()) {
	ch := make(chan ImportProgress, 10)

	bm.mu.Lock()
	bm.streams[jobID] = append(bm.streams[jobID], ch)
	bm.mu.Unlock()

	unsubscribe := func() {
		bm.mu.Lock()
		defer bm.mu.Unlock()

		subs := bm.streams[jobID]
		for i, sub := range subs {
			if sub == ch {
				// Remove from slice and close
				bm.streams[jobID] = append(subs[:i], subs[i+1:]...)
				close(ch)
				return
			}
		}
		// Channel not found - already closed by closeStreams()
	}

	return ch, unsubscribe
}

// broadcast sends progress to all subscribers (non-blocking)
func (bm *BackfillManager) broadcast(jobID string, progress ImportProgress) {
	bm.mu.RLock()
	subs := bm.streams[jobID]
	bm.mu.RUnlock()

	for _, ch := range subs {
		select {
		case ch <- progress:
		default:
			// Drop if channel is full (slow consumer)
		}
	}
}

// closeStreams closes all subscriber channels for a job
func (bm *BackfillManager) closeStreams(jobID string) {
	bm.mu.Lock()
	subs := bm.streams[jobID]
	delete(bm.streams, jobID)
	bm.mu.Unlock()

	for _, ch := range subs {
		close(ch)
	}
}

// markInterruptedJobs marks any running jobs from previous sessions as interrupted
func (bm *BackfillManager) markInterruptedJobs() {
	jobs, err := bm.db.ListImportJobs()
	if err != nil {
		log.Printf("failed to list import jobs: %v", err)
		return
	}

	for _, job := range jobs {
		if job.Status == "running" {
			job.Status = "interrupted"
			errMsg := "server restarted"
			job.LastError = &errMsg
			if err := bm.db.UpdateImportJob(job); err != nil {
				log.Printf("failed to mark job %s as interrupted: %v", job.ID, err)
			}
		}
	}
}

// PreviewCallback is called with progress updates during preview
type PreviewCallback func(progress PreviewProgress)

// Preview scans Immich for photos and aggregates by camera
// Calls the callback with progress updates
func (bm *BackfillManager) Preview(ctx context.Context, config ImportConfig, callback PreviewCallback) {
	cameras := make(map[string]*CameraPreview)
	scanned := 0
	photosWithGPS := 0
	var totalEstimate int

	opts := SearchOptions{
		After:    config.After,
		Before:   config.Before,
		PageSize: 200,
		WithExif: true,
	}

	for page := 1; ; page++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		opts.Page = page
		assets, hasMore, err := bm.client.SearchAssets(ctx, opts)
		if err != nil {
			callback(PreviewProgress{Error: err.Error()})
			return
		}

		for _, asset := range assets {
			scanned++
			if asset.HasGPS() {
				photosWithGPS++
				deviceID := asset.DeviceIDFromExif()

				cam, exists := cameras[deviceID]
				if !exists {
					cam = &CameraPreview{
						DeviceID: deviceID,
						Earliest: asset.GetTimestamp(),
						Latest:   asset.GetTimestamp(),
					}
					cameras[deviceID] = cam
				}
				cam.Count++

				ts := asset.GetTimestamp()
				if ts.Before(cam.Earliest) {
					cam.Earliest = ts
				}
				if ts.After(cam.Latest) {
					cam.Latest = ts
				}
			}
		}

		// Estimate total based on current progress
		if hasMore && len(assets) > 0 {
			// Rough estimate: assume similar density
			totalEstimate = scanned * 2
			if totalEstimate < scanned+200 {
				totalEstimate = scanned + 200
			}
		} else {
			totalEstimate = scanned
		}

		// Calculate percent
		var percent float64
		if totalEstimate > 0 {
			percent = float64(scanned) / float64(totalEstimate) * 100
		}

		// Send progress update
		callback(PreviewProgress{
			Scanned:        scanned,
			TotalEstimated: totalEstimate,
			Percent:        percent,
			PhotosWithGPS:  photosWithGPS,
			Cameras:        camerasToSlice(cameras),
			Complete:       !hasMore,
		})

		if !hasMore {
			break
		}
	}
}

// camerasToSlice converts camera map to sorted slice
func camerasToSlice(cameras map[string]*CameraPreview) []CameraPreview {
	result := make([]CameraPreview, 0, len(cameras))
	for _, cam := range cameras {
		result = append(result, *cam)
	}
	return result
}

// StartImport begins a new import job
func (bm *BackfillManager) StartImport(config ImportConfig) (string, error) {
	jobID := uuid.New().String()

	configJSON, err := json.Marshal(config)
	if err != nil {
		return "", err
	}

	job := ImportJob{
		ID:         jobID,
		Status:     "running",
		StartedAt:  time.Now().Unix(),
		Processed:  0,
		Imported:   0,
		Skipped:    0,
		Errors:     0,
		LastPage:   0,
		ConfigJSON: string(configJSON),
	}

	if err := bm.db.CreateImportJob(job); err != nil {
		return "", err
	}

	ctx, cancel := context.WithCancel(context.Background())
	bm.mu.Lock()
	bm.jobs[jobID] = cancel
	bm.mu.Unlock()

	go bm.runImport(ctx, jobID, config, 1)

	return jobID, nil
}

// ResumeImport resumes an interrupted import job
func (bm *BackfillManager) ResumeImport(jobID string) error {
	job, err := bm.db.GetImportJob(jobID)
	if err != nil {
		return err
	}
	if job == nil {
		return ErrJobNotFound
	}
	if job.Status != "interrupted" && job.Status != "failed" {
		return ErrJobNotResumable
	}

	var config ImportConfig
	if err := json.Unmarshal([]byte(job.ConfigJSON), &config); err != nil {
		return err
	}

	job.Status = "running"
	job.LastError = nil
	if err := bm.db.UpdateImportJob(*job); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	bm.mu.Lock()
	bm.jobs[jobID] = cancel
	bm.mu.Unlock()

	// Resume from last_page + 1
	go bm.runImport(ctx, jobID, config, job.LastPage+1)

	return nil
}

// CancelImport cancels a running import job
func (bm *BackfillManager) CancelImport(jobID string) error {
	bm.mu.Lock()
	cancel, exists := bm.jobs[jobID]
	if exists {
		cancel()
		delete(bm.jobs, jobID)
	}
	bm.mu.Unlock()

	if !exists {
		return ErrJobNotFound
	}

	job, err := bm.db.GetImportJob(jobID)
	if err != nil {
		return err
	}
	if job != nil {
		job.Status = "cancelled"
		now := time.Now().Unix()
		job.CompletedAt = &now
		return bm.db.UpdateImportJob(*job)
	}
	return nil
}

// runImport executes the import job
func (bm *BackfillManager) runImport(ctx context.Context, jobID string, config ImportConfig, startPage int) {
	defer func() {
		bm.mu.Lock()
		delete(bm.jobs, jobID)
		bm.mu.Unlock()
		bm.closeStreams(jobID)
	}()

	job, err := bm.db.GetImportJob(jobID)
	if err != nil || job == nil {
		log.Printf("import job %s: failed to get job: %v", jobID, err)
		return
	}

	// Helper to build and broadcast current progress
	broadcastProgress := func() {
		bm.broadcast(jobID, ImportProgress{
			JobID:    jobID,
			Status:   job.Status,
			Imported: job.Imported,
			Skipped:  job.Skipped,
			Errors:   job.Errors,
		})
	}

	// Build camera filter set
	allowedCameras := make(map[string]bool)
	for _, cam := range config.Cameras {
		allowedCameras[cam] = true
	}
	filterCameras := len(config.Cameras) > 0

	opts := SearchOptions{
		After:    config.After,
		Before:   config.Before,
		PageSize: 200,
		WithExif: true,
	}

	for page := startPage; ; page++ {
		select {
		case <-ctx.Done():
			job.Status = "cancelled"
			now := time.Now().Unix()
			job.CompletedAt = &now
			bm.db.UpdateImportJob(*job)
			broadcastProgress()
			return
		default:
		}

		opts.Page = page
		assets, hasMore, err := bm.client.SearchAssets(ctx, opts)
		if err != nil {
			job.Status = "failed"
			errMsg := err.Error()
			job.LastError = &errMsg
			now := time.Now().Unix()
			job.CompletedAt = &now
			bm.db.UpdateImportJob(*job)
			bm.broadcast(jobID, ImportProgress{
				JobID:  jobID,
				Status: job.Status,
				Error:  errMsg,
			})
			log.Printf("import job %s: search failed on page %d: %v", jobID, page, err)
			return
		}

		for _, asset := range assets {
			job.Processed++

			if !asset.HasGPS() {
				continue
			}

			deviceID := asset.DeviceIDFromExif()

			// Filter by camera if specified
			if filterCameras && !allowedCameras[deviceID] {
				continue
			}

			ts := asset.GetTimestamp()
			loc := Location{
				Timestamp: ts.Unix(),
				UserID:    config.UserID,
				DeviceID:  deviceID,
				Lat:       *asset.ExifInfo.Latitude,
				Lon:       *asset.ExifInfo.Longitude,
			}

			source := LocationSource{
				Timestamp:  ts.Unix(),
				DeviceID:   deviceID,
				SourceType: "immich",
				SourceID:   asset.ID,
				Metadata:   buildSourceMetadata(asset, bm.client.BaseURL),
			}

			inserted, err := bm.db.InsertLocationWithSource(loc, source)
			if err != nil {
				job.Errors++
				log.Printf("import job %s: failed to insert location: %v", jobID, err)
				continue
			}

			if inserted {
				job.Imported++
			} else {
				job.Skipped++
			}
		}

		// Checkpoint: save progress after each page
		job.LastPage = page
		if err := bm.db.UpdateImportJob(*job); err != nil {
			log.Printf("import job %s: failed to checkpoint: %v", jobID, err)
		}

		// Broadcast progress to SSE subscribers
		broadcastProgress()

		if !hasMore {
			break
		}
	}

	// Mark as completed
	job.Status = "completed"
	now := time.Now().Unix()
	job.CompletedAt = &now
	if err := bm.db.UpdateImportJob(*job); err != nil {
		log.Printf("import job %s: failed to mark complete: %v", jobID, err)
	}

	// Final broadcast
	broadcastProgress()

	// Rebuild paths after import
	if job.Imported > 0 {
		log.Printf("import job %s: rebuilding paths...", jobID)
		if err := bm.db.RebuildAllPaths(); err != nil {
			log.Printf("import job %s: failed to rebuild paths: %v", jobID, err)
		} else {
			log.Printf("import job %s: paths rebuilt successfully", jobID)
		}
	}

	log.Printf("import job %s: completed - imported=%d, skipped=%d, errors=%d",
		jobID, job.Imported, job.Skipped, job.Errors)
}

// buildSourceMetadata creates JSON metadata for a location source
func buildSourceMetadata(asset ImmichAsset, baseURL string) string {
	meta := map[string]string{
		"web_url":  baseURL + "/photos/" + asset.ID,
		"filename": asset.OriginalFilename(),
	}
	if asset.ExifInfo != nil {
		if asset.ExifInfo.Make != nil {
			meta["make"] = *asset.ExifInfo.Make
		}
		if asset.ExifInfo.Model != nil {
			meta["model"] = *asset.ExifInfo.Model
		}
	}
	data, _ := json.Marshal(meta)
	return string(data)
}

// GetJobProgress returns current progress for a job
func (bm *BackfillManager) GetJobProgress(jobID string) (*ImportProgress, error) {
	job, err := bm.db.GetImportJob(jobID)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, ErrJobNotFound
	}

	var percent float64
	total := job.Processed
	if job.Total != nil && *job.Total > 0 {
		total = *job.Total
		percent = float64(job.Processed) / float64(total) * 100
	}

	progress := &ImportProgress{
		JobID:     job.ID,
		Status:    job.Status,
		Total:     total,
		Processed: job.Processed,
		Imported:  job.Imported,
		Skipped:   job.Skipped,
		Errors:    job.Errors,
		Percent:   percent,
	}
	if job.LastError != nil {
		progress.Error = *job.LastError
	}

	return progress, nil
}

// Custom errors
type backfillError string

func (e backfillError) Error() string { return string(e) }

const (
	ErrJobNotFound     = backfillError("job not found")
	ErrJobNotResumable = backfillError("job cannot be resumed")
)
