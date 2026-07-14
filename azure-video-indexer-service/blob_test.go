package main

import "testing"

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
