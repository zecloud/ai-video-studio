package camera

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultOsmoHost        = "192.168.2.1"
	DefaultOsmoHTTPPort    = 80
	AlternateOsmoHTTPPort  = 7001
	DefaultOsmoMediaPath   = "/v2"
	DefaultOsmoProbePath   = "/DCIM"
	DefaultOsmoHTTPTimeout = 5 * time.Second
)

var DefaultOsmoHTTPPortCandidates = []int{DefaultOsmoHTTPPort, AlternateOsmoHTTPPort}

var (
	ErrInvalidMediaPath = errors.New("invalid media path")
	ErrInvalidByteRange = errors.New("invalid byte range")
)

type ByteRange struct {
	Start  int64 `json:"start"`
	Length int64 `json:"length,omitempty"`
}

type ContentRange struct {
	Unit   string `json:"unit"`
	Start  int64  `json:"start"`
	End    int64  `json:"end"`
	Size   int64  `json:"size,omitempty"`
	Known  bool   `json:"known"`
	Header string `json:"header"`
}

type MediaResponseMetadata struct {
	StatusCode     int           `json:"statusCode"`
	ContentLength  int64         `json:"contentLength,omitempty"`
	ContentType    string        `json:"contentType,omitempty"`
	AcceptRanges   string        `json:"acceptRanges,omitempty"`
	SupportsRanges bool          `json:"supportsRanges"`
	ContentRange   *ContentRange `json:"contentRange,omitempty"`
	ETag           string        `json:"etag,omitempty"`
	LastModified   string        `json:"lastModified,omitempty"`
}

type OsmoHTTPConnector struct {
	Client   *http.Client
	BaseURL  string
	Endpoint string
	Storage  CameraStorage
}

func NewOsmoHTTPConnector() *OsmoHTTPConnector {
	return &OsmoHTTPConnector{
		Client: &http.Client{
			Timeout: DefaultOsmoHTTPTimeout,
		},
		BaseURL:  fmt.Sprintf("http://%s:%d", DefaultOsmoHost, DefaultOsmoHTTPPort),
		Endpoint: DefaultOsmoMediaPath,
		Storage:  CameraStorageSD,
	}
}

func DefaultOsmoBaseURL(host string, port int) string {
	if host == "" {
		host = DefaultOsmoHost
	}
	if port == 0 {
		port = DefaultOsmoHTTPPort
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

func OsmoStorageID(storage CameraStorage) (int, error) {
	switch storage {
	case "", CameraStorageSD:
		return 1, nil
	case CameraStorageInternal:
		return 0, nil
	default:
		return 0, fmt.Errorf("unsupported camera storage %q", storage)
	}
}

func NormalizeMediaPath(mediaPath string) (string, error) {
	mediaPath = strings.TrimSpace(strings.ReplaceAll(mediaPath, "\\", "/"))
	if mediaPath == "" {
		return "", ErrInvalidMediaPath
	}
	if strings.Contains(mediaPath, "\x00") || strings.ContainsAny(mediaPath, "?#") {
		return "", ErrInvalidMediaPath
	}
	for _, segment := range strings.Split(strings.Trim(mediaPath, "/"), "/") {
		if segment == "." || segment == ".." {
			return "", ErrInvalidMediaPath
		}
	}

	cleaned := path.Clean("/" + strings.TrimLeft(mediaPath, "/"))
	if cleaned == "/" {
		return cleaned, nil
	}
	for _, segment := range strings.Split(strings.Trim(cleaned, "/"), "/") {
		if segment == "." || segment == ".." || segment == "" {
			return "", ErrInvalidMediaPath
		}
	}
	return cleaned, nil
}

func BuildOsmoMediaURL(baseURL string, storage CameraStorage, mediaPath string) (*url.URL, error) {
	if baseURL == "" {
		baseURL = DefaultOsmoBaseURL("", 0)
	}
	normalizedPath, err := NormalizeMediaPath(mediaPath)
	if err != nil {
		return nil, err
	}
	storageID, err := OsmoStorageID(storage)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid base url %q", baseURL)
	}
	u.Path = DefaultOsmoMediaPath
	query := u.Query()
	query.Set("storage", strconv.Itoa(storageID))
	query.Set("path", normalizedPath)
	u.RawQuery = query.Encode()
	return u, nil
}

func BuildOsmoHEADRequest(ctx context.Context, baseURL string, storage CameraStorage, mediaPath string) (*http.Request, error) {
	return buildOsmoRequest(ctx, http.MethodHead, baseURL, storage, mediaPath, nil)
}

func BuildOsmoGETRequest(ctx context.Context, baseURL string, storage CameraStorage, mediaPath string, byteRange *ByteRange) (*http.Request, error) {
	return buildOsmoRequest(ctx, http.MethodGet, baseURL, storage, mediaPath, byteRange)
}

func buildOsmoRequest(ctx context.Context, method string, baseURL string, storage CameraStorage, mediaPath string, byteRange *ByteRange) (*http.Request, error) {
	u, err := BuildOsmoMediaURL(baseURL, storage, mediaPath)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if byteRange != nil {
		header, err := FormatRangeHeader(*byteRange)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Range", header)
	}
	return req, nil
}

func FormatRangeHeader(byteRange ByteRange) (string, error) {
	if byteRange.Start < 0 || byteRange.Length < 0 {
		return "", ErrInvalidByteRange
	}
	if byteRange.Length == 0 {
		return fmt.Sprintf("bytes=%d-", byteRange.Start), nil
	}
	end := byteRange.Start + byteRange.Length - 1
	if end < byteRange.Start {
		return "", ErrInvalidByteRange
	}
	return fmt.Sprintf("bytes=%d-%d", byteRange.Start, end), nil
}

func ParseMediaResponseMetadata(resp *http.Response) MediaResponseMetadata {
	if resp == nil {
		return MediaResponseMetadata{}
	}
	metadata := MediaResponseMetadata{
		StatusCode:     resp.StatusCode,
		ContentLength:  resp.ContentLength,
		ContentType:    resp.Header.Get("Content-Type"),
		AcceptRanges:   resp.Header.Get("Accept-Ranges"),
		SupportsRanges: strings.EqualFold(resp.Header.Get("Accept-Ranges"), "bytes"),
		ETag:           resp.Header.Get("ETag"),
		LastModified:   resp.Header.Get("Last-Modified"),
	}
	if metadata.ContentLength < 0 {
		metadata.ContentLength = 0
	}
	if contentRange, ok := ParseContentRange(resp.Header.Get("Content-Range")); ok {
		metadata.ContentRange = &contentRange
		metadata.SupportsRanges = metadata.SupportsRanges || strings.EqualFold(contentRange.Unit, "bytes")
	}
	return metadata
}

func ParseContentRange(header string) (ContentRange, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return ContentRange{}, false
	}
	unit, remainder, ok := strings.Cut(header, " ")
	if !ok {
		return ContentRange{}, false
	}
	rangePart, sizePart, ok := strings.Cut(remainder, "/")
	if !ok {
		return ContentRange{}, false
	}
	startPart, endPart, ok := strings.Cut(rangePart, "-")
	if !ok {
		return ContentRange{}, false
	}
	start, err := strconv.ParseInt(startPart, 10, 64)
	if err != nil {
		return ContentRange{}, false
	}
	end, err := strconv.ParseInt(endPart, 10, 64)
	if err != nil || end < start {
		return ContentRange{}, false
	}
	contentRange := ContentRange{
		Unit:   unit,
		Start:  start,
		End:    end,
		Header: header,
	}
	if sizePart != "*" {
		size, err := strconv.ParseInt(sizePart, 10, 64)
		if err != nil {
			return ContentRange{}, false
		}
		contentRange.Size = size
		contentRange.Known = true
	}
	return contentRange, true
}

func (c *OsmoHTTPConnector) ProbeEndpoint(ctx context.Context, req EndpointProbeRequest) (EndpointProbeResult, error) {
	baseURL := c.baseURLFor(req)
	storage := req.Storage
	if storage == "" {
		storage = c.defaultStorage()
	}

	mediaPath := req.Path
	if mediaPath == "" || mediaPath == DefaultOsmoMediaPath {
		mediaPath = DefaultOsmoProbePath
	}

	result := EndpointProbeResult{
		BaseURL:      baseURL,
		EndpointPath: DefaultOsmoMediaPath,
		CheckedAt:    time.Now().UTC(),
		Message:      "Osmo media endpoint probe did not complete.",
	}
	targetURL, err := BuildOsmoMediaURL(baseURL, storage, mediaPath)
	if err != nil {
		result.Message = err.Error()
		return result, nil
	}
	result.EndpointPath = targetURL.RequestURI()

	headReq, err := BuildOsmoHEADRequest(ctx, baseURL, storage, mediaPath)
	if err != nil {
		result.Message = err.Error()
		return result, nil
	}
	headResp, err := c.httpClient().Do(headReq)
	if err != nil {
		result.Message = fmt.Sprintf("HEAD probe failed: %v", err)
		return result, nil
	}
	defer headResp.Body.Close()

	headMetadata := ParseMediaResponseMetadata(headResp)
	result.Reachable = true
	result.StatusCode = headMetadata.StatusCode
	result.ContentLength = headMetadata.ContentLength
	result.ContentType = headMetadata.ContentType
	result.HEADOK = headResp.StatusCode >= 200 && headResp.StatusCode < 400
	result.V2EndpointOK = result.HEADOK
	result.RangeOK = headMetadata.SupportsRanges

	rangeReq, err := BuildOsmoGETRequest(ctx, baseURL, storage, mediaPath, &ByteRange{Start: 0, Length: 1})
	if err != nil {
		result.Message = err.Error()
		return result, nil
	}
	rangeResp, err := c.httpClient().Do(rangeReq)
	if err != nil {
		result.Message = fmt.Sprintf("HEAD succeeded; Range probe failed: %v", err)
		return result, nil
	}
	defer rangeResp.Body.Close()
	result.RangeOK = result.RangeOK || rangeResp.StatusCode == http.StatusPartialContent || ParseMediaResponseMetadata(rangeResp).SupportsRanges
	if rangeResp.StatusCode == http.StatusPartialContent {
		result.V2EndpointOK = true
	}

	if result.HEADOK && result.RangeOK {
		result.Message = "Osmo media endpoint responded to HEAD and byte Range probes."
	} else if result.HEADOK {
		result.Message = "Osmo media endpoint responded to HEAD; byte Range support is unconfirmed."
	} else {
		result.Message = fmt.Sprintf("Osmo media endpoint responded with status %d.", headResp.StatusCode)
	}
	return result, nil
}

func (c *OsmoHTTPConnector) ProbeEndpointCandidates(ctx context.Context, req EndpointProbeRequest) (EndpointProbePlan, error) {
	ipAddress := strings.TrimSpace(req.IPAddress)
	if ipAddress == "" {
		ipAddress = DefaultOsmoHost
	}
	storage := req.Storage
	if storage == "" {
		storage = c.defaultStorage()
	}
	mediaPath := req.Path
	if mediaPath == "" {
		mediaPath = DefaultOsmoProbePath
	}
	ports := ProbePorts(req.Port)
	plan := EndpointProbePlan{
		IPAddress: ipAddress,
		Path:      mediaPath,
		Storage:   storage,
		Ports:     ports,
		Message:   "No Osmo media endpoint candidate has been probed yet.",
	}
	for _, port := range ports {
		result, err := c.ProbeEndpoint(ctx, EndpointProbeRequest{
			DeviceID:  req.DeviceID,
			IPAddress: ipAddress,
			Port:      port,
			Path:      mediaPath,
			Storage:   storage,
		})
		if err != nil {
			return plan, err
		}
		plan.Results = append(plan.Results, result)
		if result.HEADOK && result.RangeOK {
			plan.Message = fmt.Sprintf("Osmo media endpoint validated on %s.", result.BaseURL)
			return plan, nil
		}
	}
	plan.Message = "No candidate confirmed both HEAD and byte Range support. Verify camera Wi-Fi, file path, storage, and firmware-specific port."
	return plan, nil
}

func ProbePorts(port int) []int {
	if port > 0 {
		return []int{port}
	}
	return append([]int(nil), DefaultOsmoHTTPPortCandidates...)
}

func (c *OsmoHTTPConnector) OpenMediaStream(ctx context.Context, request MediaStreamRequest) (io.ReadCloser, error) {
	req, err := BuildOsmoGETRequest(ctx, c.BaseURL, request.Storage, request.Path, &ByteRange{Start: request.Offset, Length: request.Length})
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	if request.Length > 0 && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("expected 206 Partial Content for ranged media read, got %d", resp.StatusCode)
	}
	if request.Length == 0 && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		resp.Body.Close()
		return nil, fmt.Errorf("media read failed with status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func (c *OsmoHTTPConnector) ListMedia(context.Context, CameraDevice) ([]CameraMediaItem, error) {
	return nil, errors.New("Osmo media listing requires hardware validation of DJI listing or DUML discovery behavior")
}

func (c *OsmoHTTPConnector) httpClient() *http.Client {
	if c != nil && c.Client != nil {
		return c.Client
	}
	return NewOsmoHTTPConnector().Client
}

func (c *OsmoHTTPConnector) baseURLFor(req EndpointProbeRequest) string {
	if req.IPAddress != "" || req.Port != 0 {
		return DefaultOsmoBaseURL(req.IPAddress, req.Port)
	}
	if c != nil && c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultOsmoBaseURL("", 0)
}

func (c *OsmoHTTPConnector) defaultStorage() CameraStorage {
	if c != nil && c.Storage != "" {
		return c.Storage
	}
	return CameraStorageSD
}
