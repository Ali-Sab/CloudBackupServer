const { contextBridge, ipcRenderer } = require('electron');

// Expose a safe, typed API surface to the renderer via window.electronAPI.
// The renderer has no direct access to Node or Electron internals.
contextBridge.exposeInMainWorld('electronAPI', {
  /** Retrieve the stored auth token from the main process. */
  getToken: () => ipcRenderer.sendSync('get-token'),

  /** Persist a new auth token in the main process. */
  setToken: (token) => ipcRenderer.sendSync('set-token', token),

  /** Clear the stored auth token (logout). */
  clearToken: () => ipcRenderer.sendSync('clear-token'),
});
