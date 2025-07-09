.PHONY: build test test-verbose test-coverage bench clean release github-release

# Build the binary
build:
	go build -o soraql

# Run tests
test:
	go test

# Run tests with verbose output
test-verbose:
	go test -v

# Run tests with coverage
test-coverage:
	go test -cover

# Run benchmark tests
bench:
	go test -bench=.

# Run tests in short mode (skip integration tests)
test-short:
	go test -short

# Build release archives for multiple platforms
release:
	@echo "Building release archives..."
	@mkdir -p dist
	@rm -rf tmp-build
	@mkdir -p tmp-build
	
	# macOS AMD64
	@echo "Building macOS AMD64..."
	@mkdir -p tmp-build/soraql-darwin-amd64
	GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o tmp-build/soraql-darwin-amd64/soraql
	@cd tmp-build && tar -czf ../dist/soraql-darwin-amd64.tar.gz soraql-darwin-amd64/
	
	# macOS ARM64 (Apple Silicon)
	@echo "Building macOS ARM64..."
	@mkdir -p tmp-build/soraql-darwin-arm64
	GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o tmp-build/soraql-darwin-arm64/soraql
	@cd tmp-build && tar -czf ../dist/soraql-darwin-arm64.tar.gz soraql-darwin-arm64/
	
	# Linux AMD64
	@echo "Building Linux AMD64..."
	@mkdir -p tmp-build/soraql-linux-amd64
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o tmp-build/soraql-linux-amd64/soraql
	@cd tmp-build && tar -czf ../dist/soraql-linux-amd64.tar.gz soraql-linux-amd64/
	
	# Linux ARM64
	@echo "Building Linux ARM64..."
	@mkdir -p tmp-build/soraql-linux-arm64
	GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o tmp-build/soraql-linux-arm64/soraql
	@cd tmp-build && tar -czf ../dist/soraql-linux-arm64.tar.gz soraql-linux-arm64/
	
	# Windows AMD64
	@echo "Building Windows AMD64..."
	@mkdir -p tmp-build/soraql-windows-amd64
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o tmp-build/soraql-windows-amd64/soraql.exe
	@cd tmp-build && zip -q ../dist/soraql-windows-amd64.zip soraql-windows-amd64/soraql.exe
	
	# Windows ARM64
	@echo "Building Windows ARM64..."
	@mkdir -p tmp-build/soraql-windows-arm64
	GOOS=windows GOARCH=arm64 go build -ldflags="-s -w" -o tmp-build/soraql-windows-arm64/soraql.exe
	@cd tmp-build && zip -q ../dist/soraql-windows-arm64.zip soraql-windows-arm64/soraql.exe
	
	@rm -rf tmp-build
	@echo "Release archives built in dist/ directory:"
	@ls -la dist/

# Create GitHub release (requires gh CLI and git tag)
github-release: release
	@if [ -z "$$(git describe --tags --exact-match 2>/dev/null)" ]; then \
		echo "Error: No git tag found. Create a tag first with: git tag v1.0.0"; \
		exit 1; \
	fi
	@echo "Creating GitHub release for tag $$(git describe --tags)..."
	gh release create $$(git describe --tags) \
		dist/* \
		--title "Release $$(git describe --tags)" \
		--generate-notes

# Clean build artifacts
clean:
	rm -f soraql
	rm -rf dist/
	rm -rf tmp-build/

# Run all checks (build, test, coverage)
check: build test test-coverage

# Help
help:
	@echo "Available targets:"
	@echo "  build         - Build the soraql binary"
	@echo "  test          - Run unit tests"
	@echo "  test-verbose  - Run tests with verbose output"
	@echo "  test-coverage - Run tests with coverage report"
	@echo "  bench         - Run benchmark tests"
	@echo "  test-short    - Run tests in short mode"
	@echo "  release       - Build release archives for multiple platforms"
	@echo "  github-release- Create GitHub release with archives (requires git tag)"
	@echo "  clean         - Clean build artifacts"
	@echo "  check         - Run build, test, and coverage"
	@echo "  help          - Show this help message"