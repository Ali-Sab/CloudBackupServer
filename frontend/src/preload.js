const { contextBridge, ipcRenderer } = require('electron');

// Expose a minimal, typed API surface to the renderer via window.electronAPI.
// The renderer has no direct access to Node or Electron internals.
contextBridge.exposeInMainWorld('electronAPI', {

  // ---- Directory browser ----

  /** Open native folder picker; resolves to the chosen path or null if cancelled. */
  selectDirectory: () => ipcRenderer.invoke('select-directory'),

  /** Read all entries in dirPath; resolves to { name, isDirectory, size, modified }[]. */
  readDirectory: (dirPath) => ipcRenderer.invoke('read-directory', dirPath),

  /** Start watching dirPath for changes. Replaces any previously active watcher. */
  watchDirectory: (dirPath) => ipcRenderer.invoke('watch-directory', dirPath),

  /** Stop the active directory watcher. */
  unwatchDirectory: () => ipcRenderer.invoke('unwatch-directory'),

  /** Register a callback that fires whenever the watched directory changes. */
  onDirectoryChange: (callback) =>
    ipcRenderer.on('directory-changed', (_event, data) => callback(data)),

  // ---- File backup ----

  /**
   * Upload a single file to the backend backup endpoint.
   * Streams the file bytes — does not buffer the entire file in memory.
   * SHA-256 is computed by streaming as well.
   * Auth is handled via the access_token cookie read from the Electron session store.
   *
   * @param {string} rootPath      - Absolute path to the watched directory root
   * @param {string} relativePath  - POSIX relative path within the root (e.g. "photos/img.jpg")
   * @param {string} apiBaseUrl    - Backend base URL (e.g. "http://localhost:8080")
   * @returns {Promise<{skipped: boolean, error: string|null}>}
   */
  uploadFile: (rootPath, relativePath, apiBaseUrl, accessToken) =>
    ipcRenderer.invoke('upload-file', { rootPath, relativePath, apiBaseUrl, accessToken }),
});
