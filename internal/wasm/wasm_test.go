package wasm

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestEmbeddedEngine(t *testing.T) {
	sum := sha256.Sum256(Binary)
	if got := hex.EncodeToString(sum[:]); got != SHA256 {
		t.Fatalf("coffee_core.wasm sha256 = %s, pinned %s (run scripts/update-engine.sh)", got, SHA256)
	}
}
