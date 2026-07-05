package onedrive

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlanSequentialUploadCalculatesAlignedContentRanges(t *testing.T) {
	total := int64(25 * 1024 * 1024)
	chunks, err := PlanSequentialUpload(total, DefaultChunkSizeBytes, 0)
	if err != nil {
		t.Fatalf("PlanSequentialUpload returned error: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}

	assertChunk(t, chunks[0], 0, 0, 10485759, 10485760, total, "bytes 0-10485759/26214400")
	assertChunk(t, chunks[1], 1, 10485760, 20971519, 10485760, total, "bytes 10485760-20971519/26214400")
	assertChunk(t, chunks[2], 2, 20971520, 26214399, 5242880, total, "bytes 20971520-26214399/26214400")
}

func TestPlanSequentialUploadResumesFromNextExpectedStart(t *testing.T) {
	total := int64(25 * 1024 * 1024)
	chunks, err := PlanSequentialUpload(total, DefaultChunkSizeBytes, DefaultChunkSizeBytes)
	if err != nil {
		t.Fatalf("PlanSequentialUpload returned error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	assertChunk(t, chunks[0], 0, 10485760, 20971519, 10485760, total, "bytes 10485760-20971519/26214400")
}

func TestPlanSequentialUploadRejectsUnalignedChunkSize(t *testing.T) {
	_, err := PlanSequentialUpload(1024*1024, 1024*1024, 0)
	if !errors.Is(err, ErrInvalidChunkPlan) {
		t.Fatalf("expected ErrInvalidChunkPlan, got %v", err)
	}
}

func TestBuildContentRange(t *testing.T) {
	got, err := BuildContentRange(5, 10, 20)
	if err != nil {
		t.Fatalf("BuildContentRange returned error: %v", err)
	}
	if got != "bytes 5-14/20" {
		t.Fatalf("unexpected content range: %s", got)
	}
}

func TestParseNextExpectedRanges(t *testing.T) {
	state, err := ParseNextExpectedRanges([]string{"10485760-", "20971520-26214399"})
	if err != nil {
		t.Fatalf("ParseNextExpectedRanges returned error: %v", err)
	}
	if state.NextStart != 10485760 {
		t.Fatalf("expected next start 10485760, got %d", state.NextStart)
	}
	if len(state.NextRanges) != 2 || !state.NextRanges[0].Open || state.NextRanges[1].End != 26214399 {
		t.Fatalf("unexpected ranges: %+v", state.NextRanges)
	}
}

func TestParseResumableStateRejectsMalformedRanges(t *testing.T) {
	_, err := ParseResumableState(strings.NewReader(`{"nextExpectedRanges":["abc-"]}`))
	if !errors.Is(err, ErrInvalidResumeState) {
		t.Fatalf("expected ErrInvalidResumeState, got %v", err)
	}
}

func TestBuildCreateUploadSessionMetadataUsesAppFolderPathAndLeastPrivilegeShape(t *testing.T) {
	meta, err := BuildCreateUploadSessionMetadata("", OneDriveDestination{Mode: "app_folder"}, CreateUploadSessionOptions{
		DestinationPath:  "Imports/clip 01.MP4",
		FileSizeBytes:    123,
		ConflictBehavior: ConflictBehaviorRename,
	})
	if err != nil {
		t.Fatalf("BuildCreateUploadSessionMetadata returned error: %v", err)
	}
	if meta.Method != "POST" {
		t.Fatalf("expected POST, got %s", meta.Method)
	}
	wantURL := "https://graph.microsoft.com/v1.0/me/drive/special/approot:/Imports/clip%2001.MP4:/createUploadSession"
	if meta.URL != wantURL {
		t.Fatalf("unexpected URL:\nwant %s\n got %s", wantURL, meta.URL)
	}

	var body map[string]map[string]string
	if err := json.Unmarshal(meta.Body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if body["item"]["@microsoft.graph.conflictBehavior"] != ConflictBehaviorRename {
		t.Fatalf("unexpected conflict behavior body: %s", string(meta.Body))
	}
}

func TestBuildChunkUploadRequest(t *testing.T) {
	chunk, err := NewChunkRange(0, 0, 10, 20)
	if err != nil {
		t.Fatalf("NewChunkRange returned error: %v", err)
	}
	req, err := BuildChunkUploadRequest(OneDriveUploadSession{UploadURL: "https://upload.example/session"}, chunk)
	if err != nil {
		t.Fatalf("BuildChunkUploadRequest returned error: %v", err)
	}
	if req.Method != "PUT" || req.ContentLength != 10 || req.Headers["Content-Range"] != "bytes 0-9/20" {
		t.Fatalf("unexpected request metadata: %+v", req)
	}
}

func TestClientUploadChunkSendsContentRangeAndParsesCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("method = %q, want PUT", r.Method)
		}
		if got := r.Header.Get("Content-Range"); got != "bytes 0-2/3" {
			t.Fatalf("Content-Range = %q", got)
		}
		if got := r.Header.Get("Content-Length"); got != "3" {
			t.Fatalf("Content-Length = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		if string(body) != "abc" {
			t.Fatalf("body = %q", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"drive-item-1","name":"clip.mp4","size":3}`)
	}))
	defer server.Close()

	chunk, err := NewChunkRange(0, 0, 3, 3)
	if err != nil {
		t.Fatalf("NewChunkRange returned error: %v", err)
	}
	client := &Client{HTTPClient: server.Client()}
	session, err := client.UploadChunk(context.Background(), OneDriveUploadSession{UploadURL: server.URL}, chunk, strings.NewReader("abc"))
	if err != nil {
		t.Fatalf("UploadChunk returned error: %v", err)
	}
	if session.DriveItemID != "drive-item-1" || session.NextStart != 3 {
		t.Fatalf("unexpected session: %+v", session)
	}
}

func TestClientUploadChunkParsesAcceptedResumeState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"nextExpectedRanges":["3-"]}`)
	}))
	defer server.Close()

	chunk, err := NewChunkRange(0, 0, 3, 6)
	if err != nil {
		t.Fatalf("NewChunkRange returned error: %v", err)
	}
	client := &Client{HTTPClient: server.Client()}
	session, err := client.UploadChunk(context.Background(), OneDriveUploadSession{UploadURL: server.URL}, chunk, strings.NewReader("abc"))
	if err != nil {
		t.Fatalf("UploadChunk returned error: %v", err)
	}
	if session.NextStart != 3 || len(session.NextRanges) != 1 || session.NextRanges[0] != "3-" {
		t.Fatalf("unexpected resumable session: %+v", session)
	}
}

func assertChunk(t *testing.T, got ChunkRange, index int, start, end, size, total int64, contentRange string) {
	t.Helper()
	if got.Index != index || got.Start != start || got.End != end || got.Size != size || got.Total != total {
		t.Fatalf("unexpected chunk:\nwant index=%d start=%d end=%d size=%d total=%d\n got %+v", index, start, end, size, total, got)
	}
	if got.ContentRange() != contentRange {
		t.Fatalf("expected content range %q, got %q", contentRange, got.ContentRange())
	}
}
