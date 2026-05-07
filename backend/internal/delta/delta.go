// Package delta implements binary delta compression for file versions using
// the bsdiff/bspatch algorithm. Deltas are only stored when they offer a
// meaningful size reduction; otherwise the caller falls back to a full blob.
package delta

import (
	"path/filepath"
	"strings"

	"github.com/gabstv/go-bsdiff/pkg/bsdiff"
	"github.com/gabstv/go-bsdiff/pkg/bspatch"
)

const (
	// MaxDeltaInputSize is the largest file (in bytes) for which delta
	// compression is attempted. bsdiff loads both old and new into memory,
	// so very large files would spike RAM usage on the server.
	MaxDeltaInputSize = 50 * 1024 * 1024 // 50 MB

	// MaxDeltaRatio is the maximum (deltaSize / originalSize) ratio below which
	// we actually store the delta. Above this threshold a full blob is cheaper.
	MaxDeltaRatio = 0.80

	// KeyframeEvery forces a full blob every N versions so the reconstruction
	// chain never exceeds this depth, keeping restore latency bounded.
	KeyframeEvery = 10
)

// incompressibleExts lists file extensions whose contents are already
// compressed/encrypted, making byte-level deltas unlikely to help.
var incompressibleExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".webm": true,
	".mp3": true, ".aac": true, ".flac": true, ".ogg": true,
	".zip": true, ".gz": true, ".bz2": true, ".xz": true, ".zst": true,
	".tar": true, ".7z": true, ".rar": true,
	".pdf": true,
}

// ShouldDelta reports whether delta compression should be attempted for a
// file with the given name, size in bytes, and version number.
// It returns false for incompressible types, oversized files, and keyframe
// versions (multiples of KeyframeEvery, counting from 1).
func ShouldDelta(relativePath string, fileSize int64, version int) bool {
	if version <= 1 {
		return false // no previous version to diff against
	}
	if version%KeyframeEvery == 1 {
		return false // force full blob keyframe (versions 1, 11, 21, …)
	}
	if fileSize > MaxDeltaInputSize {
		return false
	}
	ext := strings.ToLower(filepath.Ext(relativePath))
	return !incompressibleExts[ext]
}

// Diff computes a bsdiff binary patch from oldData to newData.
// Returns the patch bytes or an error.
func Diff(oldData, newData []byte) ([]byte, error) {
	return bsdiff.Bytes(oldData, newData)
}

// Patch applies a bsdiff patch to oldData and returns the reconstructed newData.
func Patch(oldData, patch []byte) ([]byte, error) {
	return bspatch.Bytes(oldData, patch)
}

// IsWorthStoring reports whether storing a delta of deltaSize bytes is
// preferable to storing a full blob of originalSize bytes.
func IsWorthStoring(deltaSize, originalSize int) bool {
	if originalSize == 0 {
		return false
	}
	return float64(deltaSize)/float64(originalSize) < MaxDeltaRatio
}
