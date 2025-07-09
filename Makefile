.PHONY: build test test-verbose test-coverage bench clean

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

# Clean build artifacts
clean:
	rm -f soraql

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
	@echo "  clean         - Clean build artifacts"
	@echo "  check         - Run build, test, and coverage"
	@echo "  help          - Show this help message"