package delta_test

import (
	"bytes"
	"testing"

	"github.com/ali-sab/cloudbackupserver/backend/internal/delta"
)

func TestDiffPatch_RoundTrip(t *testing.T) {
	old := []byte("hello world, this is the original content of the file")
	new := []byte("hello world, this is the modified content of the file!")

	patch, err := delta.Diff(old, new)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	reconstructed, err := delta.Patch(old, patch)
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}

	if !bytes.Equal(reconstructed, new) {
		t.Fatalf("reconstructed %q, want %q", reconstructed, new)
	}
}

func TestDiffPatch_IdenticalInputs(t *testing.T) {
	data := []byte("unchanged content")
	patch, err := delta.Diff(data, data)
	if err != nil {
		t.Fatalf("Diff identical: %v", err)
	}
	reconstructed, err := delta.Patch(data, patch)
	if err != nil {
		t.Fatalf("Patch identical: %v", err)
	}
	if !bytes.Equal(reconstructed, data) {
		t.Fatalf("round-trip of identical data failed")
	}
}

func TestDiffPatch_EmptyOld(t *testing.T) {
	new := []byte("brand new content")
	patch, err := delta.Diff([]byte{}, new)
	if err != nil {
		t.Fatalf("Diff empty old: %v", err)
	}
	reconstructed, err := delta.Patch([]byte{}, patch)
	if err != nil {
		t.Fatalf("Patch empty old: %v", err)
	}
	if !bytes.Equal(reconstructed, new) {
		t.Fatalf("round-trip from empty old failed: got %q", reconstructed)
	}
}

func TestIsWorthStoring(t *testing.T) {
	cases := []struct {
		deltaSize    int
		originalSize int
		want         bool
	}{
		{500, 1000, true},   // 50% ratio — worth storing
		{799, 1000, true},   // 79.9% — just under 80% threshold
		{800, 1000, false},  // exactly 80% — not strictly less, not worth it
		{850, 1000, false},  // 85% — not worth it
		{1000, 1000, false}, // 100% — full blob is no worse
		{0, 1000, true},     // empty delta is always worth it
		{100, 0, false},     // zero-length original — never worth it
	}
	for _, c := range cases {
		got := delta.IsWorthStoring(c.deltaSize, c.originalSize)
		if got != c.want {
			t.Errorf("IsWorthStoring(%d, %d) = %v, want %v", c.deltaSize, c.originalSize, got, c.want)
		}
	}
}

func TestShouldDelta(t *testing.T) {
	cases := []struct {
		path    string
		size    int64
		version int
		want    bool
	}{
		{"file.txt", 1024, 2, true},
		{"file.txt", 1024, 1, false},                       // version 1 has no prior
		{"file.txt", 1024, 11, false},                      // keyframe version (11 % 10 == 1)
		{"file.txt", 1024, 21, false},                      // keyframe
		{"file.txt", 1024, 12, true},                       // not a keyframe
		{"photo.jpg", 1024, 2, false},                      // incompressible ext
		{"archive.zip", 1024, 2, false},                    // incompressible ext
		{"file.txt", delta.MaxDeltaInputSize + 1, 2, false}, // too large
		{"file.txt", delta.MaxDeltaInputSize, 2, true},     // exactly at limit — allowed
	}
	for _, c := range cases {
		got := delta.ShouldDelta(c.path, c.size, c.version)
		if got != c.want {
			t.Errorf("ShouldDelta(%q, %d, %d) = %v, want %v", c.path, c.size, c.version, got, c.want)
		}
	}
}
