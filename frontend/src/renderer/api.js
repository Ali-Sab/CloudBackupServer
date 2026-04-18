/**
 * API layer — one function per backend endpoint.
 *
 * This is the only file that knows URL strings.
 * All functions return a raw fetch Response so callers can check .ok and call .json().
 * UI modules never call APIClient directly — they go through this layer.
 */

'use strict';

(function () {

  const _mod = (typeof require !== 'undefined' && typeof module !== 'undefined')
    ? require('./api-client')
    : null;
  const APIClient   = _mod ? _mod.APIClient   : window.APIClient;
  const TokenStore  = _mod ? _mod.TokenStore  : window.TokenStore;

  const API = {

    // ---- Session ----

    fetchSession() {
      return APIClient.request('/api/session');
    },

    // ---- Auth ----

    login(email, password) {
      return APIClient.post('/api/auth/login', { email, password });
    },

    register(email, password) {
      return APIClient.post('/api/auth/register', { email, password });
    },

    logout() {
      const refreshToken = TokenStore.getRefreshToken();
      return APIClient.post('/api/auth/logout', refreshToken ? { refresh_token: refreshToken } : {});
    },

    forgotPassword(email) {
      return APIClient.post('/api/auth/forgot-password', { email });
    },

    resetPassword(resetToken, newPassword) {
      return APIClient.post('/api/auth/reset-password', {
        reset_token: resetToken,
        new_password: newPassword,
      });
    },

    // ---- Folders ----

    /** Returns { folders: [...FolderStats] } */
    getFolders() {
      return APIClient.request('/api/folders');
    },

    /** Creates a new watched folder. Body: { path, name? }. Returns FolderResponse. */
    addFolder(path, name) {
      const body = { path };
      if (name !== undefined && name !== '') body.name = name;
      return APIClient.request('/api/folders', { method: 'POST', body: JSON.stringify(body) });
    },

    /** Deletes a folder and all its backups. */
    removeFolder(id) {
      return APIClient.request('/api/folders/' + id, { method: 'DELETE' });
    },

    // ---- Per-folder files ----

    /** Returns { files: [...] } for the given folder. */
    getFolderFiles(id) {
      return APIClient.request('/api/folders/' + id + '/files');
    },

    /**
     * Atomically replaces the stored file list for a folder.
     * @param {number} id
     * @param {Array<{name, relative_path, is_directory, size, modified_ms}>} files
     */
    syncFolderFiles(id, files) {
      return APIClient.put('/api/folders/' + id + '/sync', { files });
    },

    // ---- Per-folder backups ----

    /** Returns { backups: [...] } for the given folder. */
    getFolderBackups(id) {
      return APIClient.request('/api/folders/' + id + '/backups');
    },

    /**
     * Download a backed-up file from a specific folder.
     * Returns a raw Response with the binary body.
     * @param {number} id           - folder ID
     * @param {string} relativePath - e.g. "photos/2024/img.jpg"
     */
    downloadFromFolder(id, relativePath) {
      const encoded = relativePath.split('/').map(encodeURIComponent).join('/');
      return APIClient.request('/api/folders/' + id + '/backup/' + encoded);
    },
  };

  if (typeof module !== 'undefined') {
    module.exports = { API };
  } else if (typeof window !== 'undefined') {
    window.API = API;
  }

})();
