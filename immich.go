package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ImmichClient handles communication with the Immich API
type ImmichClient struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// NewImmichClient creates a new Immich API client
func NewImmichClient(baseURL, apiKey string) *ImmichClient {
	// Normalize URL - remove trailing slash
	baseURL = strings.TrimRight(baseURL, "/")

	return &ImmichClient{
		BaseURL: baseURL,
		APIKey:  apiKey,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ImmichAsset represents an asset returned from Immich API
type ImmichAsset struct {
	ID            string          `json:"id"`
	DeviceID      string          `json:"deviceId"`
	FileCreatedAt time.Time       `json:"fileCreatedAt"`
	ExifInfo      *ImmichExifInfo `json:"exifInfo,omitempty"`
	OriginalPath  string          `json:"originalPath,omitempty"`
}

// ImmichExifInfo contains EXIF metadata from an asset
type ImmichExifInfo struct {
	Latitude         *float64   `json:"latitude,omitempty"`
	Longitude        *float64   `json:"longitude,omitempty"`
	DateTimeOriginal *time.Time `json:"dateTimeOriginal,omitempty"`
	Make             *string    `json:"make,omitempty"`
	Model            *string    `json:"model,omitempty"`
}

// HasGPS returns true if the asset has GPS coordinates
func (a *ImmichAsset) HasGPS() bool {
	return a.ExifInfo != nil &&
		a.ExifInfo.Latitude != nil &&
		a.ExifInfo.Longitude != nil
}

// GetTimestamp returns the best timestamp for the asset
func (a *ImmichAsset) GetTimestamp() time.Time {
	if a.ExifInfo != nil && a.ExifInfo.DateTimeOriginal != nil {
		return *a.ExifInfo.DateTimeOriginal
	}
	return a.FileCreatedAt
}

// DeviceIDFromExif generates a device ID from EXIF make/model
func (a *ImmichAsset) DeviceIDFromExif() string {
	if a.ExifInfo == nil {
		return "immich-unknown"
	}

	var make, model string
	if a.ExifInfo.Make != nil {
		make = strings.TrimSpace(*a.ExifInfo.Make)
	}
	if a.ExifInfo.Model != nil {
		model = strings.TrimSpace(*a.ExifInfo.Model)
	}

	if make == "" && model == "" {
		return "immich-unknown"
	}
	if make == "" {
		return model
	}
	if model == "" {
		return make
	}

	// Avoid duplication like "Apple Apple iPhone 15 Pro"
	if strings.HasPrefix(strings.ToLower(model), strings.ToLower(make)) {
		return model
	}
	return make + " " + model
}

// OriginalFilename returns just the filename from the path
func (a *ImmichAsset) OriginalFilename() string {
	if a.OriginalPath == "" {
		return ""
	}
	parts := strings.Split(a.OriginalPath, "/")
	return parts[len(parts)-1]
}

// SearchOptions defines parameters for searching assets
type SearchOptions struct {
	After    *time.Time
	Before   *time.Time
	Page     int
	PageSize int
	WithExif bool
}

// SearchResponse represents the response from Immich search API
type SearchResponse struct {
	Assets struct {
		Items    []ImmichAsset `json:"items"`
		NextPage *int          `json:"nextPage,omitempty"`
	} `json:"assets"`
}

// MetadataSearchResponse represents response from POST /search/metadata
type MetadataSearchResponse struct {
	Assets struct {
		Items    []ImmichAsset `json:"items"`
		NextPage *string       `json:"nextPage,omitempty"`
	} `json:"assets"`
}

// ServerInfo contains Immich server information
type ServerInfo struct {
	Version string `json:"version"`
}

// UserInfo contains Immich user information
type UserInfo struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// ValidateConnection tests the connection to Immich and verifies permissions
// Only requires asset.read permission - validates by doing a minimal search
func (c *ImmichClient) ValidateConnection(ctx context.Context) (*ServerInfo, error) {
	// Verify we can search assets (tests asset.read permission)
	// This is the only permission we actually need
	_, _, err := c.SearchAssets(ctx, SearchOptions{PageSize: 1})
	if err != nil {
		return nil, fmt.Errorf("failed to connect or API key lacks asset.read permission: %w", err)
	}

	// Return minimal server info - we don't need server.about permission
	return &ServerInfo{Version: "connected"}, nil
}

// SearchAssets searches for assets matching the given options
// Returns assets, hasMore flag, and any error
func (c *ImmichClient) SearchAssets(ctx context.Context, opts SearchOptions) ([]ImmichAsset, bool, error) {
	if opts.PageSize == 0 {
		opts.PageSize = 200
	}
	if opts.Page == 0 {
		opts.Page = 1
	}

	// Build search request body
	body := map[string]any{
		"page":     opts.Page,
		"size":     opts.PageSize,
		"withExif": true,
		"order":    "asc", // Oldest first for consistent pagination
	}

	if opts.After != nil {
		body["takenAfter"] = opts.After.Format(time.RFC3339)
	}
	if opts.Before != nil {
		body["takenBefore"] = opts.Before.Format(time.RFC3339)
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, false, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/api/search/metadata", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, false, fmt.Errorf("search failed with status %d: %s", resp.StatusCode, string(body))
	}

	var result MetadataSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, false, fmt.Errorf("failed to parse search response: %w", err)
	}

	hasMore := result.Assets.NextPage != nil
	return result.Assets.Items, hasMore, nil
}

// GetThumbnail fetches a thumbnail for an asset
// size can be "thumbnail" (default), "preview", or "fullsize"
func (c *ImmichClient) GetThumbnail(ctx context.Context, assetID, size string) ([]byte, string, error) {
	url := c.BaseURL + "/api/assets/" + assetID + "/thumbnail"
	if size != "" && size != "thumbnail" {
		url += "?size=" + size
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("x-api-key", c.APIKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("thumbnail request failed with status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}

	return data, contentType, nil
}

// WebURL returns the URL to view an asset in the Immich web UI
func (c *ImmichClient) WebURL(assetID string) string {
	return c.BaseURL + "/photos/" + assetID
}
