package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ali-sab/cloudbackupserver/backend/internal/db"
	"github.com/ali-sab/cloudbackupserver/backend/internal/models"
)

func parseFolderID(w http.ResponseWriter, r *http.Request) int64 {
	idStr := chi.URLParam(r, "folderID")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid folder id"})
		return 0
	}
	return id
}

func (h *Handler) requireFolder(w http.ResponseWriter, r *http.Request, userID int64) *models.WatchedPath {
	folderID := parseFolderID(w, r)
	if folderID == 0 {
		return nil
	}
	wp, err := db.GetWatchedPathByID(r.Context(), h.db, folderID, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "folder not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve folder"})
		}
		return nil
	}
	return wp
}

func (h *Handler) GetFolders(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	stats, err := db.GetFolderStats(r.Context(), h.db, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to load folders"})
		return
	}
	writeJSON(w, http.StatusOK, FolderStatsResponse{Folders: stats})
}

func (h *Handler) PostFolder(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())

	var req AddFolderRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.Path == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "path is required"})
		return
	}

	name := req.Name
	if name == "" {
		name = filepath.Base(req.Path)
	}

	wp, err := db.AddWatchedPath(r.Context(), h.db, userID, req.Path, name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to add folder"})
		return
	}
	writeJSON(w, http.StatusCreated, FolderResponse{ID: wp.ID, Path: wp.Path, Name: wp.Name, CreatedAt: wp.CreatedAt})
}

func (h *Handler) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	folderID := parseFolderID(w, r)
	if folderID == 0 {
		return
	}

	_, err := db.GetWatchedPathByID(r.Context(), h.db, folderID, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "folder not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve folder"})
		}
		return
	}

	if h.storage != nil {
		backups, err := db.GetFileBackupsByWatchedPathID(r.Context(), h.db, folderID)
		if err != nil {
			log.Printf("warn: failed to list backups for folder %d during delete: %v", folderID, err)
		} else {
			for _, b := range backups {
				if delErr := h.storage.DeleteObject(r.Context(), b.ObjectKey); delErr != nil {
					log.Printf("warn: failed to delete object %q for folder %d: %v", b.ObjectKey, folderID, delErr)
				}
			}
		}
	}

	if err := db.DeleteWatchedPath(r.Context(), h.db, folderID, userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "folder not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete folder"})
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) PutFolder(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	folderID := parseFolderID(w, r)
	if folderID == 0 {
		return
	}

	var req RenameFolderRequest
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "name is required"})
		return
	}

	if err := db.RenameWatchedPath(r.Context(), h.db, folderID, userID, strings.TrimSpace(req.Name)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "folder not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to rename folder"})
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) GetFiles(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}
	files, err := db.GetWatchedFiles(r.Context(), h.db, wp.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve files"})
		return
	}
	writeJSON(w, http.StatusOK, WatchedFilesResponse{Files: files})
}

func (h *Handler) PutSyncFiles(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 32<<20) // 32 MiB
	var req SyncWatchedFilesRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		log.Printf("debug: sync decode failed: %v", err)
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.Files == nil {
		req.Files = []FileEntry{}
	}

	watchedFiles := make([]models.WatchedFile, len(req.Files))
	for i, f := range req.Files {
		cleaned, pathErr := validateRelativePath(f.RelativePath)
		if pathErr != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: fmt.Sprintf("files[%d].relative_path: %s", i, pathErr.Error())})
			return
		}
		watchedFiles[i] = models.WatchedFile{
			Name:         f.Name,
			RelativePath: cleaned,
			IsDirectory:  f.IsDirectory,
			Size:         f.Size,
			ModifiedMs:   f.ModifiedMs,
		}
	}

	if err := db.SyncWatchedFiles(r.Context(), h.db, wp.ID, watchedFiles); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to sync files"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
