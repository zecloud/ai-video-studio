package videoindexerstudio

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

const loopbackHTTPOptInEnv = "AI_VIDEO_STUDIO_ALLOW_LOOPBACK_HTTP"

func NormalizeEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: endpoint is required", ErrInvalidConfig)
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: endpoint must be an absolute HTTPS URL", ErrInvalidConfig)
	}
	if !parsed.IsAbs() || parsed.Host == "" {
		return "", fmt.Errorf("%w: endpoint must be an absolute HTTPS URL with a host", ErrInvalidConfig)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%w: endpoint must not include credentials", ErrInvalidConfig)
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("%w: endpoint must not include a fragment", ErrInvalidConfig)
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" {
		if scheme == "http" && allowLoopbackHTTP(parsed.Hostname()) {
			parsed.Fragment = ""
			return strings.TrimRight(parsed.String(), "/"), nil
		}
		return "", fmt.Errorf("%w: endpoint must use https", ErrInvalidConfig)
	}

	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func allowLoopbackHTTP(host string) bool {
	value := strings.TrimSpace(os.Getenv(loopbackHTTPOptInEnv))
	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return isLoopbackHost(host)
	default:
		return false
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}
