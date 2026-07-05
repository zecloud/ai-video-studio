package onedrive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type AuthFlow string

type AuthStatus string

const (
	AuthFlowAuthorizationCodePKCE AuthFlow = "authorization_code_pkce"
	AuthFlowDeviceCode            AuthFlow = "device_code"

	AuthNotConfigured AuthStatus = "not_configured"
	AuthSignedOut     AuthStatus = "signed_out"
	AuthSignedIn      AuthStatus = "signed_in"
)

const (
	GraphScopeFilesReadWriteAppFolder = "Files.ReadWrite.AppFolder"
	GraphScopeFilesReadWrite          = "Files.ReadWrite"

	DefaultGraphBaseURL       = "https://graph.microsoft.com/v1.0"
	DefaultChunkSizeBytes     = int64(10 * 1024 * 1024)
	GraphChunkAlignmentBytes  = int64(320 * 1024)
	ConflictBehaviorFail      = "fail"
	ConflictBehaviorRename    = "rename"
	ConflictBehaviorReplace   = "replace"
	createUploadSessionSuffix = "createUploadSession"
)

var DefaultGraphScopes = []string{GraphScopeFilesReadWriteAppFolder}

var (
	ErrInvalidUploadRequest       = errors.New("invalid OneDrive upload-session request")
	ErrInvalidChunkPlan           = errors.New("invalid OneDrive chunk plan")
	ErrInvalidResumeState         = errors.New("invalid OneDrive resumable state")
	ErrHTTPClientNotConfigured    = errors.New("OneDrive HTTP client is not configured")
	ErrTokenProviderNotConfigured = errors.New("OneDrive token provider is not configured")
	ErrUploadSessionURLMissing    = errors.New("OneDrive upload session URL is missing")
	ErrUnexpectedGraphStatus      = errors.New("unexpected Microsoft Graph response status")
)

type GraphAuthConfig struct {
	TenantID    string   `json:"tenantId,omitempty"`
	ClientID    string   `json:"clientId,omitempty"`
	RedirectURI string   `json:"redirectUri,omitempty"`
	AuthFlow    AuthFlow `json:"authFlow"`
	Scopes      []string `json:"scopes"`
}

type AuthState struct {
	Status              AuthStatus `json:"status"`
	AccountName         string     `json:"accountName,omitempty"`
	TenantID            string     `json:"tenantId,omitempty"`
	GrantedScopes       []string   `json:"grantedScopes,omitempty"`
	TokenCacheAvailable bool       `json:"tokenCacheAvailable"`
	ExpiresAt           string     `json:"expiresAt,omitempty"`
	Message             string     `json:"message,omitempty"`
}

type OneDriveDestination struct {
	Mode        string `json:"mode"`
	DisplayName string `json:"displayName"`
	Path        string `json:"path"`
	DriveID     string `json:"driveId,omitempty"`
	DriveItemID string `json:"driveItemId,omitempty"`
}

type OneDriveUploadSession struct {
	ID          string   `json:"id"`
	UploadURL   string   `json:"-"`
	DriveItemID string   `json:"driveItemId,omitempty"`
	NextStart   int64    `json:"nextStart"`
	NextRanges  []string `json:"nextRanges,omitempty"`
	ExpiresAt   string   `json:"expiresAt,omitempty"`
}

type CloudAsset struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	DriveItemID    string `json:"driveItemId"`
	OneDrivePath   string `json:"oneDrivePath"`
	SizeBytes      int64  `json:"sizeBytes"`
	ContentType    string `json:"contentType"`
	AnalysisStatus string `json:"analysisStatus,omitempty"`
}

type Status struct {
	AuthStatus  AuthStatus          `json:"authStatus"`
	Scope       string              `json:"scope"`
	Scopes      []string            `json:"scopes,omitempty"`
	Auth        AuthState           `json:"auth"`
	Destination OneDriveDestination `json:"destination"`
	Message     string              `json:"message"`
}

type Service interface {
	Status(context.Context) (Status, error)
	CreateUploadSession(context.Context, string, int64) (OneDriveUploadSession, error)
}

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type TokenProvider interface {
	AccessToken(context.Context, []string) (string, error)
}

type Client struct {
	HTTPClient    HTTPClient
	TokenProvider TokenProvider
	GraphBaseURL  string
	Scopes        []string
	Destination   OneDriveDestination
}

type CreateUploadSessionOptions struct {
	DestinationPath  string
	FileSizeBytes    int64
	ConflictBehavior string
}

type CreateUploadSessionMetadata struct {
	Method string
	URL    string
	Body   []byte
}

type createUploadSessionBody struct {
	Item uploadSessionItem `json:"item"`
}

type uploadSessionItem struct {
	ConflictBehavior string `json:"@microsoft.graph.conflictBehavior,omitempty"`
}

type graphUploadSessionResponse struct {
	UploadURL          string   `json:"uploadUrl"`
	ExpirationDateTime string   `json:"expirationDateTime"`
	NextExpectedRanges []string `json:"nextExpectedRanges"`
}

type graphDriveItemResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type ChunkRange struct {
	Index int   `json:"index"`
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	Size  int64 `json:"size"`
	Total int64 `json:"total"`
}

func (r ChunkRange) ContentRange() string {
	return fmt.Sprintf("bytes %d-%d/%d", r.Start, r.End, r.Total)
}

type ChunkUploadRequest struct {
	Method        string            `json:"method"`
	UploadURL     string            `json:"-"`
	ContentRange  string            `json:"contentRange"`
	ContentLength int64             `json:"contentLength"`
	Headers       map[string]string `json:"headers"`
}

type ResumeRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end,omitempty"`
	Open  bool  `json:"open"`
}

type ResumableState struct {
	NextStart  int64         `json:"nextStart"`
	NextRanges []ResumeRange `json:"nextRanges"`
	RawRanges  []string      `json:"rawRanges"`
}

func (c *Client) CreateUploadSession(ctx context.Context, destinationPath string, fileSizeBytes int64) (OneDriveUploadSession, error) {
	if c == nil {
		return OneDriveUploadSession{}, fmt.Errorf("%w: nil client", ErrInvalidUploadRequest)
	}
	if c.HTTPClient == nil {
		return OneDriveUploadSession{}, ErrHTTPClientNotConfigured
	}
	if c.TokenProvider == nil {
		return OneDriveUploadSession{}, ErrTokenProviderNotConfigured
	}

	scopes := c.Scopes
	if len(scopes) == 0 {
		scopes = DefaultGraphScopes
	}

	token, err := c.TokenProvider.AccessToken(ctx, scopes)
	if err != nil {
		return OneDriveUploadSession{}, err
	}
	if strings.TrimSpace(token) == "" {
		return OneDriveUploadSession{}, fmt.Errorf("%w: token provider returned an empty token", ErrTokenProviderNotConfigured)
	}

	meta, err := BuildCreateUploadSessionMetadata(c.GraphBaseURL, c.Destination, CreateUploadSessionOptions{
		DestinationPath:  destinationPath,
		FileSizeBytes:    fileSizeBytes,
		ConflictBehavior: ConflictBehaviorRename,
	})
	if err != nil {
		return OneDriveUploadSession{}, err
	}

	req, err := http.NewRequestWithContext(ctx, meta.Method, meta.URL, bytes.NewReader(meta.Body))
	if err != nil {
		return OneDriveUploadSession{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	res, err := c.HTTPClient.Do(req)
	if err != nil {
		return OneDriveUploadSession{}, err
	}
	defer res.Body.Close()

	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		return OneDriveUploadSession{}, fmt.Errorf("%w: createUploadSession returned %s", ErrUnexpectedGraphStatus, res.Status)
	}

	state, uploadURL, expiresAt, err := parseUploadSessionResponse(res.Body)
	if err != nil {
		return OneDriveUploadSession{}, err
	}
	return OneDriveUploadSession{
		UploadURL:  uploadURL,
		NextStart:  state.NextStart,
		NextRanges: state.RawRanges,
		ExpiresAt:  expiresAt,
	}, nil
}

func (c *Client) UploadChunk(ctx context.Context, session OneDriveUploadSession, chunk ChunkRange, body io.Reader) (OneDriveUploadSession, error) {
	if c == nil {
		return OneDriveUploadSession{}, fmt.Errorf("%w: nil client", ErrInvalidUploadRequest)
	}
	if c.HTTPClient == nil {
		return OneDriveUploadSession{}, ErrHTTPClientNotConfigured
	}
	if body == nil {
		return OneDriveUploadSession{}, fmt.Errorf("%w: chunk body is required", ErrInvalidChunkPlan)
	}

	meta, err := BuildChunkUploadRequest(session, chunk)
	if err != nil {
		return OneDriveUploadSession{}, err
	}
	req, err := http.NewRequestWithContext(ctx, meta.Method, meta.UploadURL, body)
	if err != nil {
		return OneDriveUploadSession{}, err
	}
	for key, value := range meta.Headers {
		req.Header.Set(key, value)
	}
	req.ContentLength = meta.ContentLength

	res, err := c.HTTPClient.Do(req)
	if err != nil {
		return OneDriveUploadSession{}, err
	}
	defer res.Body.Close()

	next := session
	switch res.StatusCode {
	case http.StatusAccepted:
		state, err := ParseResumableState(res.Body)
		if err != nil {
			return OneDriveUploadSession{}, err
		}
		next.NextStart = state.NextStart
		next.NextRanges = state.RawRanges
		return next, nil
	case http.StatusOK, http.StatusCreated:
		var item graphDriveItemResponse
		if err := json.NewDecoder(res.Body).Decode(&item); err != nil && !errors.Is(err, io.EOF) {
			return OneDriveUploadSession{}, fmt.Errorf("%w: invalid completed upload response: %v", ErrInvalidResumeState, err)
		}
		next.DriveItemID = item.ID
		next.NextStart = chunk.Total
		next.NextRanges = nil
		return next, nil
	default:
		return OneDriveUploadSession{}, fmt.Errorf("%w: chunk upload returned %s", ErrUnexpectedGraphStatus, res.Status)
	}
}

func BuildCreateUploadSessionMetadata(graphBaseURL string, destination OneDriveDestination, opts CreateUploadSessionOptions) (CreateUploadSessionMetadata, error) {
	if opts.FileSizeBytes <= 0 {
		return CreateUploadSessionMetadata{}, fmt.Errorf("%w: file size must be positive", ErrInvalidUploadRequest)
	}
	cleanDestinationPath, err := cleanGraphRelativePath(opts.DestinationPath)
	if err != nil {
		return CreateUploadSessionMetadata{}, err
	}

	conflictBehavior := strings.TrimSpace(opts.ConflictBehavior)
	if conflictBehavior == "" {
		conflictBehavior = ConflictBehaviorRename
	}
	switch conflictBehavior {
	case ConflictBehaviorFail, ConflictBehaviorRename, ConflictBehaviorReplace:
	default:
		return CreateUploadSessionMetadata{}, fmt.Errorf("%w: unsupported conflict behavior %q", ErrInvalidUploadRequest, conflictBehavior)
	}

	base := strings.TrimRight(strings.TrimSpace(graphBaseURL), "/")
	if base == "" {
		base = DefaultGraphBaseURL
	}

	body, err := json.Marshal(createUploadSessionBody{Item: uploadSessionItem{ConflictBehavior: conflictBehavior}})
	if err != nil {
		return CreateUploadSessionMetadata{}, err
	}

	return CreateUploadSessionMetadata{
		Method: http.MethodPost,
		URL:    base + buildCreateUploadSessionPath(destination, cleanDestinationPath),
		Body:   body,
	}, nil
}

func BuildContentRange(start, size, total int64) (string, error) {
	chunk, err := NewChunkRange(0, start, size, total)
	if err != nil {
		return "", err
	}
	return chunk.ContentRange(), nil
}

func NewChunkRange(index int, start, size, total int64) (ChunkRange, error) {
	if total <= 0 {
		return ChunkRange{}, fmt.Errorf("%w: total size must be positive", ErrInvalidChunkPlan)
	}
	if start < 0 || start >= total {
		return ChunkRange{}, fmt.Errorf("%w: start must be within the file", ErrInvalidChunkPlan)
	}
	if size <= 0 {
		return ChunkRange{}, fmt.Errorf("%w: chunk size must be positive", ErrInvalidChunkPlan)
	}
	end := start + size - 1
	if end >= total {
		end = total - 1
	}
	return ChunkRange{Index: index, Start: start, End: end, Size: end - start + 1, Total: total}, nil
}

func PlanSequentialUpload(total, chunkSize, resumeNextStart int64) ([]ChunkRange, error) {
	if total <= 0 {
		return nil, fmt.Errorf("%w: total size must be positive", ErrInvalidChunkPlan)
	}
	if chunkSize <= 0 {
		return nil, fmt.Errorf("%w: chunk size must be positive", ErrInvalidChunkPlan)
	}
	if chunkSize%GraphChunkAlignmentBytes != 0 {
		return nil, fmt.Errorf("%w: chunk size must be a multiple of %d bytes", ErrInvalidChunkPlan, GraphChunkAlignmentBytes)
	}
	if resumeNextStart < 0 || resumeNextStart > total {
		return nil, fmt.Errorf("%w: resume offset must be between 0 and total size", ErrInvalidChunkPlan)
	}
	if resumeNextStart == total {
		return []ChunkRange{}, nil
	}

	var chunks []ChunkRange
	for index, start := 0, resumeNextStart; start < total; index, start = index+1, start+chunkSize {
		chunk, err := NewChunkRange(index, start, chunkSize, total)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

func BuildChunkUploadRequest(session OneDriveUploadSession, chunk ChunkRange) (ChunkUploadRequest, error) {
	if strings.TrimSpace(session.UploadURL) == "" {
		return ChunkUploadRequest{}, ErrUploadSessionURLMissing
	}
	if chunk.Size <= 0 || chunk.Total <= 0 || chunk.Start < 0 || chunk.End < chunk.Start || chunk.End >= chunk.Total {
		return ChunkUploadRequest{}, fmt.Errorf("%w: invalid chunk range", ErrInvalidChunkPlan)
	}
	return ChunkUploadRequest{
		Method:        http.MethodPut,
		UploadURL:     session.UploadURL,
		ContentRange:  chunk.ContentRange(),
		ContentLength: chunk.Size,
		Headers: map[string]string{
			"Content-Length": strconv.FormatInt(chunk.Size, 10),
			"Content-Range":  chunk.ContentRange(),
		},
	}, nil
}

func ParseResumableState(r io.Reader) (ResumableState, error) {
	var response graphUploadSessionResponse
	if err := json.NewDecoder(r).Decode(&response); err != nil {
		return ResumableState{}, fmt.Errorf("%w: %v", ErrInvalidResumeState, err)
	}
	return ParseNextExpectedRanges(response.NextExpectedRanges)
}

func parseUploadSessionResponse(r io.Reader) (ResumableState, string, string, error) {
	var response graphUploadSessionResponse
	if err := json.NewDecoder(r).Decode(&response); err != nil {
		return ResumableState{}, "", "", fmt.Errorf("%w: %v", ErrInvalidResumeState, err)
	}
	if strings.TrimSpace(response.UploadURL) == "" {
		return ResumableState{}, "", "", ErrUploadSessionURLMissing
	}
	state, err := ParseNextExpectedRanges(response.NextExpectedRanges)
	if err != nil {
		return ResumableState{}, "", "", err
	}
	return state, response.UploadURL, response.ExpirationDateTime, nil
}

func ParseNextExpectedRanges(rawRanges []string) (ResumableState, error) {
	state := ResumableState{
		NextStart:  0,
		RawRanges:  append([]string(nil), rawRanges...),
		NextRanges: []ResumeRange{},
	}
	if len(rawRanges) == 0 {
		return state, nil
	}

	for _, raw := range rawRanges {
		parsed, err := parseResumeRange(raw)
		if err != nil {
			return ResumableState{}, err
		}
		if len(state.NextRanges) == 0 || parsed.Start < state.NextStart {
			state.NextStart = parsed.Start
		}
		state.NextRanges = append(state.NextRanges, parsed)
	}
	return state, nil
}

func parseResumeRange(raw string) (ResumeRange, error) {
	parts := strings.Split(strings.TrimSpace(raw), "-")
	if len(parts) != 2 || parts[0] == "" {
		return ResumeRange{}, fmt.Errorf("%w: malformed nextExpectedRanges entry %q", ErrInvalidResumeState, raw)
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 {
		return ResumeRange{}, fmt.Errorf("%w: invalid range start %q", ErrInvalidResumeState, raw)
	}
	if parts[1] == "" {
		return ResumeRange{Start: start, Open: true}, nil
	}
	end, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || end < start {
		return ResumeRange{}, fmt.Errorf("%w: invalid range end %q", ErrInvalidResumeState, raw)
	}
	return ResumeRange{Start: start, End: end}, nil
}

func buildCreateUploadSessionPath(destination OneDriveDestination, cleanDestinationPath string) string {
	encodedPath := encodeGraphPath(cleanDestinationPath)
	if destination.DriveID != "" && destination.DriveItemID != "" {
		return fmt.Sprintf("/drives/%s/items/%s:/%s:/%s", url.PathEscape(destination.DriveID), url.PathEscape(destination.DriveItemID), encodedPath, createUploadSessionSuffix)
	}
	return fmt.Sprintf("/me/drive/special/approot:/%s:/%s", encodedPath, createUploadSessionSuffix)
}

func cleanGraphRelativePath(raw string) (string, error) {
	clean := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	clean = strings.Trim(clean, "/")
	if clean == "" || clean == "." {
		return "", fmt.Errorf("%w: destination path is required", ErrInvalidUploadRequest)
	}
	segments := strings.Split(clean, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("%w: destination path contains unsafe segment %q", ErrInvalidUploadRequest, segment)
		}
	}
	return strings.Join(segments, "/"), nil
}

func encodeGraphPath(cleanPath string) string {
	segments := strings.Split(cleanPath, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}
