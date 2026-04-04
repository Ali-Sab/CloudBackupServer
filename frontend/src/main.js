const { app, BrowserWindow, ipcMain } = require('electron');
const path = require('path');

// In-memory token store — persists for the process lifetime.
// Both tokens are updated together so they always stay in sync.
let accessToken = null;
let refreshToken = null;

function createWindow() {
  const win = new BrowserWindow({
    width: 1024,
    height: 768,
    title: 'Cloud Backup',
    webPreferences: {
      preload: path.join(__dirname, 'preload.js'),
      contextIsolation: true,   // security: isolate renderer from Node
      nodeIntegration: false,   // security: no Node in renderer
    },
  });

  win.loadFile(path.join(__dirname, 'renderer', 'index.html'));

  if (process.env.NODE_ENV === 'development') {
    win.webContents.openDevTools();
  }
}

// ---- IPC: token management ----

ipcMain.on('get-access-token', (event) => {
  event.returnValue = accessToken;
});

ipcMain.on('get-refresh-token', (event) => {
  event.returnValue = refreshToken;
});

ipcMain.on('set-tokens', (event, { access, refresh }) => {
  accessToken = access;
  refreshToken = refresh;
  event.returnValue = null;
});

ipcMain.on('clear-tokens', (event) => {
  accessToken = null;
  refreshToken = null;
  event.returnValue = null;
});

// ---- App lifecycle ----

app.whenReady().then(() => {
  createWindow();

  // macOS: re-create window when dock icon is clicked and no windows are open
  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) createWindow();
  });
});

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit();
});
