package main

import (
	_ "embed"
	"flag"
	"log"
	"net/http"
)

//go:embed index.html
var indexHTML []byte

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dbPath := flag.String("db", "./data/whence.db", "database path")
	defaultUser := flag.String("user", "default", "default user ID")
	flag.Parse()

	db, err := OpenDB(*dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	server := &Server{
		db:            db,
		defaultUserID: *defaultUser,
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})

	http.HandleFunc("/owntracks", server.handleOwnTracks)
	http.HandleFunc("/gpslogger", server.handleGPSLogger)
	http.HandleFunc("/api/locations", server.handleAPILocations)

	log.Printf("starting server on %s", *addr)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
