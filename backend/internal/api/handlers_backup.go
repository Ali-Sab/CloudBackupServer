package api

import (
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
	obj, _, err := h.storage.GetObject(r.Context(), v.ObjectKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve object"})
		return
	}
	defer obj.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Backup-Version", strconv.Itoa(v.Version))
	w.Header().Set("X-Checksum-SHA256", v.ChecksumSHA256)
	w.Header().Set("Content-Length", strconv.FormatInt(v.Size, 10))
	if _, err := io.Copy(w, obj); err != nil {
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

	backup := &models.FileBackup{
		UserID:         userID,
		WatchedPathID:  wp.ID,
		RelativePath:   relativePath,
		Size:           fileSize,
		ChecksumSHA256: checksum,
		ObjectKey:      objectKey,
	}
	if err := db.UpsertFileBackup(r.Context(), h.db, backup); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to record backup"})
		return
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
