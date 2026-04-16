const { app, BrowserWindow, ipcMain, dialog } = require('electron');
const path = require('path');
const fs = require('fs');

if (process.env.NODE_ENV === 'development') {
  require('electron-reload')(__dirname, {
    electron: path.join(__dirname, '..', 'node_modules', '.bin', 'electron'),
  });
}

// Active directory watcher — only one at a time.
let dirWatcher = null;

// The main window — kept at module scope so IPC handlers can send events to it.
let win = null;

function createWindow() {
  win = new BrowserWindow({
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

// ---- IPC: directory browser ----

// Opens native folder picker; returns the chosen path or null if cancelled.
ipcMain.handle('select-directory', async () => {
  const result = await dialog.showOpenDialog(win, {
    properties: ['openDirectory'],
  });
  return result.canceled ? null : result.filePaths[0];
});

// Returns a flat array of { name, relativePath, isDirectory, size, modified } for every
// entry in the tree rooted at dirPath, recursively.
// The tree structure is encoded in relativePath (POSIX-style, e.g. "photos/2024/img.jpg").
// Top-level entries have relativePath === name.
// Permission errors on individual entries are silently skipped.
ipcMain.handle('read-directory', (_event, dirPath) => {
  const results = [];
  function walk(absDir, relDir) {
    let entries;
    try { entries = fs.readdirSync(absDir); } catch { return; }
    for (const name of entries) {
      const absPath = path.join(absDir, name);
      const relPath = relDir ? relDir + '/' + name : name;
      let stat;
      try { stat = fs.statSync(absPath); } catch { continue; }
      results.push({
        name,
        relativePath: relPath,
        isDirectory: stat.isDirectory(),
        size: stat.size,
        modified: stat.mtimeMs,
      });
      if (stat.isDirectory()) walk(absPath, relPath);
    }
  }
  walk(dirPath, '');
  return results;
});

// Starts watching dirPath for changes; sends 'directory-changed' to the renderer on each event.
// Replaces any previously active watcher.
ipcMain.handle('watch-directory', (_event, dirPath) => {
  if (dirWatcher) dirWatcher.close();
  dirWatcher = fs.watch(dirPath, { persistent: false, recursive: true }, (eventType, filename) => {
    if (win) win.webContents.send('directory-changed', { eventType, filename });
  });
});

// Stops the active directory watcher.
ipcMain.handle('unwatch-directory', () => {
  if (dirWatcher) {
    dirWatcher.close();
    dirWatcher = null;
  }
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
