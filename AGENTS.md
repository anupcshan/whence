# Whence

A self-hosted location history server.

## Tech Stack

- Go (no CGO)
- SQLite via modernc.org/sqlite
- Leaflet.js for frontend map

## Building

```bash
go mod tidy
CGO_ENABLED=0 go build -o whence .
```

## Code Quality

Always run before committing:

```bash
go fmt ./...
go vet ./...
go tool golangci-lint run
```

Code must pass all three with no errors or warnings.

## Running

```bash
./whence -addr :8080 -db ./data/whence.db -user default
```

## API Endpoints

- `POST /owntracks` - OwnTracks compatible
- `GET /gpslogger` - GPSLogger compatible
- `GET /api/locations` - GeoJSON for map
- `GET /` - Web frontend
