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

    // ---- Files ----

    /** Returns { id, path, updated_at } or null if no path is saved. */
    getWatchedPath() {
      return APIClient.request('/api/files/path');
    },

    /** Sets/replaces the watched path. Body: { path }. Returns { id, path, updated_at }. */
    setWatchedPath(path) {
      return APIClient.put('/api/files/path', { path });
    },

    /** Returns { files: [...] } — the last-synced file list. */
    getFiles() {
      return APIClient.request('/api/files/');
    },

    /**
     * Atomically replaces the stored file list.
     * @param {Array<{name, is_directory, size, modified_ms}>} files
     */
    syncFiles(files) {
      return APIClient.put('/api/files/sync', { files });
    },

    // ---- Backup ----

    /**
     * Download a backed-up file. Returns a raw Response with the binary body.
     * Use response.blob() or response.arrayBuffer() to access the bytes.
     * @param {string} relativePath  - e.g. "photos/2024/img.jpg"
     */
    downloadFile(relativePath) {
      const encoded = relativePath.split('/').map(encodeURIComponent).join('/');
      return APIClient.request('/api/files/backup/' + encoded);
    },
  };

  if (typeof module !== 'undefined') {
    module.exports = { API };
  } else if (typeof window !== 'undefined') {
    window.API = API;
  }

})();
