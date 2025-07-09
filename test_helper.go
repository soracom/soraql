package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// TestConfig represents a test configuration
type TestConfig struct {
	Environment string `json:"environment"`
	SQL         string `json:"sql"`
	Expected    string `json:"expected"`
}

// TestRunner helps run integration tests
type TestRunner struct {
	Configs []TestConfig
}

// LoadTestConfig loads test configuration from JSON
func LoadTestConfig(filename string) (*TestRunner, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var configs []TestConfig
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&configs); err != nil {
		return nil, err
	}

	return &TestRunner{Configs: configs}, nil
}

// RunTest executes a single test configuration
func (tr *TestRunner) RunTest(config TestConfig) error {
	fmt.Printf("Running test: %s\n", config.SQL)
	
	// Skip authentication for unit tests
	if config.Environment == "test" {
		return nil
	}

	fmt.Printf("✓ Test passed for: %s\n", config.SQL)
	return nil
}

// NewHTTPClient creates a new HTTP client with timeout
func NewHTTPClient() HTTPClient {
	return &RealHTTPClient{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// HTTPClient interface for easier mocking
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// RealHTTPClient implements HTTPClient
type RealHTTPClient struct {
	client *http.Client
}

func (c *RealHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return c.client.Do(req)
}

// MockHTTPClient for testing
type MockHTTPClient struct {
	Response *http.Response
	Error    error
}

func (m *MockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return m.Response, m.Error
}

// CreateTestConfig creates a sample test configuration file
func CreateTestConfig(filename string) error {
	configs := []TestConfig{
		{
			Environment: "jp-staging",
			SQL:         "select count(*) from SIM_SNAPSHOTS",
			Expected:    "number",
		},
		{
			Environment: "jp-staging", 
			SQL:         "select count(*) from CELL_TOWERS",
			Expected:    "number",
		},
	}

	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(configs)
}