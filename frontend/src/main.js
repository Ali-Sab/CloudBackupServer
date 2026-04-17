const { app, BrowserWindow, ipcMain, dialog } = require('electron');
const path = require('path');
const fs = require('fs');
const crypto = require('crypto');
const http = require('http');
const https = require('https');

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
  // E2E test bypass: return the env var path directly instead of opening the OS dialog.
  if (process.env.E2E_SELECT_DIR) {
    return process.env.E2E_SELECT_DIR;
  }
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

// ---- IPC: file backup upload ----

// Streams a file through SHA-256 and returns the hex digest.
// Never loads the full file into memory.
function computeFileChecksum(absPath) {
  return new Promise((resolve, reject) => {
    const hash = crypto.createHash('sha256');
    const stream = fs.createReadStream(absPath);
    stream.on('data', chunk => hash.update(chunk));
    stream.on('end', () => resolve(hash.digest('hex')));
    stream.on('error', reject);
  });
}

// Returns the SHA-256 checksum of a file within the watched directory.
// Returns { checksum: string } on success, or { error: string } on failure.
ipcMain.handle('checksum-file', async (_event, { rootPath, relativePath }) => {
  const sep = path.sep;
  const absPath = path.join(rootPath, relativePath.split('/').join(sep));
  try {
    const checksum = await computeFileChecksum(absPath);
    return { checksum };
  } catch (e) {
    return { error: e.message };
  }
});

// Streams a single file to the backend backup endpoint.
// Computes SHA-256 by streaming the file (no buffering), then streams the bytes
// directly into the HTTP request body via fs.createReadStream().pipe(req).
//
// Uses native http/https instead of fetch because Node fetch does not reliably
// support streaming request bodies with a known Content-Length in all Electron versions.
//
// Returns { skipped: boolean, error: string|null }.
ipcMain.handle('upload-file', async (_event, { rootPath, relativePath, apiBaseUrl, accessToken }) => {
  const sep = path.sep;
  const absPath = path.join(rootPath, relativePath.split('/').join(sep));

  let stat;
  try {
    stat = fs.statSync(absPath);
  } catch (e) {
    return { error: `Cannot stat file: ${e.message}` };
  }

  // Stream the file through SHA-256 to get checksum — never loaded fully into memory.
  let checksum;
  try {
    checksum = await computeFileChecksum(absPath);
  } catch (e) {
    return { error: `Checksum failed: ${e.message}` };
  }

  // Build the request URL — each path segment is percent-encoded, slashes preserved.
  const encodedPath = relativePath.split('/').map(encodeURIComponent).join('/');
  let parsedUrl;
  try {
    parsedUrl = new URL(`${apiBaseUrl}/api/files/backup/${encodedPath}`);
  } catch (e) {
    return { error: `Invalid URL: ${e.message}` };
  }

  const protocol = parsedUrl.protocol === 'https:' ? https : http;

  return new Promise((resolve) => {
    const req = protocol.request({
      hostname: parsedUrl.hostname,
      port: parsedUrl.port || (parsedUrl.protocol === 'https:' ? 443 : 80),
      path: parsedUrl.pathname,
      method: 'PUT',
      headers: {
        ...(accessToken ? { 'Authorization': `Bearer ${accessToken}` } : {}),
        'X-Checksum-SHA256': checksum,
        'X-File-Size': String(stat.size),
        'Content-Type': 'application/octet-stream',
      },
    }, (res) => {
      let body = '';
      res.on('data', chunk => { body += chunk; });
      res.on('end', () => {
        if (res.statusCode === 200) {
          try {
            const parsed = JSON.parse(body);
            resolve({ skipped: !!parsed.skipped, error: null });
          } catch {
            resolve({ skipped: false, error: null });
          }
        } else {
          resolve({ error: `Upload failed: HTTP ${res.statusCode}` });
        }
      });
    });

    req.on('error', (e) => resolve({ error: e.message }));

    // Stream file bytes into the request body — no buffering.
    fs.createReadStream(absPath).pipe(req);
  });
});

// Writes downloaded bytes to a file within the watched directory root.
// Creates intermediate directories as needed.
// Returns {} on success or { error: string } on failure.
ipcMain.handle('save-file', async (_event, { rootPath, relativePath, buffer }) => {
  const sep = path.sep;
  const absPath = path.join(rootPath, relativePath.split('/').join(sep));
  try {
    fs.mkdirSync(path.dirname(absPath), { recursive: true });
    fs.writeFileSync(absPath, Buffer.from(buffer));
    return {};
  } catch (e) {
    return { error: e.message };
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
