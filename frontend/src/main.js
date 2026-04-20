const { app, BrowserWindow, ipcMain, dialog, shell, safeStorage } = require('electron');
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

// Allow the userData path to be overridden via --user-data-dir=<path> (used by E2E tests).
// Must be called before app.whenReady() so all subsequent getPath('userData') calls see it.
const _userDataArg = process.argv.find(a => a.startsWith('--user-data-dir='));
if (_userDataArg) app.setPath('userData', _userDataArg.slice('--user-data-dir='.length));

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
        created: stat.birthtimeMs,
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
  if (dirWatcher) { dirWatcher.close(); dirWatcher = null; }
  try {
    dirWatcher = fs.watch(dirPath, { persistent: false, recursive: true }, (eventType, filename) => {
      if (win) win.webContents.send('directory-changed', { eventType, filename });
    });
    dirWatcher.on('error', () => { dirWatcher = null; });
  } catch {
    // recursive fs.watch not supported on this platform (Linux < Node 20); live updates disabled
  }
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

  // Build the request URL — apiBaseUrl is the full prefix up to (not including)
  // the relative path (e.g. "http://localhost:8080/api/folders/3/backup").
  const encodedPath = relativePath.split('/').map(encodeURIComponent).join('/');
  let parsedUrl;
  try {
    parsedUrl = new URL(`${apiBaseUrl}/${encodedPath}`);
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
      family: 4,
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

// Opens a file with the OS default application.
// Returns {} on success or { error: string } on failure.
ipcMain.handle('open-file', async (_event, { rootPath, relativePath }) => {
  const absPath = path.resolve(rootPath, relativePath.split('/').join(path.sep));
  const rel = path.relative(rootPath, absPath);
  if (!rel || rel.startsWith('..') || path.isAbsolute(rel)) {
    return { error: 'Path is outside the watched directory' };
  }
  const err = await shell.openPath(absPath);
  return err ? { error: err } : {};
});

// Deletes a single file within the watched directory.
// Returns {} on success or { error: string } on failure.
ipcMain.handle('delete-file', async (_event, { rootPath, relativePath }) => {
  const absPath = path.join(rootPath, relativePath.split('/').join(path.sep));
  try {
    fs.unlinkSync(absPath);
    return {};
  } catch (e) {
    return { error: e.message };
  }
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

// Reads up to 2 MB of a local file for in-app preview.
// Returns a flat array of POSIX relative paths for all files (not dirs) under rootPath.
// Uses async fs.promises with an iterative queue to avoid blocking the main process.
ipcMain.handle('get-all-file-paths', async (_event, { rootPath }) => {
  const results = [];
  const queue = [{ dir: path.resolve(rootPath), relDir: '' }];
  while (queue.length > 0) {
    const { dir, relDir } = queue.shift();
    let entries;
    try { entries = await fs.promises.readdir(dir, { withFileTypes: true }); } catch { continue; }
    for (const entry of entries) {
      const relPath = relDir ? relDir + '/' + entry.name : entry.name;
      if (entry.isDirectory()) queue.push({ dir: path.join(dir, entry.name), relDir: relPath });
      else results.push(relPath);
    }
  }
  return results;
});

// Returns { buffer: ArrayBuffer } (up to 50 MB) or { error: string, size? } if too large / unreadable.
// Reads only up to MAX bytes using a bounded fd read — does not load the entire file for large files.
ipcMain.handle('read-file-preview', (_event, { rootPath, relativePath }) => {
  const absPath = path.resolve(rootPath, relativePath.split('/').join(path.sep));
  const rel = path.relative(path.resolve(rootPath), absPath);
  if (!rel || rel.startsWith('..') || path.isAbsolute(rel)) {
    return { error: 'Path is outside the watched directory' };
  }
  try {
    const stat = fs.statSync(absPath);
    const MAX = 50 * 1024 * 1024;
    if (stat.size > MAX) return { error: 'too_large', size: stat.size };
    const length = Math.min(stat.size, MAX);
    const buf = Buffer.alloc(length);
    const fd = fs.openSync(absPath, 'r');
    try {
      const bytesRead = fs.readSync(fd, buf, 0, length, 0);
      return { buffer: buf.buffer.slice(buf.byteOffset, buf.byteOffset + bytesRead) };
    } finally {
      fs.closeSync(fd);
    }
  } catch (e) {
    return { error: e.message };
  }
});

// ---- IPC: remember-me (safeStorage keychain) ----

// Resolved lazily after app.ready so app.getPath('userData') is safe to call.
function rememberMePath() {
  return path.join(app.getPath('userData'), 'remember-me.bin');
}

ipcMain.handle('safe-storage-available', () => safeStorage.isEncryptionAvailable());

// Encrypts the refresh token with the OS keychain and writes it to disk.
ipcMain.handle('save-refresh-token', (_event, token) => {
  try {
    const encrypted = safeStorage.encryptString(token);
    fs.writeFileSync(rememberMePath(), encrypted);
    return {};
  } catch (e) {
    return { error: e.message };
  }
});

// Reads and decrypts the saved refresh token. Returns null if none is stored.
ipcMain.handle('load-refresh-token', () => {
  try {
    const encrypted = fs.readFileSync(rememberMePath());
    return safeStorage.decryptString(encrypted);
  } catch {
    return null;
  }
});

// Removes the saved refresh token file.
ipcMain.handle('clear-refresh-token', () => {
  try { fs.unlinkSync(rememberMePath()); } catch {}
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
