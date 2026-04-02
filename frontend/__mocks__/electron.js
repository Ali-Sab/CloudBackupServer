// Minimal Electron mock for Jest tests running in jsdom.
module.exports = {
  app: { whenReady: () => Promise.resolve(), on: () => {}, quit: () => {} },
  BrowserWindow: class {
    loadFile() {}
    on() {}
    webContents = { openDevTools: () => {} };
    static getAllWindows() { return []; }
  },
  ipcMain: { on: () => {} },
  ipcRenderer: { sendSync: () => null },
  contextBridge: { exposeInMainWorld: () => {} },
};
