package videoindexerstudio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderClientCreatePollCancelAndStreamOutput(t *testing.T) {
	var created CreateRenderJobRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-API-Key"); got != "test-api-key" {
			t.Fatalf("X-API-Key = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/render-jobs":
			if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"job":{"id":"render-1","projectId":"project-1","status":"queued","preset":"h264-1080p","outputName":"output.mp4"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/render-jobs/render-1":
			_, _ = io.WriteString(w, `{"job":{"id":"render-1","projectId":"project-1","status":"succeeded","preset":"h264-1080p","outputName":"output.mp4","output":{"container":"staging","blobName":"render-outputs/render-1/output.mp4","size":5,"mediaType":"video/mp4"}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/render-jobs/render-1/cancel":
			_, _ = io.WriteString(w, `{"job":{"id":"render-1","projectId":"project-1","status":"canceled","preset":"h264-1080p","outputName":"output.mp4"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/render-jobs/render-1/output":
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Disposition", `attachment; filename="output.mp4"`)
			w.Header().Set("Content-Length", "5")
			_, _ = io.WriteString(w, "video")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := testClient(t, server)
	req := CreateRenderJobRequest{ProjectID: "project-1", OneDriveAccessToken: "delegated", Clips: []RenderClipRequest{{ID: "clip-1", OneDriveItemID: "item-1", InMS: 0, OutMS: 1000}}, Preset: "h264-1080p", OutputName: "output.mp4", CorrelationID: "local-1"}
	createdResp, err := client.CreateRenderJob(context.Background(), req)
	if err != nil || createdResp.Job.Status != RenderJobStatusQueued || created.OneDriveAccessToken != "delegated" {
		t.Fatalf("CreateRenderJob() = %#v, %v, request=%#v", createdResp, err, created)
	}
	polled, err := client.GetRenderJob(context.Background(), "render-1")
	if err != nil || polled.Job.Status != RenderJobStatusSucceeded {
		t.Fatalf("GetRenderJob() = %#v, %v", polled, err)
	}
	stream, err := client.OpenRenderOutput(context.Background(), "render-1")
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(stream.Body)
	closeErr := stream.Body.Close()
	if readErr != nil || closeErr != nil || string(data) != "video" || stream.ContentLength != 5 || stream.FileName != "output.mp4" {
		t.Fatalf("stream = %#v, data=%q, read=%v close=%v", stream, data, readErr, closeErr)
	}
	canceled, err := client.CancelRenderJob(context.Background(), "render-1")
	if err != nil || canceled.Job.Status != RenderJobStatusCanceled {
		t.Fatalf("CancelRenderJob() = %#v, %v", canceled, err)
	}
	if strings.Contains(string(data), "sig=") {
		t.Fatal("stream contained a SAS URL")
	}
}
