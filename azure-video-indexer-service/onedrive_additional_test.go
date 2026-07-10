package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestOneDriveClientRedirectStripsAuthorizationAndStreamsLargeBody(t *testing.T) {
	var mu sync.Mutex
	var redirectedAuth string
	graphServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/me/drive/items/item-123/content":
			http.Redirect(w, r, "/download/item-123", http.StatusFound)
		case "/download/item-123":
			mu.Lock()
			redirectedAuth = r.Header.Get("Authorization")
			mu.Unlock()
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Disposition", `attachment; filename="clip.mp4"`)
			w.WriteHeader(http.StatusOK)
			for i := 0; i < 128; i++ {
				_, _ = io.WriteString(w, strings.Repeat("a", 16<<10))
			}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer graphServer.Close()

	client := NewOneDriveClient(graphServer.URL, graphServer.Client())
	body, meta, err := client.OpenItem(context.Background(), "item-123", "token-abc")
	if err != nil {
		t.Fatalf("open item: %v", err)
	}
	defer body.Close()

	total, err := io.Copy(io.Discard, body)
	if err != nil {
		t.Fatalf("copy body: %v", err)
	}
	if total != 2<<20 {
		t.Fatalf("unexpected streamed byte count: %d", total)
	}
	if meta.FileName != "clip.mp4" || meta.ContentType != "video/mp4" {
		t.Fatalf("unexpected metadata: %#v", meta)
	}
	mu.Lock()
	defer mu.Unlock()
	if redirectedAuth != "" {
		t.Fatalf("authorization leaked on redirect: %q", redirectedAuth)
	}
}
