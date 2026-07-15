package server_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/Sammyjroberts/gantry/core/go/models"
)

// putBody does an HTTP PUT with the given body and returns the response.
func putBody(t *testing.T, url, contentType string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new PUT request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	return resp
}

// TestModelsRoundTrip: PUT → list → GET byte-exact with the right content type.
func TestModelsRoundTrip(t *testing.T) {
	ts, _ := newModelsServer(t)
	body := []byte(`<robot name="arm"><link name="base"/></robot>`)

	// PUT.
	presp := putBody(t, ts.URL+"/models/arm.urdf", "application/xml", body)
	if presp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT status = %d, want 201", presp.StatusCode)
	}
	presp.Body.Close()

	// List (trailing slash).
	lresp := httpGet(t, ts.URL+"/models/")
	if lresp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want 200", lresp.StatusCode)
	}
	var listed struct {
		Files []string `json:"files"`
	}
	decodeJSON(t, lresp, &listed)
	if len(listed.Files) != 1 || listed.Files[0] != "arm.urdf" {
		t.Fatalf("files = %v, want [arm.urdf]", listed.Files)
	}

	// GET bytes + content type.
	gresp := httpGet(t, ts.URL+"/models/arm.urdf")
	if gresp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want 200", gresp.StatusCode)
	}
	if ct := gresp.Header.Get("Content-Type"); ct != "application/xml" {
		t.Fatalf("content type = %q, want application/xml", ct)
	}
	if got := readAll(t, gresp); !bytes.Equal(got, body) {
		t.Fatalf("bytes not exact: %q", got)
	}
}

// TestModelsExtensionRejected: PUT of a disallowed extension is 415.
func TestModelsExtensionRejected(t *testing.T) {
	ts, blob := newModelsServer(t)
	resp := putBody(t, ts.URL+"/models/evil.sh", "text/plain", []byte("rm -rf"))
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", resp.StatusCode)
	}
	resp.Body.Close()
	if len(blob.m) != 0 {
		t.Fatal("disallowed file was stored")
	}
}

// TestModelsTraversalRejected: traversal attempts never store or read outside
// the models/ namespace. Go's client/ServeMux cleans "../" in paths, so we also
// hit an encoded segment and a dotdot-bearing name directly.
func TestModelsTraversalRejected(t *testing.T) {
	ts, blob := newModelsServer(t)

	// A name that reaches the handler but is a bad model name → 400.
	resp := putBody(t, ts.URL+"/models/..urdf", "application/xml", []byte("x"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("dotdot name PUT status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Encoded traversal in the path segment. However the server routes/cleans it,
	// nothing may be written and no upload may succeed.
	resp2 := putBody(t, ts.URL+"/models/%2e%2e%2fpasswd.urdf", "application/xml", []byte("x"))
	if resp2.StatusCode == http.StatusCreated {
		t.Fatalf("encoded traversal PUT unexpectedly succeeded (status %d)", resp2.StatusCode)
	}
	resp2.Body.Close()

	if len(blob.m) != 0 {
		t.Fatalf("traversal attempts stored blobs: keys present")
	}
}

// TestModelsSizeCap: PUT over the cap is 413.
func TestModelsSizeCap(t *testing.T) {
	ts, blob := newModelsServer(t, models.WithMaxFileBytes(8))
	resp := putBody(t, ts.URL+"/models/big.stl", "model/stl", make([]byte, 100))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	resp.Body.Close()
	if len(blob.m) != 0 {
		t.Fatal("oversize file stored")
	}
}

// TestModelsMissing404: GET of an unknown model is 404 (fs.ErrNotExist mapped).
func TestModelsMissing404(t *testing.T) {
	ts, _ := newModelsServer(t)
	if got := httpGet(t, ts.URL+"/models/nope.urdf").StatusCode; got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}
