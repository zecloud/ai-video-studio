package main

import (
	"fmt"
	"os"
)

// Config holds runtime configuration for the media staging service, sourced
// entirely from environment variables so the service stays stateless and
// container-friendly.
type Config struct {
	// APIKey is the shared secret desktop clients must present as a bearer
	// token. Required — the service refuses to start without it.
	APIKey string

	// StorageConnectionString is the Azure Storage connection string used for
	// staging blobs and generating SAS URLs. Required.
	StorageConnectionString string

	// ContainerName is the default blob container used when a request does
	// not specify one explicitly.
	ContainerName string

	// Port is the TCP port the HTTP server listens on.
	Port string

	// CUEndpoint is the Azure Content Understanding endpoint URL.
	// Required for the /api/v1/analyze endpoint.
	CUEndpoint string

	// CUAPIKey is the Azure Content Understanding subscription key.
	// Required for the /api/v1/analyze endpoint.
	CUAPIKey string
}

// LoadConfig reads configuration from the environment and validates that
// required values are present. It never falls back to insecure defaults for
// required fields.
func LoadConfig() (*Config, error) {
	cfg := &Config{
		APIKey:                  os.Getenv("API_KEY"),
		StorageConnectionString: os.Getenv("STORAGE_CONNECTION_STRING"),
		ContainerName:           getEnvDefault("CONTAINER_NAME", "media-staging"),
		Port:                    getEnvDefault("PORT", "8080"),
		CUEndpoint:              os.Getenv("CU_ENDPOINT"),
		CUAPIKey:                os.Getenv("CU_API_KEY"),
	}

	if cfg.APIKey == "" {
		return nil, fmt.Errorf("API_KEY environment variable is required")
	}
	if cfg.StorageConnectionString == "" {
		return nil, fmt.Errorf("STORAGE_CONNECTION_STRING environment variable is required")
	}

	return cfg, nil
}

// CUConfigured returns true when the Content Understanding integration
// variables are both set.
func (c *Config) CUConfigured() bool {
	return c.CUEndpoint != "" && c.CUAPIKey != ""
}

func getEnvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
