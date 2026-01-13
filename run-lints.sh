#!/bin/bash
set -e

echo "Running go fmt..."
go fmt ./...

echo "Running go vet..."
go vet ./...

echo "Running golangci-lint..."
go tool golangci-lint run

echo "Checking for trailing whitespace..."
if grep -r --include="*.go" --include="*.js" --include="*.html" --include="*.css" --include="*.md" '[[:space:]]$' . 2>/dev/null; then
    echo "ERROR: Trailing whitespace found in above files"
    exit 1
fi

echo "All checks passed!"
