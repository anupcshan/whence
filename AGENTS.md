# Whence

A self-hosted location history server.

## Tech Stack

- Go (no CGO)
- SQLite via modernc.org/sqlite
- Leaflet.js for frontend map

Minimize use of non-Go code. JavaScript should only be used where absolutely necessary for frontend functionality.

## Building

```bash
go mod tidy
CGO_ENABLED=0 go build -o whence .
```

## Code Quality

Always run before committing:

```bash
./run-lints.sh
```

Or manually:

```bash
go fmt ./...
go vet ./...
go tool golangci-lint run
```

Code must pass all checks with no errors or warnings. Ensure no trailing whitespace in any files.

## Running

```bash
./whence -addr :8080 -db ./data/whence.db -user default
```

## API Endpoints

### Location Ingestion
- `POST /owntracks` - OwnTracks compatible
- `GET /gpslogger` - GPSLogger compatible

### Location Queries
- `GET /api/paths` - GeoJSON paths for map
- `GET /api/bounds` - Bounding box for time range
- `GET /api/photos` - Clustered photos

### Import & Integrations
- `GET /import` - Import UI
- `/api/immich/*` - Immich photo sync

### Frontend
- `GET /` - Web frontend

See `main.go:50-93` for full route registration.
