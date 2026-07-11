package main

import (
	"net/url"
	"regexp"
	"strings"
)

var urlPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)
var sensitiveQueryPattern = regexp.MustCompile(`(?i)\b(accessToken|oneDriveToken|sig)=([^&\s]+)`)

func redactURLsInText(text string) string {
	if text == "" {
		return ""
	}
	redacted := urlPattern.ReplaceAllStringFunc(text, redactURL)
	return sensitiveQueryPattern.ReplaceAllString(redacted, `${1}=<redacted>`)
}

func redactText(text, sourceURL string) string {
	if text == "" {
		return ""
	}
	redacted := redactURLsInText(text)
	if sourceURL != "" {
		redacted = strings.ReplaceAll(redacted, sourceURL, redactURL(sourceURL))
	}
	return redacted
}

func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "<redacted-url>"
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.User = nil
	return u.String()
}
