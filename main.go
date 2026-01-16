package main

import (
	_ "embed"
	"flag"
	"log"
	"net/http"
	"strings"
)

//go:embed index.html
var indexHTML []byte

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dbPath := flag.String("db", "./data/whence.db", "database path")
	defaultUser := flag.String("user", "default", "default user ID")
	configPath := flag.String("config", "", "config file path (default: ~/.config/whence/config.yaml)")
	flag.Parse()

	// Load config
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Override default user from config if set
	if cfg != nil && cfg.DefaultUser != "" {
		*defaultUser = cfg.DefaultUser
	}

	db, err := OpenDB(*dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Initialize templates
	templates := NewTemplates()

	// Initialize geocoding service
	geocoder := NewGeocodingService(db)

	server := &Server{
		db:            db,
		defaultUserID: *defaultUser,
		geocoder:      geocoder,
	}

	// Initialize Immich handlers
	immichHandlers := NewImmichHandlers(cfg, db, templates)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})

	// Import page (HTMX-powered)
	http.HandleFunc("/import", immichHandlers.HandleImportPage)

	// Existing endpoints
	http.HandleFunc("/owntracks", server.handleOwnTracks)
	http.HandleFunc("/gpslogger", server.handleGPSLogger)
	http.HandleFunc("/api/paths", server.handleAPIPaths)
	http.HandleFunc("/api/paths/rebuild", server.handleAPIPathsRebuild)
	http.HandleFunc("/api/bounds", server.handleAPIBounds)
	http.HandleFunc("/api/latest", server.handleAPILatest)
	http.HandleFunc("/api/location/source", server.handleAPILocationSource)
	http.HandleFunc("/api/photos", server.handleAPIPhotos)
	http.HandleFunc("/api/timeline", server.handleAPITimeline)
	http.HandleFunc("/api/import/timeline", server.handleImportTimeline)

	// Immich endpoints
	http.HandleFunc("/api/immich/status", immichHandlers.HandleStatus)
	http.HandleFunc("/api/immich/preview/start", immichHandlers.HandlePreviewStart)
	http.HandleFunc("/api/immich/preview", immichHandlers.HandlePreview)
	http.HandleFunc("/api/immich/import", immichHandlers.HandleImport)
	http.HandleFunc("/api/immich/jobs", immichHandlers.HandleJobs)
	http.HandleFunc("/api/immich/jobs/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Route based on path suffix
		if strings.HasSuffix(path, "/resume") {
			immichHandlers.HandleJobResume(w, r)
		} else if strings.HasSuffix(path, "/cancel") {
			immichHandlers.HandleJobCancel(w, r)
		} else if strings.HasSuffix(path, "/stream") {
			immichHandlers.HandleJobStream(w, r)
		} else {
			immichHandlers.HandleJob(w, r)
		}
	})
	http.HandleFunc("/api/immich/assets/", immichHandlers.HandleThumbnail)
	http.HandleFunc("/api/immich/sync", immichHandlers.HandleSync)
	http.HandleFunc("/api/immich/sync/status", immichHandlers.HandleSyncStatus)

	if cfg != nil && cfg.ImmichConfigured() {
		log.Printf("Immich configured: %s", cfg.Immich.URL)
	} else {
		log.Printf("Immich not configured (add immich section to config file)")
	}

	log.Printf("starting server on %s", *addr)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
