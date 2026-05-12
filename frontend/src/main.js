const { app, BrowserWindow, ipcMain, dialog, shell } = require('electron');
const path = require('path');
const fs = require('fs');
const crypto = require('crypto');
const http = require('http');

if (process.env.NODE_ENV === 'development') {
  require('electron-reload')(__dirname, {
    electron: path.join(__dirname, '..', 'node_modules', '.bin', 'electron'),
  });
}

// Allow the userData path to be overridden via --user-data-dir=<path> (used by E2E tests).
const _userDataArg = process.argv.find(a => a.startsWith('--user-data-dir='));
if (_userDataArg) app.setPath('userData', _userDataArg.slice('--user-data-dir='.length));

// ---- Renderer-from-localhost server ----
//
// We serve the renderer over http://localhost so SameSite=Strict cookies set
// by the API (also on localhost, different port) attach to fetches. file://
// renderers can't carry SameSite cookies — they're treated as null-origin.
//
// IMPORTANT: bind to 127.0.0.1 (loopback only, never reachable off-host) but
// load the URL by hostname `localhost`. Browsers consider `localhost` and
// `127.0.0.1` to be DIFFERENT SITES, so cookies set on http://localhost:8080
// would NOT attach to a renderer loaded from http://127.0.0.1:5173. Loading
// via the same `localhost` hostname keeps everything same-site.

const RENDERER_PORT = Number(process.env.RENDERER_PORT) || 5173;
const RENDERER_BIND = '127.0.0.1';
const RENDERER_HOST = 'localhost';
const RENDERER_DIR  = path.join(__dirname, 'renderer');

function startRendererServer() {
  return new Promise((resolve, reject) => {
    const server = http.createServer((req, res) => {
      // Strip query / hash; default to index.html.
      let urlPath = req.url.split('?')[0].split('#')[0];
      if (urlPath === '/' || urlPath === '') urlPath = '/index.html';

      // Resolve and confine to RENDERER_DIR — defends against ../ shenanigans.
      const requested = path.normalize(path.join(RENDERER_DIR, urlPath));
      if (!requested.startsWith(RENDERER_DIR + path.sep) && requested !== RENDERER_DIR) {
        res.statusCode = 403;
        return res.end('Forbidden');
      }

      fs.readFile(requested, (err, data) => {
        if (err) {
          res.statusCode = 404;
          return res.end('Not found');
        }
        const ext = path.extname(requested).toLowerCase();
        const types = {
          '.html': 'text/html; charset=utf-8',
          '.js':   'application/javascript; charset=utf-8',
          '.css':  'text/css; charset=utf-8',
          '.svg':  'image/svg+xml',
          '.png':  'image/png',
          '.jpg':  'image/jpeg',
          '.json': 'application/json',
          '.ico':  'image/x-icon',
        };
        res.setHeader('Content-Type', types[ext] || 'application/octet-stream');
        res.end(data);
      });
    });
    server.on('error', reject);
    server.listen(RENDERER_PORT, RENDERER_BIND, () => resolve(server));
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
      contextIsolation: true,
      nodeIntegration: false,
      partition: 'persist:main',
    },
  });

  win.loadURL(`http://${RENDERER_HOST}:${RENDERER_PORT}/index.html`);

  if (process.env.NODE_ENV === 'development') {
    win.webContents.openDevTools();
  }
}

// ---- IPC: directory browser ----

ipcMain.handle('select-directory', async () => {
  if (process.env.E2E_SELECT_DIR) {
    return process.env.E2E_SELECT_DIR;
  }
  const result = await dialog.showOpenDialog(win, { properties: ['openDirectory'] });
  return result.canceled ? null : result.filePaths[0];
});

// confineWithinRoot rejects relativePaths that escape rootPath.
// Returns { absPath } on success or { error } on rejection.
function confineWithinRoot(rootPath, relativePath) {
  const absRoot = path.resolve(rootPath);
  const absTarget = path.resolve(absRoot, relativePath.split('/').join(path.sep));
  const rel = path.relative(absRoot, absTarget);
  if (!rel || rel.startsWith('..') || path.isAbsolute(rel)) {
    return { error: 'Path is outside the watched directory' };
  }
  return { absPath: absTarget };
}

ipcMain.handle('read-directory', async (_event, dirPath) => {
  const results = [];
  async function walk(absDir, relDir) {
    let entries;
    try { entries = await fs.promises.readdir(absDir, { withFileTypes: true }); } catch { return; }
    for (const entry of entries) {
      const absPath = path.join(absDir, entry.name);
      const relPath = relDir ? relDir + '/' + entry.name : entry.name;
      let stat;
      try { stat = await fs.promises.stat(absPath); } catch { continue; }
      results.push({
        name: entry.name,
        relativePath: relPath,
        isDirectory: stat.isDirectory(),
        size: stat.size,
        modified: stat.mtimeMs,
        created: stat.birthtimeMs,
      });
      if (stat.isDirectory()) await walk(absPath, relPath);
    }
  }
  await walk(path.resolve(dirPath), '');
  return results;
});

// Starts watching dirPath for changes; sends 'directory-changed' on each event.
ipcMain.handle('watch-directory', (_event, dirPath) => {
  if (dirWatcher) { try { dirWatcher.close(); } catch {} dirWatcher = null; }
  try {
    dirWatcher = fs.watch(dirPath, { persistent: false, recursive: true }, (eventType, filename) => {
      if (win) win.webContents.send('directory-changed', { eventType, filename });
    });
    dirWatcher.on('error', () => {
      dirWatcher = null;
      if (win) win.webContents.send('directory-watch-failed', {});
    });
  } catch {
    if (win) win.webContents.send('directory-watch-failed', {});
  }
});

ipcMain.handle('unwatch-directory', () => {
  if (dirWatcher) {
    try { dirWatcher.close(); } catch {}
    dirWatcher = null;
  }
});

// ---- IPC: file backup upload ----

function computeFileChecksum(absPath) {
  return new Promise((resolve, reject) => {
    const hash = crypto.createHash('sha256');
    const stream = fs.createReadStream(absPath);
    stream.on('data', chunk => hash.update(chunk));
    stream.on('end', () => resolve(hash.digest('hex')));
    stream.on('error', reject);
  });
}

ipcMain.handle('checksum-file', async (_event, { rootPath, relativePath }) => {
  const conf = confineWithinRoot(rootPath, relativePath);
  if (conf.error) return { error: conf.error };
  try {
    const checksum = await computeFileChecksum(conf.absPath);
    return { checksum };
  } catch (e) {
    return { error: e.message };
  }
});

// Streams a file to the backend backup endpoint. Cookies are stored in the
// Electron session and attach automatically — no Authorization header needed.
ipcMain.handle('upload-file', async (_event, { rootPath, relativePath, apiBaseUrl, restoredFromVersionId }) => {
  const conf = confineWithinRoot(rootPath, relativePath);
  if (conf.error) return { error: conf.error };
  const absPath = conf.absPath;

  let stat;
  try { stat = fs.statSync(absPath); } catch (e) { return { error: `Cannot stat file: ${e.message}` }; }

  let checksum;
  try { checksum = await computeFileChecksum(absPath); }
  catch (e) { return { error: `Checksum failed: ${e.message}` }; }

  const encodedPath = relativePath.split('/').map(encodeURIComponent).join('/');
  let uploadUrl;
  try { new URL(`${apiBaseUrl}/${encodedPath}`); uploadUrl = `${apiBaseUrl}/${encodedPath}`; }
  catch (e) { return { error: `Invalid URL: ${e.message}` }; }

  // Use net.request so Chromium attaches session cookies automatically —
  // no manual Cookie header needed.
  const { net, session: electronSession } = require('electron');

  return new Promise((resolve) => {
    const req = net.request({
      method: 'PUT',
      url: uploadUrl,
      session: electronSession.fromPartition('persist:main'),
      useSessionCookies: true,
    });

    req.setHeader('Origin', `http://${RENDERER_HOST}:${RENDERER_PORT}`);
    req.setHeader('X-Checksum-SHA256', checksum);
    req.setHeader('X-File-Size', String(stat.size));
    req.setHeader('Content-Type', 'application/octet-stream');
    if (restoredFromVersionId) req.setHeader('X-Restored-From-Version-ID', String(restoredFromVersionId));

    req.on('response', (res) => {
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

    const stream = fs.createReadStream(absPath);
    stream.on('data', chunk => {
      if (!req.write(chunk)) stream.pause();
    });
    req.on('drain', () => stream.resume());
    stream.on('end', () => req.end());
    stream.on('error', (e) => { req.abort(); resolve({ error: e.message }); });
  });
});

ipcMain.handle('open-file', async (_event, { rootPath, relativePath }) => {
  const conf = confineWithinRoot(rootPath, relativePath);
  if (conf.error) return { error: conf.error };
  const err = await shell.openPath(conf.absPath);
  return err ? { error: err } : {};
});

ipcMain.handle('delete-file', async (_event, { rootPath, relativePath }) => {
  const conf = confineWithinRoot(rootPath, relativePath);
  if (conf.error) return { error: conf.error };
  try {
    fs.unlinkSync(conf.absPath);
    return {};
  } catch (e) {
    return { error: e.message };
  }
});

ipcMain.handle('save-file', async (_event, { rootPath, relativePath, buffer }) => {
  const conf = confineWithinRoot(rootPath, relativePath);
  if (conf.error) return { error: conf.error };
  try {
    fs.mkdirSync(path.dirname(conf.absPath), { recursive: true });
    fs.writeFileSync(conf.absPath, Buffer.from(buffer));
    return {};
  } catch (e) {
    return { error: e.message };
  }
});

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

ipcMain.handle('read-file-preview', (_event, { rootPath, relativePath }) => {
  const conf = confineWithinRoot(rootPath, relativePath);
  if (conf.error) return { error: conf.error };
  try {
    const stat = fs.statSync(conf.absPath);
    const MAX = 50 * 1024 * 1024;
    if (stat.size > MAX) return { error: 'too_large', size: stat.size };
    const length = Math.min(stat.size, MAX);
    const buf = Buffer.alloc(length);
    const fd = fs.openSync(conf.absPath, 'r');
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

// ---- App lifecycle ----

app.whenReady().then(async () => {
  try {
    await startRendererServer();
  } catch (e) {
    // Couldn't bind the renderer port — fatal.
    console.error('[main] failed to start renderer server:', e);
    app.quit();
    return;
  }
  createWindow();

  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) createWindow();
  });
});

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit();
});
