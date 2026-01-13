package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Timeline represents the root structure of Android Timeline JSON export
type Timeline struct {
	RawSignals []RawSignal `json:"rawSignals"`
}

// RawSignal represents a single signal entry in the timeline
type RawSignal struct {
	Position *TimelinePosition `json:"position,omitempty"`
}

// TimelinePosition represents a position reading from Google Timeline
type TimelinePosition struct {
	LatLng    string  `json:"LatLng"`               // "37.422°, -122.084°"
	AccuracyM float64 `json:"accuracyMeters"`       // meters
	AltitudeM float64 `json:"altitudeMeters"`       // meters
	Source    string  `json:"source"`               // GPS, WIFI, CELL, UNKNOWN
	Timestamp string  `json:"timestamp"`            // ISO 8601
	SpeedMPS  float64 `json:"speedMetersPerSecond"` // m/s
}

// ParseLatLng extracts latitude and longitude from a string like "37.422°, -122.084°"
func ParseLatLng(s string) (lat, lon float64, err error) {
	// Remove degree symbols and split by comma
	s = strings.ReplaceAll(s, "°", "")
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid LatLng format: %s", s)
	}

	lat, err = strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid latitude: %w", err)
	}

	lon, err = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid longitude: %w", err)
	}

	return lat, lon, nil
}

// TimelineImportStats tracks import progress
type TimelineImportStats struct {
	Total    int `json:"total"`
	Parsed   int `json:"parsed"`
	Inserted int `json:"inserted"`
	Skipped  int `json:"skipped"`
	Errors   int `json:"errors"`
}

// TimelineImportProgress is sent via SSE during import
type TimelineImportProgress struct {
	Stats    TimelineImportStats `json:"stats"`
	Message  string              `json:"message,omitempty"`
	Error    string              `json:"error,omitempty"`
	Complete bool                `json:"complete"`
}

// ParseTimeline reads an Android Timeline JSON file and extracts locations
func ParseTimeline(r io.Reader) (*Timeline, error) {
	var timeline Timeline
	if err := json.NewDecoder(r).Decode(&timeline); err != nil {
		return nil, fmt.Errorf("failed to parse timeline JSON: %w", err)
	}
	return &timeline, nil
}

// ExtractLocations converts Timeline positions to Location structs
func ExtractLocations(timeline *Timeline, userID, deviceID string) ([]Location, []error) {
	var locations []Location
	var errors []error

	for i, signal := range timeline.RawSignals {
		if signal.Position == nil {
			continue
		}

		pos := signal.Position

		// Parse coordinates
		lat, lon, err := ParseLatLng(pos.LatLng)
		if err != nil {
			errors = append(errors, fmt.Errorf("signal %d: %w", i, err))
			continue
		}

		// Parse timestamp
		t, err := time.Parse(time.RFC3339, pos.Timestamp)
		if err != nil {
			// Try alternate format without timezone
			t, err = time.Parse("2006-01-02T15:04:05.000-07:00", pos.Timestamp)
			if err != nil {
				errors = append(errors, fmt.Errorf("signal %d: invalid timestamp %q: %w", i, pos.Timestamp, err))
				continue
			}
		}

		loc := Location{
			Timestamp: t.Unix(),
			UserID:    userID,
			DeviceID:  deviceID,
			Lat:       lat,
			Lon:       lon,
		}

		// Set extended fields if present
		if pos.AltitudeM != 0 {
			alt := pos.AltitudeM
			loc.AltitudeM = &alt
		}
		if pos.AccuracyM != 0 {
			acc := pos.AccuracyM
			loc.AccuracyM = &acc
		}
		if pos.SpeedMPS != 0 {
			// Convert m/s to km/h
			speed := pos.SpeedMPS * 3.6
			loc.SpeedKmh = &speed
		}
		if pos.Source != "" {
			src := pos.Source
			loc.Source = &src
		}

		locations = append(locations, loc)
	}

	return locations, errors
}
