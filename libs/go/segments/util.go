package segments

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// defaultID returns a short random hex id for a segment row (mirrors
// libs/go/experiments' id scheme). A crypto/rand failure is vanishingly rare;
// we fall back to a fixed prefix so a flush never panics.
func defaultID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "seg-" + hex.EncodeToString([]byte("fallback"))
	}
	return hex.EncodeToString(b[:])
}

// blobSafe makes a device id safe as a single blob key segment: it strips "/"
// and "\" (which would create unintended sub-prefixes or be rejected by the key
// validator) and empty ids become "_unknown". Everything else is preserved so
// the on-disk layout stays legible.
func blobSafe(device string) string {
	if device == "" {
		return "_unknown"
	}
	r := strings.NewReplacer("/", "_", "\\", "_")
	return r.Replace(device)
}
