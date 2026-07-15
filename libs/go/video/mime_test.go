package video_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Sammyjroberts/gantry/libs/go/video"
)

// TestIngestNormalizesCodecMime is a regression guard for a bug the browser e2e
// suite surfaced: a MediaRecorder tags its blob Content-Type with codec params
// (e.g. "video/webm;codecs=vp9"), and the exact-match allowlist rejected it with
// 415 — so the real capture path never worked, even though unit tests (which
// posted a bare "video/webm") passed. IngestChunk must accept the parameterized
// form and store the playable base mime.
func TestIngestNormalizesCodecMime(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(t)

	cases := []struct {
		in   string
		want string
	}{
		{"video/webm;codecs=vp9", "video/webm"},
		{"video/webm;codecs=vp8", "video/webm"},
		{"video/webm; codecs=\"vp8,opus\"", "video/webm"},
		{"VIDEO/WEBM", "video/webm"},
		{"video/mp4;codecs=avc1.42E01E", "video/mp4"},
	}
	for i, c := range cases {
		start := time.Now().UnixNano() + int64(i)
		id, err := svc.IngestChunk(ctx, "cam", start, 2000, c.in, bytes.NewReader([]byte("\x1aE\xdf\xa3x")))
		if err != nil {
			t.Fatalf("IngestChunk(%q) failed: %v", c.in, err)
		}
		chunk, rc, err := svc.GetChunk(ctx, id)
		if err != nil {
			t.Fatalf("GetChunk: %v", err)
		}
		rc.Close()
		if chunk.Mime != c.want {
			t.Fatalf("stored mime for %q = %q, want %q (playable base type)", c.in, chunk.Mime, c.want)
		}
	}

	// A genuinely unsupported type is still rejected.
	if _, err := svc.IngestChunk(ctx, "cam", time.Now().UnixNano(), 2000, "image/png", bytes.NewReader([]byte("x"))); !errors.Is(err, video.ErrUnsupportedMime) {
		t.Fatalf("image/png err = %v, want ErrUnsupportedMime", err)
	}
}
