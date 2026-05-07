package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ali-sab/cloudbackupserver/backend/internal/db"
	"github.com/ali-sab/cloudbackupserver/backend/internal/delta"
	"github.com/ali-sab/cloudbackupserver/backend/internal/models"
	"github.com/ali-sab/cloudbackupserver/backend/internal/storage"
)

// HistoryResponse is returned by GET /api/history.
type HistoryResponse struct {
	Items  []db.HistoryItem `json:"items"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

// FileVersionsResponse is returned by GET /api/folders/{id}/versions.
type FileVersionsResponse struct {
	Versions []db.FileVersion `json:"versions"`
}

func (h *Handler) GetBackupHistory(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())

	folderID, _ := strconv.ParseInt(r.URL.Query().Get("folder_id"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	items, err := db.GetBackupHistory(r.Context(), h.db, userID, folderID, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to load history"})
		return
	}
	writeJSON(w, http.StatusOK, HistoryResponse{Items: items, Limit: limit, Offset: offset})
}

func (h *Handler) GetFileVersions(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}
	relativePath := r.URL.Query().Get("path")
	if relativePath == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "path query parameter is required"})
		return
	}
	relativePath, err := validateRelativePath(relativePath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	versions, err := db.GetFileVersions(r.Context(), h.db, wp.ID, relativePath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to load versions"})
		return
	}
	writeJSON(w, http.StatusOK, FileVersionsResponse{Versions: versions})
}

func (h *Handler) GetFileVersionDownload(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}
	versionID, err := strconv.ParseInt(chi.URLParam(r, "versionID"), 10, 64)
	if err != nil || versionID <= 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid versionID"})
		return
	}
	v, err := db.GetFileVersionByID(r.Context(), h.db, versionID, userID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "version not found"})
		return
	}
	if v.WatchedPathID != wp.ID {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "version not found"})
		return
	}
	data, err := h.reconstructVersionBytes(r.Context(), v)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve object"})
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Backup-Version", strconv.Itoa(v.Version))
	w.Header().Set("X-Checksum-SHA256", v.ChecksumSHA256)
	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(data)), 10))
	if _, err := io.Copy(w, bytes.NewReader(data)); err != nil {
		log.Printf("warn: short copy on version download key=%q err=%v", v.ObjectKey, err)
	}
}

func (h *Handler) GetFileBackups(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}
	backups, err := db.GetFileBackupsByWatchedPathID(r.Context(), h.db, wp.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to load backups"})
		return
	}
	writeJSON(w, http.StatusOK, FileBackupsResponse{Backups: backups})
}

// PutFileBackup handles PUT /api/folders/{folderID}/backup/*.
// Streams the request body into object storage. Skips when checksum matches.
// After upload, verifies the SHA-256 of the received bytes against the
// X-Checksum-SHA256 header to catch corrupt or misreported uploads.
func (h *Handler) PutFileBackup(w http.ResponseWriter, r *http.Request) {
	relativePath, err := validateRelativePath(chi.URLParam(r, "*"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	checksum := strings.ToLower(r.Header.Get("X-Checksum-SHA256"))
	if checksum == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "X-Checksum-SHA256 header is required"})
		return
	}
	if len(checksum) != 64 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "X-Checksum-SHA256 must be a 64-character hex string"})
		return
	}
	for _, c := range checksum {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "X-Checksum-SHA256 must be a 64-character hex string"})
			return
		}
	}

	fileSizeStr := r.Header.Get("X-File-Size")
	fileSize, fileSizeErr := strconv.ParseInt(fileSizeStr, 10, 64)
	if fileSizeErr != nil || fileSize < 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "X-File-Size header is required and must be a non-negative integer"})
		return
	}
	if fileSize > maxBackupSize {
		writeJSON(w, http.StatusRequestEntityTooLarge, ErrorResponse{Error: "file exceeds maximum allowed size"})
		return
	}

	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}

	existing, lookupErr := db.GetFileBackup(r.Context(), h.db, wp.ID, relativePath)
	if lookupErr == nil && existing.ChecksumSHA256 == checksum {
		writeJSON(w, http.StatusOK, UploadFileResponse{
			RelativePath:   existing.RelativePath,
			Size:           existing.Size,
			ChecksumSHA256: existing.ChecksumSHA256,
			BackedUpAt:     existing.BackedUpAt,
			Version:        existing.Version,
			Skipped:        true,
		})
		return
	}

	objectKey, err := storage.ObjectKey(userID, wp.ID, relativePath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	var oldKey string
	if lookupErr == nil && existing.ObjectKey != objectKey {
		oldKey = existing.ObjectKey
	}

	r.Body = http.MaxBytesReader(w, r.Body, fileSize+1024)

	// Tee the body through a SHA-256 hasher while streaming to storage.
	// This lets us verify the client's declared checksum without buffering.
	hasher := sha256.New()
	tee := io.TeeReader(r.Body, hasher)

	if err := h.storage.PutObject(r.Context(), objectKey, tee, fileSize, "application/octet-stream"); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "upload failed"})
		return
	}

	actualChecksum := hex.EncodeToString(hasher.Sum(nil))
	if actualChecksum != checksum {
		// Checksum mismatch: the uploaded bytes don't match the declared hash.
		// Delete the corrupt object and reject the request.
		if delErr := h.storage.DeleteObject(r.Context(), objectKey); delErr != nil {
			log.Printf("warn: failed to delete corrupt object %q after checksum mismatch: %v", objectKey, delErr)
		}
		writeJSON(w, http.StatusBadRequest, ErrorResponse{
			Error: fmt.Sprintf("checksum mismatch: declared %s, got %s", checksum, actualChecksum),
		})
		return
	}

	if oldKey != "" {
		if delErr := h.storage.DeleteObject(r.Context(), oldKey); delErr != nil {
			log.Printf("warn: failed to delete old object %q after re-upload: %v", oldKey, delErr)
		}
	}

	var restoredFromVersionID *int64
	if raw := r.Header.Get("X-Restored-From-Version-ID"); raw != "" {
		if id, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil && id > 0 {
			restoredFromVersionID = &id
		}
	}

	backup := &models.FileBackup{
		UserID:         userID,
		WatchedPathID:  wp.ID,
		RelativePath:   relativePath,
		Size:           fileSize,
		ChecksumSHA256: checksum,
		ObjectKey:      objectKey,
	}
	if err := db.UpsertFileBackup(r.Context(), h.db, backup, restoredFromVersionID); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to record backup"})
		return
	}

	// Content-addressable blob dedup: copy the object to a blob key keyed by
	// checksum so identical content across versions is only stored once.
	blobKey := storage.BlobKey(userID, checksum)
	exists, existsErr := h.storage.ObjectExists(r.Context(), blobKey)
	if existsErr != nil {
		log.Printf("warn: could not check blob existence key=%q: %v", blobKey, existsErr)
	} else {
		if !exists {
			if copyErr := h.storage.CopyObject(r.Context(), objectKey, blobKey); copyErr != nil {
				log.Printf("warn: failed to copy blob key=%q: %v", blobKey, copyErr)
				blobKey = ""
			}
		}
		if blobKey != "" {
			if updateErr := db.UpdateFileVersionObjectKey(r.Context(), h.db, wp.ID, relativePath, int64(backup.Version), blobKey); updateErr != nil {
				log.Printf("warn: failed to update version object_key version=%d: %v", backup.Version, updateErr)
			}
		}
	}

	// Attempt binary delta compression against the previous version. Failures are
	// non-fatal — the full blob is always kept as fallback.
	if delta.ShouldDelta(relativePath, fileSize, backup.Version) {
		prevVer, prevErr := db.GetFileVersionByNumber(r.Context(), h.db, wp.ID, relativePath, backup.Version-1)
		if prevErr == nil {
			prevBytes, prevErr := h.reconstructVersionBytes(r.Context(), prevVer)
			if prevErr == nil {
				curKey := objectKey
				if blobKey != "" {
					curKey = blobKey
				}
				curObj, _, curErr := h.storage.GetObject(r.Context(), curKey)
				if curErr == nil {
					newBytes, curErr := io.ReadAll(curObj)
					curObj.Close()
					if curErr == nil {
						patch, diffErr := delta.Diff(prevBytes, newBytes)
						if diffErr == nil && delta.IsWorthStoring(len(patch), len(newBytes)) {
							curVer, cvErr := db.GetFileVersionByNumber(r.Context(), h.db, wp.ID, relativePath, backup.Version)
							if cvErr == nil {
								deltaKey := storage.DeltaKey(userID, curVer.ID)
								putErr := h.storage.PutObject(r.Context(), deltaKey, bytes.NewReader(patch), int64(len(patch)), "application/octet-stream")
								if putErr == nil {
									if setErr := db.SetVersionDelta(r.Context(), h.db, curVer.ID, prevVer.ID, deltaKey); setErr == nil {
										if blobKey != "" {
											if count, _ := db.CountVersionsForObjectKey(r.Context(), h.db, blobKey); count == 0 {
												if delErr := h.storage.DeleteObject(r.Context(), blobKey); delErr != nil {
													log.Printf("warn: failed to delete superseded blob %q: %v", blobKey, delErr)
												}
											}
										}
									} else {
										log.Printf("warn: failed to set delta for version %d: %v", curVer.ID, setErr)
									}
								} else {
									log.Printf("warn: failed to store delta object %q: %v", deltaKey, putErr)
								}
							}
						}
					}
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, UploadFileResponse{
		RelativePath:   backup.RelativePath,
		Size:           backup.Size,
		ChecksumSHA256: backup.ChecksumSHA256,
		BackedUpAt:     backup.BackedUpAt,
		Version:        backup.Version,
		Skipped:        false,
	})
}

func (h *Handler) GetFileBackup(w http.ResponseWriter, r *http.Request) {
	relativePath, err := validateRelativePath(chi.URLParam(r, "*"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}

	backup, err := db.GetFileBackup(r.Context(), h.db, wp.ID, relativePath)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "file not backed up"})
		} else {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve backup record"})
		}
		return
	}

	reader, size, err := h.storage.GetObject(r.Context(), backup.ObjectKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve file"})
		return
	}
	defer reader.Close()

	fileName := filepath.Base(relativePath)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("warn: short copy on backup download key=%q err=%v", backup.ObjectKey, err)
	}
}

func (h *Handler) DeleteFileBackup(w http.ResponseWriter, r *http.Request) {
	relativePath, err := validateRelativePath(chi.URLParam(r, "*"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}

	objectKeys, err := db.DeleteFileBackup(r.Context(), h.db, wp.ID, userID, relativePath)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "file not backed up"})
		} else {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete backup"})
		}
		return
	}

	h.deleteObjectKeys(r.Context(), userID, objectKeys)
	w.WriteHeader(http.StatusNoContent)
}

// deleteObjectKeys deletes a set of storage objects, applying reference-counting
// for content-addressable blob keys so a blob is only removed when no version row
// references it anymore. Non-blob keys are always deleted. Duplicate keys are handled safely.
func (h *Handler) deleteObjectKeys(ctx context.Context, userID int64, keys []string) {
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, already := seen[key]; already {
			continue
		}
		seen[key] = struct{}{}

		if storage.IsBlobKey(userID, key) {
			count, err := db.CountVersionsForObjectKey(ctx, h.db, key)
			if err != nil {
				log.Printf("warn: refcount check failed for blob %q: %v", key, err)
				continue
			}
			if count > 0 {
				continue
			}
		}
		if err := h.storage.DeleteObject(ctx, key); err != nil {
			log.Printf("warn: failed to delete object %q: %v", key, err)
		}
	}
}

// reconstructVersionBytes reads the full file bytes for a version, following the
// delta chain if needed.
func (h *Handler) reconstructVersionBytes(ctx context.Context, v *db.FileVersion) ([]byte, error) {
	if v.DeltaBaseVersionID == nil {
		obj, _, err := h.storage.GetObject(ctx, v.ObjectKey)
		if err != nil {
			return nil, err
		}
		defer obj.Close()
		return io.ReadAll(obj)
	}

	chain := []*db.FileVersion{v}
	cur := v
	for cur.DeltaBaseVersionID != nil {
		base, err := db.GetFileVersionByIDInternal(ctx, h.db, *cur.DeltaBaseVersionID)
		if err != nil {
			return nil, fmt.Errorf("loading delta base version %d: %w", *cur.DeltaBaseVersionID, err)
		}
		chain = append(chain, base)
		cur = base
	}

	base := chain[len(chain)-1]
	obj, _, err := h.storage.GetObject(ctx, base.ObjectKey)
	if err != nil {
		return nil, fmt.Errorf("loading keyframe object: %w", err)
	}
	data, err := io.ReadAll(obj)
	obj.Close()
	if err != nil {
		return nil, fmt.Errorf("reading keyframe object: %w", err)
	}

	for i := len(chain) - 2; i >= 0; i-- {
		patchObj, _, err := h.storage.GetObject(ctx, chain[i].ObjectKey)
		if err != nil {
			return nil, fmt.Errorf("loading delta patch: %w", err)
		}
		patch, err := io.ReadAll(patchObj)
		patchObj.Close()
		if err != nil {
			return nil, fmt.Errorf("reading delta patch: %w", err)
		}
		data, err = delta.Patch(data, patch)
		if err != nil {
			return nil, fmt.Errorf("applying delta patch: %w", err)
		}
	}
	return data, nil
}
