/**
 * File browser UI — recursive directory tree with drill-down navigation.
 *
 * Depends on:
 *   window.electronAPI  (readDirectory, watchDirectory, unwatchDirectory,
 *                        onDirectoryChange, checksumFile, uploadFile, saveFile)
 *   window.API          (getFolderFiles, syncFolderFiles, getFolderBackups,
 *                        downloadFromFolder)
 *   window.Dashboard    (show)
 *   window.UI           (toast)
 *
 * Exposes: window.Files = { show, hide }
 *   show(folderId, folderPath) — opens the browser for a specific watched folder
 *   hide()                    — closes the browser
 *
 * Navigation model
 * ────────────────
 * Two independent interactions on directory rows:
 *   • Click the ▶/▼ toggle arrow  → expand/collapse the folder inline (tree stays)
 *   • Click the folder name        → drill into the folder (view resets to that subtree)
 *
 * viewStack tracks the drill-down path:
 *   []                       = at root
 *   [{name:'photos',…}]      = inside photos/
 *   [{…},{name:'2024',…}]    = inside photos/2024/
 *
 * The breadcrumb at the top always reflects viewStack and lets the user
 * jump back to any ancestor level, or use the ← Back button for one level up.
 */

'use strict';

// ---- Pure / testable functions ------------------------------------------

/**
 * Map a filename to an emoji icon based on its extension.
 * Pure function — no DOM or IPC dependencies.
 * @param {string} filename
 * @returns {string}
 */
function fileTypeIcon(filename) {
  const ext = (filename.split('.').pop() || '').toLowerCase();
  const images  = ['jpg','jpeg','png','gif','bmp','webp','svg','ico','tiff','heic','avif','raw'];
  const video   = ['mp4','mov','avi','mkv','webm','flv','wmv','m4v','3gp'];
  const audio   = ['mp3','wav','ogg','flac','aac','m4a','wma','opus'];
  const code    = ['js','ts','jsx','tsx','py','go','rs','java','c','cpp','h','hpp','cs','css',
                   'html','json','yaml','yml','toml','xml','sh','bash','zsh','rb','php','swift',
                   'kt','vue','svelte','md','sql','graphql','prisma','proto'];
  const archive = ['zip','tar','gz','bz2','xz','7z','rar','dmg','iso','pkg','deb','rpm'];
  const pdf     = ['pdf'];
  const doc     = ['doc','docx','xls','xlsx','ppt','pptx','odt','ods','odp','rtf','csv','numbers','pages','key'];
  const font    = ['ttf','otf','woff','woff2','eot'];
  const data    = ['db','sqlite','sqlite3','parquet','npy','pkl'];
  if (images.includes(ext))  return '🖼️';
  if (video.includes(ext))   return '🎬';
  if (audio.includes(ext))   return '🎵';
  if (pdf.includes(ext))     return '📕';
  if (archive.includes(ext)) return '📦';
  if (code.includes(ext))    return '💻';
  if (doc.includes(ext))     return '📃';
  if (font.includes(ext))    return '🔤';
  if (data.includes(ext))    return '🗄️';
  return '📄';
}

/**
 * Format a millisecond timestamp as a locale-aware date+time string.
 * Returns '—' for falsy or zero values.
 * Pure function — no DOM or IPC dependencies.
 * @param {number} ms
 * @returns {string}
 */
function formatDate(ms) {
  if (!ms) return '—';
  return new Date(ms).toLocaleString(undefined, {
    year: 'numeric', month: 'short', day: 'numeric',
    hour: '2-digit', minute: '2-digit',
  });
}

/**
 * Human-readable label for a backup status string.
 * Pure function — no DOM or IPC dependencies.
 * @param {string|undefined} status
 * @returns {string}
 */
function formatBackupStatusLabel(status) {
  if (status === 'done')      return 'Backed up ✓';
  if (status === 'outdated')  return 'Changed since last backup';
  if (status === 'error')     return 'Backup failed ✗';
  if (status === 'uploading') return 'Uploading…';
  return 'Not yet backed up';
}

/**
 * Compute a backup result summary from an array of per-file upload outcomes.
 * Pure function — no DOM or IPC dependencies.
 *
 * @param {Array<{error: string|null, skipped: boolean}>} results
 * @returns {{ message: string, type: 'success'|'error' }|null}
 *   null when the array is empty (nothing to report).
 */
function buildBackupSummary(results) {
  var uploaded = 0, skipped = 0, failed = 0;
  for (var i = 0; i < results.length; i++) {
    var r = results[i];
    if (r.error) { failed++; }
    else if (r.skipped) { skipped++; }
    else { uploaded++; }
  }
  var parts = [];
  if (uploaded > 0) parts.push(uploaded + ' uploaded');
  if (skipped > 0) parts.push(skipped + ' unchanged');
  if (failed > 0) parts.push(failed + ' failed');
  if (parts.length === 0) return null;
  return { message: 'Backup: ' + parts.join(', '), type: failed > 0 ? 'error' : 'success' };
}

/**
 * #21 — Filter a tree's children to those whose name matches query (case-insensitive).
 * Folders that contain a matching descendant are included.
 * Pure function — no DOM or IPC dependencies.
 *
 * @param {Object} children  - map of name → { entry, children }
 * @param {string} query     - search string (empty = return all)
 * @returns {Object}         - filtered children map (same shape)
 */
function filterTree(children, query) {
  if (!query) return children;
  const q = query.toLowerCase();
  const result = {};
  for (const name of Object.keys(children)) {
    const node = children[name];
    const nameMatch = name.toLowerCase().includes(q);
    const filteredChildren = filterTree(node.children, query);
    const childMatch = Object.keys(filteredChildren).length > 0;
    if (nameMatch || childMatch) {
      result[name] = { entry: node.entry, children: filteredChildren };
    }
  }
  return result;
}

/**
 * #22 — Sort a children map's keys according to field and direction.
 * Directories always sort before files regardless of field.
 * Pure function — no DOM or IPC dependencies.
 *
 * @param {string[]}  keys      - array of names (keys of children)
 * @param {Object}    children  - map of name → { entry, children }
 * @param {'name'|'size'|'modified'} field
 * @param {'asc'|'desc'} dir
 * @returns {string[]}          - sorted array of keys
 */
function sortEntries(keys, children, field, dir) {
  return keys.slice().sort(function (a, b) {
    const aIsDir = children[a].entry ? children[a].entry.isDirectory : true;
    const bIsDir = children[b].entry ? children[b].entry.isDirectory : true;
    // Dirs always before files
    if (aIsDir !== bIsDir) return aIsDir ? -1 : 1;

    let cmp = 0;
    if (field === 'size') {
      const aSize = (!aIsDir && children[a].entry) ? (children[a].entry.size || 0) : 0;
      const bSize = (!bIsDir && children[b].entry) ? (children[b].entry.size || 0) : 0;
      cmp = aSize - bSize;
    } else if (field === 'modified') {
      const aMod = (!aIsDir && children[a].entry) ? (children[a].entry.modified || 0) : 0;
      const bMod = (!bIsDir && children[b].entry) ? (children[b].entry.modified || 0) : 0;
      cmp = aMod - bMod;
    } else {
      cmp = a.localeCompare(b);
    }
    return dir === 'desc' ? -cmp : cmp;
  });
}

// ---- Exports (for Jest) -------------------------------------------------

if (typeof module !== 'undefined') {
  module.exports = { buildBackupSummary, formatDate, formatBackupStatusLabel, fileTypeIcon, filterTree, sortEntries };
}

// ---- Browser / Electron UI ----------------------------------------------

(function () {

  if (typeof document === 'undefined' || typeof window === 'undefined' || window._testMode) return;

  const electronAPI = window.electronAPI;

  let currentFolderId = null;
  let currentPath = null;
  let _keyHandler = null;

  // #21/#22 — Search and sort state
  let _searchQuery = '';
  let _sortField = 'name';   // 'name' | 'size' | 'modified'
  let _sortDir   = 'asc';    // 'asc'  | 'desc'

  // #29 — Per-session skipped files (by relativePath). Resets on folder open.
  const skippedFiles = new Set();

  // Full flat entry list from the most recent disk read.
  let allEntries = [];

  // Drill-down navigation stack: [{ name: string, relativePath: string }, …]
  let viewStack = [];

  // Which relative-paths are currently expanded inline (persisted across refreshes).
  const expandedPaths = new Set();

  // Per-file backup status. Keys are relativePath strings.
  // Values: 'uploading' | 'done' | 'outdated' | 'error'
  const uploadStatus = new Map();

  // Backup records for files that exist in the cloud but not locally.
  // Keys are relativePath strings; values are the raw backup record objects from the server.
  const cloudOnlyFiles = new Map();

  // Full backup records from the server, keyed by relativePath.
  // Populated by loadBackupStatuses; used by the metadata modal.
  const backupRecords = new Map();

  // Register the live-watch listener once at module startup.
  electronAPI.onDirectoryChange(function () {
    if (currentPath) refreshDirectory(currentPath, { silent: true, skipUpload: true });
  });

  // ---- Public interface ---------------------------------------------------

  async function show(folderId, folderPath) {
    currentFolderId = folderId;
    currentPath = folderPath;
    viewStack = [];
    expandedPaths.clear();
    uploadStatus.clear();
    cloudOnlyFiles.clear();
    backupRecords.clear();
    skippedFiles.clear();
    _searchQuery = '';
    _sortField = 'name';
    _sortDir = 'asc';

    const el = document.getElementById('file-browser');
    el.classList.remove('hidden');
    renderScaffold(el);
    setBackupButtonEnabled(true);

    _keyHandler = function (e) {
      if (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA') return;
      if (e.key === 'b' || e.key === 'B') {
        e.preventDefault();
        const btn = document.getElementById('backup-now-btn');
        if (btn && !btn.disabled) btn.click();
      }
      // #21/#30 — '/' focuses search box
      if (e.key === '/') {
        e.preventDefault();
        const searchInput = document.getElementById('file-search-input');
        if (searchInput) searchInput.focus();
      }
    };
    document.addEventListener('keydown', _keyHandler);

    try {
      await electronAPI.watchDirectory(folderPath);
    } catch {
      // Live watching not supported on this platform — directory still loads below.
    }
    try {
      await refreshDirectory(folderPath, { silent: true });
      loadBackupStatuses();
    } catch {
      // Non-critical — directory may not exist yet.
    }
  }

  function hide() {
    if (_keyHandler) {
      document.removeEventListener('keydown', _keyHandler);
      _keyHandler = null;
    }
    const el = document.getElementById('file-browser');
    el.classList.add('hidden');
    el.innerHTML = '';
    currentFolderId = null;
    currentPath = null;
    allEntries = [];
    viewStack = [];
    expandedPaths.clear();
    uploadStatus.clear();
    cloudOnlyFiles.clear();
    backupRecords.clear();
    skippedFiles.clear();
    _searchQuery = '';
    _sortField = 'name';
    _sortDir = 'asc';
    closeMetadataModal();
    electronAPI.unwatchDirectory();
  }

  // ---- Backup status loading ----------------------------------------------

  async function loadBackupStatuses() {
    if (!currentPath || !currentFolderId) return;
    try {
      const resp = await window.API.getFolderBackups(currentFolderId);
      if (!resp.ok) return;
      const data = await resp.json();
      for (const b of data.backups) {
        backupRecords.set(b.relative_path, b);
        const result = await electronAPI.checksumFile(currentPath, b.relative_path);
        if (result.error) {
          cloudOnlyFiles.set(b.relative_path, b);
        } else {
          uploadStatus.set(
            b.relative_path,
            result.checksum === b.checksum_sha256 ? 'done' : 'outdated'
          );
        }
        renderView();
      }
    } catch (err) {
      // #28 — Improved UI feedback for backup status load failure
      console.warn('[Files] loadBackupStatuses failed:', err);
      window.UI.toast('Could not load backup status', 'error');
    }
  }

  // ---- Scaffold -----------------------------------------------------------

  function renderScaffold(el) {
    el.className = 'file-browser';
    el.innerHTML = `
      <div class="card">
        <div class="backup-progress-track"><div id="backup-progress-fill" class="backup-progress-fill"></div></div>
        <div class="file-browser-header">
          <div id="breadcrumb" class="breadcrumb-area">
            <h2>Local Folder</h2>
          </div>
          <div class="file-browser-actions">
            <span id="backup-progress" class="backup-progress" aria-live="polite"></span>
            <button id="backup-now-btn" disabled title="Backup now (B)">Backup Now</button>
            <button id="all-folders-btn">← All Folders</button>
          </div>
        </div>
        <div class="file-browser-controls">
          <div class="file-search-wrap">
            <input
              id="file-search-input"
              class="file-search-input"
              type="search"
              placeholder="Search files… (/)"
              autocomplete="off"
              aria-label="Filter files"
            />
            <button id="file-search-clear" class="file-search-clear hidden" type="button" aria-label="Clear search">×</button>
          </div>
          <select id="file-sort-select" class="file-sort-select" aria-label="Sort by">
            <option value="name">Name</option>
            <option value="size">Size</option>
            <option value="modified">Modified</option>
          </select>
          <button id="file-sort-dir-btn" class="file-sort-dir-btn" type="button" title="Toggle sort direction">↑ Asc</button>
        </div>
        <ul id="file-list" class="file-list"></ul>
      </div>
    `;
    document.getElementById('file-list').appendChild(buildSkeletonRows(6));
    document.getElementById('all-folders-btn').addEventListener('click', function () {
      hide();
      window.Dashboard.show();
    });
    document.getElementById('backup-now-btn').addEventListener('click', backupNow);

    // #21 — Search input handler
    const searchInput = document.getElementById('file-search-input');
    const clearBtn    = document.getElementById('file-search-clear');
    searchInput.addEventListener('input', function () {
      _searchQuery = searchInput.value;
      clearBtn.classList.toggle('hidden', !_searchQuery);
      renderView();
    });
    clearBtn.addEventListener('click', function () {
      searchInput.value = '';
      _searchQuery = '';
      clearBtn.classList.add('hidden');
      searchInput.focus();
      renderView();
    });

    // #22 — Sort controls
    const sortSelect = document.getElementById('file-sort-select');
    const sortDirBtn = document.getElementById('file-sort-dir-btn');
    sortSelect.value = _sortField;
    sortDirBtn.textContent = _sortDir === 'asc' ? '↑ Asc' : '↓ Desc';
    sortSelect.addEventListener('change', function () {
      _sortField = sortSelect.value;
      renderView();
    });
    sortDirBtn.addEventListener('click', function () {
      _sortDir = _sortDir === 'asc' ? 'desc' : 'asc';
      sortDirBtn.textContent = _sortDir === 'asc' ? '↑ Asc' : '↓ Desc';
      renderView();
    });
  }

  function buildSkeletonRows(count) {
    const widths = [55, 70, 45, 80, 60, 50];
    const rows = [];
    for (let i = 0; i < count; i++) {
      rows.push({ w: widths[i % widths.length] });
    }

    const ul = document.createDocumentFragment();
    for (const { w } of rows) {
      const li = document.createElement('li');
      li.className = 'skeleton-row';
      const icon = document.createElement('span');
      icon.className = 'skeleton skeleton-icon';
      const line = document.createElement('span');
      line.className = 'skeleton skeleton-line';
      line.style.maxWidth = w + '%';
      const short = document.createElement('span');
      short.className = 'skeleton skeleton-short skeleton-right';
      li.appendChild(icon); li.appendChild(line); li.appendChild(short);
      ul.appendChild(li);
    }
    return ul;
  }

  function setBackupButtonEnabled(enabled) {
    const btn = document.getElementById('backup-now-btn');
    if (btn) btn.disabled = !enabled;
  }

  // ---- Backup -------------------------------------------------------------

  async function backupNow() {
    if (!currentPath || !currentFolderId) return;

    const btn = document.getElementById('backup-now-btn');
    if (btn) { btn.disabled = true; btn.textContent = 'Backing up…'; }

    let entries;
    try {
      entries = await electronAPI.readDirectory(currentPath);
    } catch {
      window.UI.toast('Could not read folder', 'error');
      if (btn) { btn.disabled = false; btn.textContent = 'Backup Now'; }
      return;
    }

    allEntries = entries;
    renderView();

    const fileEntries = entries.filter(function (e) { return !e.isDirectory; });
    if (fileEntries.length === 0) {
      window.UI.toast('No files to back up');
    } else {
      await uploadFiles(currentPath, fileEntries);
    }

    if (btn) { btn.disabled = false; btn.textContent = 'Backup Now'; }
  }

  /**
   * Read the full directory tree from disk, cache entries, sync metadata to backend,
   * then re-render the current view.
   *
   * @param {string} dirPath
   * @param {{ silent?: boolean }} [opts]
   */
  async function refreshDirectory(dirPath, opts) {
    let entries;
    try {
      entries = await electronAPI.readDirectory(dirPath);
    } catch {
      const listEl = document.getElementById('file-list');
      if (listEl) listEl.innerHTML = '<li class="file-item file-empty">Could not read directory.</li>';
      return;
    }

    allEntries = entries;
    renderView();

    const files = entries.map(function (e) {
      return {
        name: e.name,
        relative_path: e.relativePath,
        is_directory: e.isDirectory,
        size: e.size || 0,
        modified_ms: Math.round(e.modified || 0),
      };
    });

    try {
      const resp = await window.API.syncFolderFiles(currentFolderId, files);
      if (resp.ok && !opts?.silent) {
        window.UI.toast('File list synced (' + files.length + ' item' + (files.length === 1 ? '' : 's') + ')');
      }
    } catch {
      // Sync failures are non-fatal.
    }
  }

  /**
   * Upload file content for each entry to the backup endpoint for the current folder.
   *
   * @param {string} rootPath
   * @param {Array} fileEntries
   */
  async function uploadFiles(rootPath, fileEntries) {
    if (fileEntries.length === 0) return;

    const uploadBase = window.APIClient.BASE_URL + '/api/folders/' + currentFolderId + '/backup';
    const accessToken = window.TokenStore.getAccessToken();
    const results = [];
    const total = fileEntries.length;
    let done = 0;
    let failed = 0;

    function updateProgress() {
      const textEl = document.getElementById('backup-progress');
      const fillEl = document.getElementById('backup-progress-fill');
      const pct    = total > 0 ? Math.round((done / total) * 100) : 0;

      if (fillEl) {
        fillEl.style.width = pct + '%';
        fillEl.className = 'backup-progress-fill' +
          (done === total && failed > 0 ? ' has-errors' : done === total ? ' done' : '');
      }

      if (!textEl) return;
      if (done < total) {
        textEl.className = 'backup-progress in-progress';
        textEl.textContent = done + ' / ' + total + ' files';
      } else if (failed > 0) {
        textEl.className = 'backup-progress has-errors';
        textEl.textContent = done + ' / ' + total + ' — ' + failed + ' failed';
      } else {
        textEl.className = 'backup-progress done';
        textEl.textContent = total + ' / ' + total + ' ✓';
        setTimeout(function () {
          const t = document.getElementById('backup-progress');
          const f = document.getElementById('backup-progress-fill');
          if (t) { t.textContent = ''; t.className = 'backup-progress'; }
          if (f) { f.style.width = '0%'; f.className = 'backup-progress-fill'; }
        }, 4000);
      }
    }

    updateProgress();

    for (const entry of fileEntries) {
      // #29 — Skip files the user has excluded
      if (skippedFiles.has(entry.relativePath)) {
        results.push({ error: null, skipped: true });
        done++;
        updateProgress();
        continue;
      }

      uploadStatus.set(entry.relativePath, 'uploading');
      renderView();

      let result;
      try {
        result = await electronAPI.uploadFile(
          rootPath,
          entry.relativePath,
          uploadBase,
          accessToken
        );
        if (result.error) {
          console.warn('Backup upload failed for', entry.relativePath + ':', result.error);
        }
      } catch (err) {
        console.warn('Backup upload failed for', entry.relativePath + ':', err.message);
        result = { error: err.message, skipped: false };
      }

      uploadStatus.set(entry.relativePath, result.error ? 'error' : 'done');
      results.push({ error: result.error || null, skipped: !!result.skipped });
      if (result.error) failed++;
      done++;
      updateProgress();
      renderView();
    }

    const summary = buildBackupSummary(results);
    if (summary) window.UI.toast(summary.message, summary.type);
  }

  // ---- View rendering -----------------------------------------------------

  function renderView() {
    renderBreadcrumb();

    const listEl = document.getElementById('file-list');
    if (!listEl) return;

    // #28 — Error boundary
    try {
      _renderViewContent(listEl);
    } catch (err) {
      console.error('[Files] renderView error:', err);
      listEl.innerHTML = '';
      const errorLi = document.createElement('li');
      errorLi.className = 'file-item file-empty';
      const errorDiv = document.createElement('div');
      errorDiv.className = 'file-list-error';
      const icon = document.createElement('div');
      icon.className = 'file-list-error-icon';
      icon.textContent = '⚠️';
      const msg = document.createElement('div');
      msg.className = 'file-list-error-msg';
      msg.textContent = 'Something went wrong rendering the file list';
      const retryBtn = document.createElement('button');
      retryBtn.textContent = 'Retry';
      retryBtn.addEventListener('click', function () {
        if (currentPath) refreshDirectory(currentPath, { silent: true });
      });
      errorDiv.appendChild(icon);
      errorDiv.appendChild(msg);
      errorDiv.appendChild(retryBtn);
      errorLi.appendChild(errorDiv);
      listEl.appendChild(errorLi);
    }
  }

  function _renderViewContent(listEl) {
    const combined = allEntries.slice();
    for (const [relPath, b] of cloudOnlyFiles) {
      if (!combined.some(function (e) { return e.relativePath === relPath; })) {
        const parts = relPath.split('/');
        combined.push({
          name: parts[parts.length - 1],
          relativePath: relPath,
          isDirectory: false,
          size: b.size,
          modified: null,
          isCloudOnly: true,
        });
      }
    }

    if (combined.length === 0) {
      listEl.innerHTML = `
        <li class="file-item file-empty file-item-empty-wrap">
          <div class="empty-state empty-state--compact">
            <div class="empty-state-icon">📂</div>
            <h3 class="empty-state-title">Empty folder</h3>
            <p class="empty-state-body">No files here yet.</p>
          </div>
        </li>`;
      return;
    }

    const tree = buildTree(combined);
    const viewNode = resolveViewNode(tree);

    // #21 — Apply search filter
    const filteredChildren = filterTree(viewNode.children, _searchQuery);

    listEl.innerHTML = '';
    if (Object.keys(viewNode.children).length === 0) {
      const li = document.createElement('li');
      li.className = 'file-item file-empty file-item-empty-wrap';
      const wrap = document.createElement('div');
      wrap.className = 'empty-state empty-state--compact';
      wrap.innerHTML = '<div class="empty-state-icon">📂</div><h3 class="empty-state-title">Empty folder</h3><p class="empty-state-body">No files here yet.</p>';
      li.appendChild(wrap);
      listEl.appendChild(li);
    } else if (_searchQuery && Object.keys(filteredChildren).length === 0) {
      // Search with no results
      const li = document.createElement('li');
      li.className = 'file-item file-empty';
      const msg = document.createElement('span');
      msg.className = 'file-search-empty';
      msg.textContent = 'No files match "' + _searchQuery + '"';
      li.appendChild(msg);
      listEl.appendChild(li);
    } else {
      renderTree(listEl, filteredChildren);
    }
  }

  function renderBreadcrumb() {
    const el = document.getElementById('breadcrumb');
    if (!el) return;
    el.innerHTML = '';

    if (!currentPath) {
      const h2 = document.createElement('h2');
      h2.textContent = 'Local Folder';
      el.appendChild(h2);
      return;
    }

    const rootName = currentPath.replace(/\\/g, '/').split('/').filter(Boolean).pop() || 'Root';

    const nav = document.createElement('nav');
    nav.className = 'breadcrumb';
    nav.setAttribute('aria-label', 'Folder navigation');

    if (viewStack.length > 0) {
      const back = document.createElement('button');
      back.className = 'breadcrumb-back';
      back.setAttribute('aria-label', 'Go up one level');
      back.textContent = '← Back';
      back.addEventListener('click', function () { navigateTo(viewStack.length - 1); });
      nav.appendChild(back);
    }

    const rootBtn = document.createElement('button');
    rootBtn.className = 'breadcrumb-seg' + (viewStack.length === 0 ? ' active' : '');
    rootBtn.setAttribute('title', currentPath);
    rootBtn.innerHTML = '📁 ' + truncateName(rootName, 22);
    rootBtn.addEventListener('click', function () { navigateTo(0); });
    nav.appendChild(rootBtn);

    viewStack.forEach(function (seg, i) {
      const sep = document.createElement('span');
      sep.className = 'breadcrumb-sep';
      sep.setAttribute('aria-hidden', 'true');
      sep.textContent = '›';
      nav.appendChild(sep);

      const segBtn = document.createElement('button');
      const isLast = i === viewStack.length - 1;
      segBtn.className = 'breadcrumb-seg' + (isLast ? ' active' : '');
      segBtn.setAttribute('title', seg.relativePath);
      segBtn.innerHTML = truncateName(seg.name, 22);
      const depth = i + 1;
      segBtn.addEventListener('click', function () { navigateTo(depth); });
      nav.appendChild(segBtn);
    });

    el.appendChild(nav);
  }

  // ---- Navigation ---------------------------------------------------------

  function navigateInto(entry) {
    const parts = entry.relativePath.split('/');
    viewStack = [];
    let relPath = '';
    for (const part of parts) {
      relPath = relPath ? relPath + '/' + part : part;
      viewStack.push({ name: part, relativePath: relPath });
    }
    renderView();
  }

  function navigateTo(depth) {
    viewStack = viewStack.slice(0, depth);
    renderView();
  }

  function resolveViewNode(tree) {
    let node = tree;
    for (const seg of viewStack) {
      if (node.children[seg.name]) {
        node = node.children[seg.name];
      } else {
        return tree;
      }
    }
    return node;
  }

  // ---- Tree building ------------------------------------------------------

  function buildTree(entries) {
    const root = { children: {} };
    for (const e of entries) {
      const parts = e.relativePath.split('/');
      let node = root;
      for (let i = 0; i < parts.length; i++) {
        const part = parts[i];
        if (!node.children[part]) {
          node.children[part] = { entry: null, children: {} };
        }
        if (i === parts.length - 1) {
          node.children[part].entry = e;
        } else {
          node = node.children[part];
        }
      }
    }
    return root;
  }

  // ---- Tree rendering -----------------------------------------------------

  function renderTree(ul, children) {
    // #22 — sort using pure sortEntries function
    const keys = sortEntries(Object.keys(children), children, _sortField, _sortDir);

    for (const name of keys) {
      const node = children[name];
      const entry = node.entry;
      const isDir = entry ? entry.isDirectory : Object.keys(node.children).length > 0;
      const relPath = entry ? entry.relativePath : name;

      const status = uploadStatus.get(relPath);
      const isSkipped = !isDir && skippedFiles.has(relPath);
      const li = document.createElement('li');
      li.className = 'file-item' +
        (isDir ? ' is-dir' : '') +
        (!isDir && status === 'outdated' ? ' outdated-file' : '') +
        (isSkipped ? ' skipped-file' : '');

      if (isDir) {
        const isOpen = expandedPaths.has(relPath);

        const toggle = document.createElement('button');
        toggle.className = 'tree-toggle';
        toggle.textContent = isOpen ? '▼' : '▶';
        toggle.setAttribute('aria-label', isOpen ? 'Collapse' : 'Expand');

        const icon = document.createElement('span');
        icon.className = 'file-icon';
        icon.textContent = '📁';

        const nameEl = document.createElement('span');
        nameEl.className = 'file-name folder-link';
        nameEl.setAttribute('title', name + ' — click to open');
        nameEl.innerHTML = truncateName(name, 40);
        nameEl.addEventListener('click', function () {
          if (entry) navigateInto(entry);
        });

        li.appendChild(toggle);
        li.appendChild(icon);
        li.appendChild(nameEl);

        if (entry) {
          const infoBtn = buildInfoButton(entry);
          infoBtn.style.marginLeft = 'auto';
          li.appendChild(infoBtn);
        }

        const childUl = document.createElement('ul');
        childUl.className = 'tree-children' + (isOpen ? '' : ' hidden');
        renderTree(childUl, node.children);

        toggle.addEventListener('click', function () {
          const open = !childUl.classList.contains('hidden');
          childUl.classList.toggle('hidden', open);
          toggle.textContent = open ? '▶' : '▼';
          toggle.setAttribute('aria-label', open ? 'Expand' : 'Collapse');
          if (open) expandedPaths.delete(relPath);
          else expandedPaths.add(relPath);
        });

        ul.appendChild(li);
        ul.appendChild(childUl);

      } else {
        const isCloudOnly = entry && entry.isCloudOnly;
        if (isCloudOnly) li.classList.add('cloud-only');

        const icon = document.createElement('span');
        icon.className = 'file-icon';
        icon.textContent = fileTypeIcon(name);

        const nameEl = document.createElement('span');
        nameEl.className = 'file-name' + (isCloudOnly ? '' : ' file-link');
        nameEl.setAttribute('title', isCloudOnly ? name + ' — cloud only' : name + ' — click to open');
        nameEl.innerHTML = truncateName(name, 40);
        if (!isCloudOnly && entry) {
          nameEl.setAttribute('role', 'button');
          nameEl.setAttribute('tabindex', '0');
          function doOpen() {
            electronAPI.openFile(currentPath, entry.relativePath).then(function (result) {
              if (result && result.error) window.UI.toast('Could not open file: ' + result.error, 'error');
            }).catch(function () {
              window.UI.toast('Could not open file', 'error');
            });
          }
          nameEl.addEventListener('click', doOpen);
          nameEl.addEventListener('keydown', function (e) {
            if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); doOpen(); }
          });
        }

        const meta = document.createElement('span');
        meta.className = 'file-meta';
        meta.textContent = entry ? formatSize(entry.size) : '';

        li.appendChild(icon);
        li.appendChild(nameEl);
        li.appendChild(meta);

        if (isCloudOnly) {
          const dlBtn = document.createElement('button');
          dlBtn.className = 'cloud-download-btn';
          dlBtn.textContent = '☁';
          dlBtn.setAttribute('title', 'Download from cloud');
          dlBtn.addEventListener('click', function () { downloadCloudFile(relPath); });
          li.appendChild(dlBtn);
        } else {
          const statusEl = document.createElement('span');
          if (status === 'uploading') {
            statusEl.className = 'backup-status uploading';
            statusEl.setAttribute('aria-label', 'Uploading…');
          } else if (status === 'done') {
            statusEl.className = 'backup-status done';
            statusEl.textContent = '✓';
            statusEl.setAttribute('aria-label', 'Backed up');
          } else if (status === 'outdated') {
            statusEl.className = 'backup-status outdated';
            statusEl.textContent = '↑';
            statusEl.setAttribute('aria-label', 'Local file has changed — backup outdated');
          } else if (status === 'error') {
            statusEl.className = 'backup-status error';
            statusEl.textContent = '✗';
            statusEl.setAttribute('aria-label', 'Backup failed');
          } else {
            statusEl.className = 'backup-status local-only';
            statusEl.textContent = '○';
            statusEl.setAttribute('aria-label', 'Not backed up');
          }
          const slot = document.createElement('button');
          slot.className = 'icon-slot';
          slot.setAttribute('type', 'button');
          slot.setAttribute('tabindex', '-1');
          slot.appendChild(statusEl);
          li.appendChild(slot);

          const archiveBtn = document.createElement('button');
          archiveBtn.className = 'cloud-archive-btn';
          archiveBtn.textContent = '☁';
          archiveBtn.setAttribute('title', 'Archive to cloud and remove local copy');
          archiveBtn.addEventListener('click', function () { moveToCloudOnly(entry); });
          li.appendChild(archiveBtn);
        }

        li.appendChild(buildInfoButton(entry));

        // #29 — Right-click context menu for skip toggle (non-cloud-only files only)
        if (!isCloudOnly && entry) {
          li.addEventListener('contextmenu', function (e) {
            e.preventDefault();
            showFileContextMenu(e.clientX, e.clientY, entry, li);
          });
        }

        ul.appendChild(li);
      }
    }
  }

  // ---- #29 — Context menu -------------------------------------------------

  function showFileContextMenu(x, y, entry, li) {
    // Remove any existing context menu
    const existing = document.getElementById('file-context-menu');
    if (existing) existing.remove();

    const isSkipped = skippedFiles.has(entry.relativePath);
    const menu = document.createElement('div');
    menu.id = 'file-context-menu';
    menu.className = 'context-menu';
    menu.style.left = x + 'px';
    menu.style.top  = y + 'px';

    function closeMenu() {
      menu.remove();
      document.removeEventListener('mousedown', dismiss, true);
      document.removeEventListener('keydown', dismissKey, true);
    }

    const item = document.createElement('button');
    item.className = 'context-menu-item';
    item.textContent = isSkipped ? 'Include in backups' : 'Skip this file in backups';
    item.addEventListener('click', function () {
      closeMenu();
      if (isSkipped) {
        skippedFiles.delete(entry.relativePath);
      } else {
        skippedFiles.add(entry.relativePath);
      }
      li.classList.toggle('skipped-file', !isSkipped);
    });

    menu.appendChild(item);
    document.body.appendChild(menu);

    // Position correction if off-screen
    const rect = menu.getBoundingClientRect();
    if (rect.right > window.innerWidth) {
      menu.style.left = (x - rect.width) + 'px';
    }
    if (rect.bottom > window.innerHeight) {
      menu.style.top = (y - rect.height) + 'px';
    }

    function dismiss(e) {
      if (!menu.contains(e.target)) closeMenu();
    }
    function dismissKey(e) {
      if (e.key === 'Escape') closeMenu();
    }
    document.addEventListener('mousedown', dismiss, true);
    document.addEventListener('keydown', dismissKey, true);
  }

  // ---- Cloud archive (local → cloud only) ---------------------------------

  async function moveToCloudOnly(entry) {
    closeMetadataModal();

    // Upload first if not already current.
    const status = uploadStatus.get(entry.relativePath);
    if (status !== 'done') {
      uploadStatus.set(entry.relativePath, 'uploading');
      renderView();

      const uploadBase = window.APIClient.BASE_URL + '/api/folders/' + currentFolderId + '/backup';
      const accessToken = window.TokenStore.getAccessToken();
      const result = await electronAPI.uploadFile(
        currentPath, entry.relativePath, uploadBase, accessToken
      );

      if (result.error) {
        uploadStatus.delete(entry.relativePath);
        renderView();
        window.UI.toast('Upload failed: ' + result.error, 'error');
        return;
      }
      uploadStatus.set(entry.relativePath, 'done');
      renderView();
    }

    // Delete the local copy.
    const delResult = await electronAPI.deleteFile(currentPath, entry.relativePath);
    if (delResult.error) {
      window.UI.toast('Could not remove local file: ' + delResult.error, 'error');
      return;
    }

    // Promote to cloud-only in UI state.
    const rec = backupRecords.get(entry.relativePath) ||
      { relative_path: entry.relativePath, size: entry.size };
    cloudOnlyFiles.set(entry.relativePath, rec);
    allEntries = allEntries.filter(function (e) { return e.relativePath !== entry.relativePath; });
    uploadStatus.delete(entry.relativePath);
    renderView();

    window.UI.toast('Archived to cloud: ' + entry.name, 'success');
  }

  // ---- Cloud download -----------------------------------------------------

  async function downloadCloudFile(relativePath) {
    if (!currentPath || !currentFolderId) return;
    try {
      const resp = await window.API.downloadFromFolder(currentFolderId, relativePath);
      if (!resp.ok) {
        window.UI.toast('Download failed: HTTP ' + resp.status, 'error');
        return;
      }
      const buffer = await resp.arrayBuffer();
      const result = await electronAPI.saveFile(currentPath, relativePath, buffer);
      if (result.error) {
        window.UI.toast('Could not save file: ' + result.error, 'error');
        return;
      }
      cloudOnlyFiles.delete(relativePath);
      uploadStatus.set(relativePath, 'done');
      await refreshDirectory(currentPath, { silent: true });
      window.UI.toast('Downloaded: ' + relativePath.split('/').pop(), 'success');
    } catch (e) {
      window.UI.toast('Download error: ' + e.message, 'error');
    }
  }

  // ---- Helpers ------------------------------------------------------------

  function escapeHtml(str) {
    return String(str)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;')
      .replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  }

  function buildInfoButton(entry) {
    const btn = document.createElement('button');
    btn.className = 'file-info-btn';
    btn.setAttribute('type', 'button');
    btn.setAttribute('title', 'View details');
    btn.setAttribute('aria-label', 'View details');
    btn.textContent = 'ⓘ';
    btn.addEventListener('click', function (e) {
      e.stopPropagation();
      openMetadataModal(entry);
    });
    return btn;
  }

  function openMetadataModal(entry) {
    closeMetadataModal();

    const sep = '/';
    const absPath = currentPath + sep + entry.relativePath;
    const parentDir = entry.relativePath.includes('/')
      ? currentPath + sep + entry.relativePath.split('/').slice(0, -1).join('/')
      : currentPath;

    const rows = [];
    rows.push(['Type', entry.isDirectory ? 'Folder' : 'File']);
    rows.push(['Full path', absPath]);
    if (!entry.isDirectory) {
      rows.push(['Size', formatSize(entry.size)]);
    }
    if (entry.modified) rows.push(['Modified', formatDate(entry.modified)]);
    if (entry.created && entry.created > 0) rows.push(['Created', formatDate(entry.created)]);

    // Full (untruncated) values for copy-to-clipboard. Keyed by label.
    const copyValues = { 'Full path': absPath };

    if (!entry.isDirectory) {
      if (entry.isCloudOnly) {
        rows.push(['Backup', 'Cloud only — not present locally']);
      } else {
        rows.push(['Backup', formatBackupStatusLabel(uploadStatus.get(entry.relativePath))]);
      }
      const rec = backupRecords.get(entry.relativePath);
      if (rec) {
        if (rec.last_backed_up_at) {
          rows.push(['Last backed up', formatDate(new Date(rec.last_backed_up_at).getTime())]);
        }
        if (rec.checksum_sha256) {
          const h = rec.checksum_sha256;
          rows.push(['SHA-256', h.slice(0, 8) + '…' + h.slice(-8)]);
          copyValues['SHA-256'] = h;
        }
      }
    }

    const icon = entry.isDirectory ? '📁' : '📄';

    const overlay = document.createElement('div');
    overlay.id = 'metadata-modal';
    overlay.className = 'modal-overlay';
    overlay.addEventListener('click', function (e) {
      if (e.target === overlay) closeMetadataModal();
    });

    const card = document.createElement('div');
    card.className = 'modal-card';
    card.setAttribute('role', 'dialog');
    card.setAttribute('aria-modal', 'true');

    const header = document.createElement('div');
    header.className = 'modal-header';
    header.innerHTML = '<span class="modal-title-icon">' + icon + '</span>';

    const titleEl = document.createElement('h3');
    titleEl.className = 'modal-title';
    titleEl.textContent = entry.name;
    header.appendChild(titleEl);

    const closeBtn = document.createElement('button');
    closeBtn.className = 'modal-close';
    closeBtn.setAttribute('aria-label', 'Close');
    closeBtn.textContent = '×';
    closeBtn.addEventListener('click', closeMetadataModal);
    header.appendChild(closeBtn);

    const body = document.createElement('div');
    body.className = 'modal-body';

    const copyableLabels = new Set(['Full path', 'SHA-256']);

    const list = document.createElement('dl');
    list.className = 'meta-list';
    for (const [label, value] of rows) {
      const dt = document.createElement('dt');
      dt.className = 'meta-label';
      dt.textContent = label;
      const dd = document.createElement('dd');
      dd.className = 'meta-value';
      dd.textContent = value;
      if (copyableLabels.has(label) && navigator.clipboard) {
        const copyBtn = document.createElement('button');
        copyBtn.className = 'meta-copy-btn';
        copyBtn.textContent = 'Copy';
        copyBtn.setAttribute('aria-label', 'Copy ' + label);
        const valueToCopy = copyValues[label] || value;
        copyBtn.addEventListener('click', function () {
          navigator.clipboard.writeText(valueToCopy).then(function () {
            copyBtn.textContent = 'Copied!';
            copyBtn.classList.add('copied');
            setTimeout(function () {
              copyBtn.textContent = 'Copy';
              copyBtn.classList.remove('copied');
            }, 2000);
          }).catch(function () {});
        });
        dd.appendChild(copyBtn);
      }
      list.appendChild(dt);
      list.appendChild(dd);
    }
    body.appendChild(list);

    // #27 — Backup history section (files only, uses already-loaded backupRecords)
    if (!entry.isDirectory) {
      const histSection = document.createElement('div');
      histSection.className = 'backup-history-section';

      const histHeading = document.createElement('div');
      histHeading.className = 'backup-history-heading';
      histHeading.textContent = 'Backup history';
      histSection.appendChild(histHeading);

      // backupRecords is keyed by relativePath — collect all versions for this file
      // The current API stores one record per file. If there are multiple versions
      // (e.g. from repeated backups), they would be separate entries. Here we
      // collect the single record for this path from the already-loaded map.
      const rec = backupRecords.get(entry.relativePath);
      const histEntries = rec ? [rec] : [];

      if (histEntries.length === 0) {
        const emptyEl = document.createElement('div');
        emptyEl.className = 'backup-history-empty';
        emptyEl.textContent = 'No backup history';
        histSection.appendChild(emptyEl);
      } else {
        const ul = document.createElement('ul');
        ul.className = 'backup-history-list';
        for (const h of histEntries) {
          const li = document.createElement('li');
          li.className = 'backup-history-item';
          const dateEl = document.createElement('span');
          dateEl.className = 'backup-history-item-date';
          dateEl.textContent = h.last_backed_up_at
            ? formatDate(new Date(h.last_backed_up_at).getTime())
            : '—';
          const sizeEl = document.createElement('span');
          sizeEl.textContent = formatSize(h.size || 0);
          li.appendChild(dateEl);
          li.appendChild(sizeEl);
          ul.appendChild(li);
        }
        histSection.appendChild(ul);
      }
      body.appendChild(histSection);
    }

    card.appendChild(header);
    card.appendChild(body);

    // Action buttons — only for files, not directories.
    if (!entry.isDirectory) {
      const actions = document.createElement('div');
      actions.className = 'modal-actions';

      if (entry.isCloudOnly) {
        const dlBtn = document.createElement('button');
        dlBtn.className = 'modal-action-btn modal-action-download';
        dlBtn.textContent = '↓  Download to Local';
        dlBtn.addEventListener('click', function () {
          closeMetadataModal();
          downloadCloudFile(entry.relativePath);
        });
        actions.appendChild(dlBtn);
      } else {
        const archiveBtn = document.createElement('button');
        archiveBtn.className = 'modal-action-btn modal-action-archive';
        archiveBtn.textContent = '☁  Move to Cloud Only';
        archiveBtn.setAttribute('title', 'Upload if needed, then delete the local copy');
        archiveBtn.addEventListener('click', function () { moveToCloudOnly(entry); });
        actions.appendChild(archiveBtn);
      }

      card.appendChild(actions);
    }

    overlay.appendChild(card);
    document.body.appendChild(overlay);

    overlay._keyHandler = function (e) {
      if (e.key === 'Escape') { closeMetadataModal(); return; }
      if (e.key === 'Tab') {
        const focusable = Array.from(card.querySelectorAll(
          'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
        )).filter(function (el) { return !el.disabled; });
        if (focusable.length === 0) { e.preventDefault(); return; }
        const first = focusable[0];
        const last  = focusable[focusable.length - 1];
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault(); last.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault(); first.focus();
        }
      }
    };
    document.addEventListener('keydown', overlay._keyHandler);
    // Move focus into the modal
    requestAnimationFrame(function () { closeBtn.focus(); });

    requestAnimationFrame(function () { overlay.classList.add('modal-visible'); });
  }

  function closeMetadataModal() {
    const modal = document.getElementById('metadata-modal');
    if (!modal) return;
    if (modal._keyHandler) document.removeEventListener('keydown', modal._keyHandler);
    modal.remove();
  }

  function truncateName(name, max) {
    if (name.length <= max) return escapeHtml(name);
    const half = Math.floor((max - 1) / 2);
    return escapeHtml(name.slice(0, half)) + '…' + escapeHtml(name.slice(-half));
  }

  function formatSize(bytes) {
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
    if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
    return (bytes / (1024 * 1024 * 1024)).toFixed(1) + ' GB';
  }

  // ---- Expose -------------------------------------------------------------

  window.Files = { show, hide };

})();
