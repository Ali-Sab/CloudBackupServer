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

// ---- Exports (for Jest) -------------------------------------------------

if (typeof module !== 'undefined') {
  module.exports = { buildBackupSummary, formatDate, formatBackupStatusLabel };
}

// ---- Browser / Electron UI ----------------------------------------------

(function () {

  if (typeof document === 'undefined' || typeof window === 'undefined' || window._testMode) return;

  const electronAPI = window.electronAPI;

  let currentFolderId = null;
  let currentPath = null;

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

    const el = document.getElementById('file-browser');
    el.classList.remove('hidden');
    renderScaffold(el);
    setBackupButtonEnabled(true);

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
    } catch {
      // Non-critical — status icons just won't show for existing backups.
    }
  }

  // ---- Scaffold -----------------------------------------------------------

  function renderScaffold(el) {
    el.className = 'file-browser';
    el.innerHTML = `
      <div class="card">
        <div class="file-browser-header">
          <div id="breadcrumb" class="breadcrumb-area">
            <h2>Local Folder</h2>
          </div>
          <div class="file-browser-actions">
            <button id="backup-now-btn" disabled>Backup Now</button>
            <button id="all-folders-btn">← All Folders</button>
          </div>
        </div>
        <ul id="file-list" class="file-list">
          <li class="file-item file-empty">Loading…</li>
        </ul>
      </div>
    `;
    document.getElementById('all-folders-btn').addEventListener('click', function () {
      hide();
      window.Dashboard.show();
    });
    document.getElementById('backup-now-btn').addEventListener('click', backupNow);
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

    for (const entry of fileEntries) {
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
      renderView();

      results.push({ error: result.error || null, skipped: !!result.skipped });
    }

    const summary = buildBackupSummary(results);
    if (summary) window.UI.toast(summary.message, summary.type);
  }

  // ---- View rendering -----------------------------------------------------

  function renderView() {
    renderBreadcrumb();

    const listEl = document.getElementById('file-list');
    if (!listEl) return;

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
      listEl.innerHTML = '<li class="file-item file-empty">Folder is empty</li>';
      return;
    }

    const tree = buildTree(combined);
    const viewNode = resolveViewNode(tree);

    listEl.innerHTML = '';
    if (Object.keys(viewNode.children).length === 0) {
      listEl.innerHTML = '<li class="file-item file-empty">Folder is empty</li>';
    } else {
      renderTree(listEl, viewNode.children);
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
    const keys = Object.keys(children).sort(function (a, b) {
      const aIsDir = children[a].entry ? children[a].entry.isDirectory : true;
      const bIsDir = children[b].entry ? children[b].entry.isDirectory : true;
      if (aIsDir !== bIsDir) return aIsDir ? -1 : 1;
      return a.localeCompare(b);
    });

    for (const name of keys) {
      const node = children[name];
      const entry = node.entry;
      const isDir = entry ? entry.isDirectory : Object.keys(node.children).length > 0;
      const relPath = entry ? entry.relativePath : name;

      const li = document.createElement('li');
      li.className = 'file-item' + (isDir ? ' is-dir' : '');

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
        icon.textContent = '📄';

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
          const status = uploadStatus.get(relPath);
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

        ul.appendChild(li);
      }
    }
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

    const list = document.createElement('dl');
    list.className = 'meta-list';
    for (const [label, value] of rows) {
      const dt = document.createElement('dt');
      dt.className = 'meta-label';
      dt.textContent = label;
      const dd = document.createElement('dd');
      dd.className = 'meta-value';
      dd.textContent = value;
      list.appendChild(dt);
      list.appendChild(dd);
    }
    body.appendChild(list);

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

    overlay._keyHandler = function (e) { if (e.key === 'Escape') closeMetadataModal(); };
    document.addEventListener('keydown', overlay._keyHandler);

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
