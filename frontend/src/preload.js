const { contextBridge, ipcRenderer } = require('electron');

// Expose a minimal, typed API surface to the renderer via window.electronAPI.
// The renderer has no direct access to Node or Electron internals.
//
// Auth note: the API is cookie-only. The Electron session keeps cookies
// across restarts, so "remember me" is now implicit — log out to forget.
contextBridge.exposeInMainWorld('electronAPI', {

  // ---- Directory browser ----

  selectDirectory: () => ipcRenderer.invoke('select-directory'),
  readDirectory: (dirPath) => ipcRenderer.invoke('read-directory', dirPath),
  watchDirectory: (dirPath) => ipcRenderer.invoke('watch-directory', dirPath),
  unwatchDirectory: () => ipcRenderer.invoke('unwatch-directory'),

  /** Register a callback that fires whenever the watched directory changes. */
  onDirectoryChange: (callback) =>
    ipcRenderer.on('directory-changed', (_event, data) => callback(data)),

  /** Register a callback for when the watcher fails (e.g. recursive watch unsupported). */
  onDirectoryWatchFailed: (callback) =>
    ipcRenderer.on('directory-watch-failed', () => callback()),

  // ---- File backup ----

  /**
   * Upload a single file to the backend backup endpoint. The Electron session's
   * cookie jar is forwarded automatically — no token argument required.
   */
  uploadFile: (rootPath, relativePath, apiBaseUrl) =>
    ipcRenderer.invoke('upload-file', { rootPath, relativePath, apiBaseUrl }),

  checksumFile: (rootPath, relativePath) =>
    ipcRenderer.invoke('checksum-file', { rootPath, relativePath }),

  saveFile: (rootPath, relativePath, buffer) =>
    ipcRenderer.invoke('save-file', { rootPath, relativePath, buffer }),

  deleteFile: (rootPath, relativePath) =>
    ipcRenderer.invoke('delete-file', { rootPath, relativePath }),

  openFile: (rootPath, relativePath) =>
    ipcRenderer.invoke('open-file', { rootPath, relativePath }),

  getAllFilePaths: (rootPath) =>
    ipcRenderer.invoke('get-all-file-paths', { rootPath }),

  readFilePreview: (rootPath, relativePath) =>
    ipcRenderer.invoke('read-file-preview', { rootPath, relativePath }),
});
