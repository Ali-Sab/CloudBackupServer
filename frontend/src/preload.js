const { contextBridge, ipcRenderer } = require('electron');

// Expose a minimal, typed API surface to the renderer via window.electronAPI.
// The renderer has no direct access to Node or Electron internals.
contextBridge.exposeInMainWorld('electronAPI', {
  /** Retrieve the current access token from the main process. */
  getAccessToken: () => ipcRenderer.sendSync('get-access-token'),

  /** Retrieve the current refresh token from the main process. */
  getRefreshToken: () => ipcRenderer.sendSync('get-refresh-token'),

  /**
   * Persist a new token pair.
   * Both tokens are always updated together to prevent mismatches.
   * @param {string} access
   * @param {string} refresh
   */
  setTokens: (access, refresh) => ipcRenderer.sendSync('set-tokens', { access, refresh }),

  /** Clear both tokens (called on logout or session expiry). */
  clearTokens: () => ipcRenderer.sendSync('clear-tokens'),

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
});
