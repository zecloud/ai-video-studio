package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

type narrativeFailureKind string

const (
	narrativeFailureUnavailable narrativeFailureKind = "unavailable"
	narrativeFailureTimeout     narrativeFailureKind = "timeout"
	narrativeFailureTransient   narrativeFailureKind = "transient"
	narrativeFailureInvalid     narrativeFailureKind = "invalid_response"
	narrativeFailureInvalidReq  narrativeFailureKind = "invalid_request"
	narrativeFailureLimit       narrativeFailureKind = "limit"
)

type narrativeFailure struct {
	kind narrativeFailureKind
	err  error
}

func (e narrativeFailure) Error() string {
	if e.err != nil {
		return string(e.kind) + ": " + e.err.Error()
	}
	return string(e.kind)
}
func (e narrativeFailure) Unwrap() error { return e.err }
func narrativeFailureFor(err error) narrativeFailureKind {
	var failure narrativeFailure
	if errors.As(err, &failure) {
		return failure.kind
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return narrativeFailureTimeout
	}
	return narrativeFailureTransient
}
func narrativeFailureError(kind narrativeFailureKind, err error) error {
	return narrativeFailure{kind: kind, err: err}
}
func isRetryableNarrativeFailure(err error) bool {
	return narrativeFailureFor(err) == narrativeFailureTransient
}
func classifyNarrativeProviderError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return narrativeFailureError(narrativeFailureTimeout, err)
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "invalid") || strings.Contains(lower, "schema") || strings.Contains(lower, "structured output") {
		return narrativeFailureError(narrativeFailureInvalid, err)
	}
	return narrativeFailureError(narrativeFailureTransient, err)
}
func narrativeFailureHTTP(stage string, err error) (int, string, bool) {
	switch narrativeFailureFor(err) {
	case narrativeFailureUnavailable:
		return http.StatusServiceUnavailable, stage + "_unavailable", true
	case narrativeFailureTimeout:
		return http.StatusGatewayTimeout, stage + "_timeout", true
	case narrativeFailureInvalid:
		return http.StatusBadGateway, stage + "_invalid_response", false
	case narrativeFailureInvalidReq:
		return http.StatusUnprocessableEntity, stage + "_invalid", false
	case narrativeFailureLimit:
		return http.StatusUnprocessableEntity, stage + "_request_limit", false
	default:
		return http.StatusBadGateway, stage + "_upstream_failed", true
	}
}
