package camera

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestNormalizeMediaPath(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "adds leading slash", input: "DCIM/100MEDIA/DJI_0001.MP4", want: "/DCIM/100MEDIA/DJI_0001.MP4"},
		{name: "converts windows separators", input: `\DCIM\100MEDIA\DJI_0001.MP4`, want: "/DCIM/100MEDIA/DJI_0001.MP4"},
		{name: "allows root for discovery probes", input: "/", want: "/"},
		{name: "rejects empty", input: " ", wantErr: true},
		{name: "rejects traversal", input: "../DCIM/DJI_0001.MP4", wantErr: true},
		{name: "rejects query", input: "/DCIM/file.mp4?x=1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeMediaPath(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeMediaPath(%q) expected error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeMediaPath(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeMediaPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildOsmoMediaURL(t *testing.T) {
	u, err := BuildOsmoMediaURL("http://192.168.2.1:80", CameraStorageSD, "/DCIM/100MEDIA/DJI_0001.MP4")
	if err != nil {
		t.Fatalf("BuildOsmoMediaURL returned error: %v", err)
	}

	if u.Scheme != "http" || u.Host != "192.168.2.1:80" || u.Path != "/v2" {
		t.Fatalf("unexpected URL authority/path: %s", u.String())
	}
	if got := u.Query().Get("storage"); got != "1" {
		t.Fatalf("storage query = %q, want 1", got)
	}
	if got := u.Query().Get("path"); got != "/DCIM/100MEDIA/DJI_0001.MP4" {
		t.Fatalf("path query = %q", got)
	}
}

func TestFormatRangeHeader(t *testing.T) {
	tests := []struct {
		name    string
		input   ByteRange
		want    string
		wantErr bool
	}{
		{name: "bounded", input: ByteRange{Start: 1024, Length: 4096}, want: "bytes=1024-5119"},
		{name: "open ended", input: ByteRange{Start: 2048}, want: "bytes=2048-"},
		{name: "negative start", input: ByteRange{Start: -1, Length: 1}, wantErr: true},
		{name: "negative length", input: ByteRange{Start: 0, Length: -1}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FormatRangeHeader(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("FormatRangeHeader() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseMediaResponseMetadata(t *testing.T) {
	resp := &http.Response{
		StatusCode:    http.StatusPartialContent,
		ContentLength: 1,
		Header: http.Header{
			"Accept-Ranges": []string{"bytes"},
			"Content-Type":  []string{"video/mp4"},
			"Content-Range": []string{"bytes 0-0/12345"},
			"ETag":          []string{`"abc"`},
			"Last-Modified": []string{"Sat, 04 Jul 2026 13:00:00 GMT"},
		},
	}

	got := ParseMediaResponseMetadata(resp)
	if got.StatusCode != http.StatusPartialContent || got.ContentLength != 1 || got.ContentType != "video/mp4" {
		t.Fatalf("unexpected metadata: %+v", got)
	}
	if !got.SupportsRanges {
		t.Fatal("expected SupportsRanges")
	}
	if got.ContentRange == nil || got.ContentRange.Start != 0 || got.ContentRange.End != 0 || got.ContentRange.Size != 12345 || !got.ContentRange.Known {
		t.Fatalf("unexpected content range: %+v", got.ContentRange)
	}
}

func TestBuildOsmoGETRequestAddsRange(t *testing.T) {
	req, err := BuildOsmoGETRequest(context.Background(), "http://192.168.2.1:80", CameraStorageInternal, "/DCIM/A.MP4", &ByteRange{Start: 10, Length: 5})
	if err != nil {
		t.Fatalf("BuildOsmoGETRequest returned error: %v", err)
	}
	if got := req.Method; got != http.MethodGet {
		t.Fatalf("method = %q", got)
	}
	if got := req.Header.Get("Range"); got != "bytes=10-14" {
		t.Fatalf("Range header = %q", got)
	}
	if got := req.URL.Query().Get("storage"); got != "0" {
		t.Fatalf("storage query = %q, want 0", got)
	}
}

func TestOsmoHTTPConnectorProbeEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2" {
			t.Fatalf("path = %q, want /v2", r.URL.Path)
		}

		if r.URL.Query().Get("storage") != "1" {
			t.Fatalf("storage = %q, want 1", r.URL.Query().Get("storage"))
		}
		if r.URL.Query().Get("path") != "/DCIM/100MEDIA/DJI_0001.MP4" {
			t.Fatalf("path query = %q", r.URL.Query().Get("path"))
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "4096")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != http.MethodGet {
			t.Fatalf("method = %q", r.Method)
		}
		if r.Header.Get("Range") != "bytes=0-0" {
			t.Fatalf("Range header = %q", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Range", "bytes 0-0/4096")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "x")
	}))
	defer server.Close()

	connector := &OsmoHTTPConnector{Client: server.Client(), BaseURL: server.URL, Storage: CameraStorageSD}
	got, err := connector.ProbeEndpoint(context.Background(), EndpointProbeRequest{Path: "/DCIM/100MEDIA/DJI_0001.MP4"})
	if err != nil {
		t.Fatalf("ProbeEndpoint returned error: %v", err)
	}
	if !got.Reachable || !got.V2EndpointOK || !got.HEADOK || !got.RangeOK {
		t.Fatalf("expected successful probe, got %+v", got)
	}
	if got.ContentLength != 4096 || got.ContentType != "video/mp4" {
		t.Fatalf("unexpected response metadata: %+v", got)
	}
	if !strings.Contains(got.EndpointPath, "/v2?") {
		t.Fatalf("EndpointPath = %q", got.EndpointPath)
	}
}

func TestProbePorts(t *testing.T) {
	if got := ProbePorts(1234); len(got) != 1 || got[0] != 1234 {
		t.Fatalf("ProbePorts explicit = %+v, want [1234]", got)
	}
	got := ProbePorts(0)
	if len(got) != 2 || got[0] != DefaultOsmoHTTPPort || got[1] != AlternateOsmoHTTPPort {
		t.Fatalf("ProbePorts default = %+v", got)
	}
	got[0] = 9999
	if DefaultOsmoHTTPPortCandidates[0] == 9999 {
		t.Fatal("ProbePorts returned shared default slice")
	}
}

func TestOsmoHTTPConnectorProbeEndpointCandidates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("path") != "/DCIM/100MEDIA/DJI_0001.MP4" {
			t.Fatalf("path query = %q", r.URL.Query().Get("path"))
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "2048")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Range", "bytes 0-0/2048")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "x")
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}

	connector := &OsmoHTTPConnector{Client: server.Client(), Storage: CameraStorageSD}
	got, err := connector.ProbeEndpointCandidates(context.Background(), EndpointProbeRequest{
		IPAddress: "127.0.0.1",
		Port:      port,
		Path:      "/DCIM/100MEDIA/DJI_0001.MP4",
		Storage:   CameraStorageSD,
	})
	if err != nil {
		t.Fatalf("ProbeEndpointCandidates returned error: %v", err)
	}
	if len(got.Results) != 1 || !got.Results[0].HEADOK || !got.Results[0].RangeOK {
		t.Fatalf("unexpected probe plan: %+v", got)
	}
	if !strings.Contains(got.Message, "validated") {
		t.Fatalf("message = %q", got.Message)
	}
}
