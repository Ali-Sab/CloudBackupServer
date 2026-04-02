const { app, BrowserWindow, ipcMain } = require('electron');
const path = require('path');

// In-memory token store — persists for the lifetime of the process.
// For production, consider the OS keychain via keytar.
let authToken = null;

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

// IPC: renderer retrieves the stored auth token via the preload bridge
ipcMain.on('get-token', (event) => {
  event.returnValue = authToken;
});

// IPC: renderer stores a new auth token
ipcMain.on('set-token', (event, token) => {
  authToken = token;
  event.returnValue = null;
});

// IPC: renderer clears the token on logout
ipcMain.on('clear-token', (event) => {
  authToken = null;
  event.returnValue = null;
});

app.whenReady().then(() => {
  createWindow();

  // macOS: re-create window when dock icon is clicked and no windows are open
  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) createWindow();
  });
});

// Quit on all windows closed (except macOS)
app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit();
});
