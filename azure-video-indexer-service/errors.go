package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"encoding/json"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
)

var ErrUnexpectedStatus = errors.New("unexpected status")
var ErrInvalidConfig = errors.New("invalid video indexer config")

type ServiceError struct {
	Status    int
	Code      string
	Message   string
	Retryable bool
	Cause     error
}

func (e *ServiceError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *ServiceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *ServiceError) APIError() APIErrorResponse {
	if e == nil {
		return APIErrorResponse{}
	}
	return APIErrorResponse{Code: e.Code, Message: e.Message, Retryable: e.Retryable}
}

func newServiceError(status int, code, message string, retryable bool) *ServiceError {
	return &ServiceError{Status: status, Code: code, Message: message, Retryable: retryable}
}

func classifyHTTPStatus(status int) bool {
	switch {
	case status == http.StatusRequestTimeout,
		status == http.StatusTooManyRequests,
		status == http.StatusConflict,
		status == http.StatusLocked,
		status == http.StatusFailedDependency,
		status == http.StatusTooEarly,
		status >= 500:
		return true
	default:
		return false
	}
}

func decodeHTTPError(resp *http.Response, op string) error {
	if resp == nil {
		return fmt.Errorf("%w: %s", ErrUnexpectedStatus, op)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	text := strings.TrimSpace(string(body))
	retryable := classifyHTTPStatus(resp.StatusCode)
	if text == "" {
		return &ServiceError{
			Status:    resp.StatusCode,
			Code:      "http_status",
			Message:   redactURLsInText(fmt.Sprintf("%s: %s", op, resp.Status)),
			Retryable: retryable,
		}
	}
	var apiErr APIErrorResponse
	if err := jsonUnmarshal(body, &apiErr); err == nil && apiErr.Code != "" && apiErr.Message != "" {
		return &ServiceError{
			Status:    resp.StatusCode,
			Code:      apiErr.Code,
			Message:   redactURLsInText(apiErr.Message),
			Retryable: apiErr.Retryable || retryable,
		}
	}
	return &ServiceError{
		Status:    resp.StatusCode,
		Code:      "http_status",
		Message:   redactURLsInText(fmt.Sprintf("%s: %s", op, text)),
		Retryable: retryable,
	}
}

func jsonUnmarshal(data []byte, dst any) error {
	return json.Unmarshal(data, dst)
}

func isNotFound(err error) bool {
	var se *ServiceError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		return true
	}
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) && respErr.StatusCode == http.StatusNotFound {
		return true
	}
	return false
}

func isConflict(err error) bool {
	var se *ServiceError
	if errors.As(err, &se) && se.Status == http.StatusConflict {
		return true
	}
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) && respErr.StatusCode == http.StatusConflict {
		return true
	}
	return false
}

func classifyAzureBlobOperation(err error, code, message string) error {
	if err == nil {
		return nil
	}
	var serviceErr *ServiceError
	if errors.As(err, &serviceErr) {
		return err
	}
	status := http.StatusBadGateway
	retryable := false
	var responseErr *azcore.ResponseError
	if errors.As(err, &responseErr) {
		if responseErr.StatusCode > 0 {
			status = responseErr.StatusCode
		}
		retryable = classifyHTTPStatus(responseErr.StatusCode)
	} else {
		var networkErr net.Error
		retryable = errors.As(err, &networkErr) || errors.Is(err, io.ErrUnexpectedEOF)
		if retryable {
			status = http.StatusServiceUnavailable
		}
	}
	return &ServiceError{Status: status, Code: code, Message: redactURLsInText(message), Retryable: retryable, Cause: err}
}
func toAPIError(err error) (APIErrorResponse, int) {
	var se *ServiceError
	if errors.As(err, &se) {
		status := se.Status
		if status == 0 {
			status = http.StatusInternalServerError
		}
		return se.APIError(), status
	}
	return APIErrorResponse{Code: "internal_error", Message: redactURLsInText(err.Error()), Retryable: true}, http.StatusInternalServerError
}
