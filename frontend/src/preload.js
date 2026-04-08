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
});
