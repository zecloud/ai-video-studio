package main

import (
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
)

func TestMediaContentType(t *testing.T) {
	tests := []struct {
		name        string
		sourceName  string
		contentType string
		want        string
		wantCode    string
	}{
		{name: "OneDrive video type", sourceName: "clip.mp4", contentType: "video/mp4; charset=binary", want: "video/mp4"},
		{name: "infer MP4 when generic", sourceName: "clip.mp4", contentType: "application/octet-stream", want: "video/mp4"},
		{name: "reject non video", sourceName: "notes.txt", contentType: "text/plain", wantCode: "source_media_type_unsupported"},
		{name: "reject disguised non video", sourceName: "clip.mp4", contentType: "text/plain", wantCode: "source_media_type_unsupported"},
		{name: "reject malformed type", sourceName: "clip.mp4", contentType: "not a media type", wantCode: "source_media_type_invalid"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := mediaContentType(test.sourceName, test.contentType)
			if test.wantCode != "" {
				if serviceErrorCode(err) != test.wantCode {
					t.Fatalf("mediaContentType() error = %v, want code %q", err, test.wantCode)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("mediaContentType() = %q, %v; want %q, nil", got, err, test.want)
			}
		})
	}
}

func TestClassifyAzureBlobOperationRetriesOnlyTransientResponses(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		retryable bool
	}{
		{name: "not found", status: http.StatusNotFound, retryable: false},
		{name: "validation", status: http.StatusBadRequest, retryable: false},
		{name: "conflict", status: http.StatusConflict, retryable: true},
		{name: "throttled", status: http.StatusTooManyRequests, retryable: true},
		{name: "service unavailable", status: http.StatusServiceUnavailable, retryable: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyAzureBlobOperation(&azcore.ResponseError{StatusCode: tc.status}, "blob_download_failed", "download failed")
			var serviceErr *ServiceError
			if !errors.As(err, &serviceErr) || serviceErr.Retryable != tc.retryable || serviceErr.Status != tc.status {
				t.Fatalf("classified error = %#v", serviceErr)
			}
		})
	}
}

func TestClassifyAzureBlobOperationRetriesTruncatedNetworkRead(t *testing.T) {
	err := classifyAzureBlobOperation(io.ErrUnexpectedEOF, "blob_download_failed", "download failed")
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || !serviceErr.Retryable || serviceErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("classified error = %#v", serviceErr)
	}
}
