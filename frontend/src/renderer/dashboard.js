/**
 * Multi-folder dashboard — shows all watched folders with stats.
 *
 * Depends on:
 *   window.electronAPI  (selectDirectory)
 *   window.API          (getFolders, addFolder, removeFolder)
 *   window.Files        (show)
 *   window.UI           (toast)
 *
 * Exposes: window.Dashboard = { show, hide }
 */

'use strict';

/**
 * Compute folder health status from the last backup timestamp.
 * Pure function — no DOM or IPC dependencies.
 * @param {string|null} lastBackedUpAt  ISO date string or null
 * @returns {'green'|'amber'|'red'}
 */
function folderHealthStatus(lastBackedUpAt) {
  if (!lastBackedUpAt) return 'red';
  const ageMs = Date.now() - new Date(lastBackedUpAt).getTime();
  if (ageMs < 24 * 60 * 60 * 1000) return 'green';
  return 'amber';
}

/**
 * Compute backup freshness as a 5–100 percentage for the progress bar.
 * Returns 0 for null input (never backed up), 5 (minimum visible fill) for > 7 days old,
 * and up to 100 for a backup within the last minute.
 * Pure function — no DOM or IPC dependencies.
 * @param {string|null} lastBackedUpAt ISO date string or null
 * @returns {number}
 */
function folderFreshnessPercent(lastBackedUpAt) {
  if (!lastBackedUpAt) return 0;
  const sevenDaysMs = 7 * 24 * 60 * 60 * 1000;
  const ageMs = Date.now() - new Date(lastBackedUpAt).getTime();
  if (ageMs >= sevenDaysMs) return 5;
  return Math.round(100 - (ageMs / sevenDaysMs) * 95);
}

if (typeof module !== 'undefined') {
  module.exports = { folderHealthStatus, folderFreshnessPercent };
}

(function () {

  if (typeof document === 'undefined' || typeof window === 'undefined') return;

  const electronAPI = window.electronAPI;

  // Keyboard shortcut handler — stored so it can be removed on hide().
  let _keyHandler = null;

  // ---- Public interface ---------------------------------------------------

  async function show() {
    const el = document.getElementById('dashboard');
    el.classList.remove('hidden');
    renderScaffold(el);
    _keyHandler = function (e) {
      if (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA') return;
      if (e.key === 'a' || e.key === 'A') {
        e.preventDefault();
        handleAddFolder();
      }
    };
    document.addEventListener('keydown', _keyHandler);
    await loadAndRenderFolders();
  }

  function hide() {
    if (_keyHandler) {
      document.removeEventListener('keydown', _keyHandler);
      _keyHandler = null;
    }
    const el = document.getElementById('dashboard');
    el.classList.add('hidden');
    el.innerHTML = '';
  }

  // ---- Scaffold -----------------------------------------------------------

  function renderScaffold(el) {
    el.className = 'app-view dashboard';
    el.innerHTML = `
      <div class="card">
        <div class="view-header dashboard-header">
          <h2 class="view-title">My Folders</h2>
          <button id="add-folder-btn" title="Add folder (A)">+ Add Folder</button>
        </div>
        <div id="dashboard-summary" class="dashboard-summary hidden"></div>
        <div id="folder-list" class="folder-list">
          ${buildSkeletonCards(3)}
        </div>
      </div>
    `;
    document.getElementById('add-folder-btn').addEventListener('click', handleAddFolder);
  }

  function buildSkeletonCards(count) {
    let html = '';
    for (let i = 0; i < count; i++) {
      html += `
        <div class="skeleton-card">
          <div class="skeleton skeleton-card-icon"></div>
          <div class="skeleton-card-body">
            <div class="skeleton skeleton-card-name"></div>
            <div class="skeleton skeleton-card-sub"></div>
          </div>
        </div>`;
    }
    return html;
  }

  // ---- Data loading -------------------------------------------------------

  async function loadAndRenderFolders() {
    try {
      const resp = await window.API.getFolders();
      if (!resp.ok) {
        renderFolderList([]);
        return;
      }
      const data = await resp.json();
      renderFolderList(data.folders || []);
    } catch {
      renderFolderList([]);
    }
  }

  // ---- Rendering ----------------------------------------------------------

  function renderFolderList(folders) {
    const container = document.getElementById('folder-list');
    if (!container) return;

    renderSummaryStrip(folders);

    if (folders.length === 0) {
      container.innerHTML = '';
      container.appendChild(buildEmptyState());
      return;
    }

    container.innerHTML = '';
    for (const folder of folders) {
      container.appendChild(buildFolderCard(folder));
    }
  }

  function renderSummaryStrip(folders) {
    const el = document.getElementById('dashboard-summary');
    if (!el) return;
    if (folders.length === 0) { el.classList.add('hidden'); return; }

    const totalFiles = folders.reduce(function (s, f) { return s + (f.file_count || 0); }, 0);
    const totalBytes = folders.reduce(function (s, f) { return s + (f.total_size_bytes || 0); }, 0);
    const lastTimes  = folders.map(function (f) { return f.last_backed_up_at; }).filter(Boolean);
    const lastIso    = lastTimes.length
      ? lastTimes.reduce(function (a, b) { return a > b ? a : b; })
      : null;

    el.classList.remove('hidden');

    // Build the three simple stats
    el.innerHTML = `
      <div class="summary-stat">
        <div class="summary-stat-value">${folders.length}</div>
        <div class="summary-stat-label">Folder${folders.length === 1 ? '' : 's'}</div>
      </div>
      <div class="summary-stat">
        <div class="summary-stat-value">${totalFiles}</div>
        <div class="summary-stat-label">File${totalFiles === 1 ? '' : 's'}</div>
      </div>
      <div class="summary-stat">
        <div class="summary-stat-value">${formatSize(totalBytes)}</div>
        <div class="summary-stat-label">Total size</div>
      </div>
      <div class="summary-stat" id="summary-last-backup-stat">
        <div class="summary-stat-value summary-stat-value--sm" id="summary-last-backup-val"></div>
        <div class="summary-stat-label">Last backup</div>
      </div>
    `;

    // #26 — Build the rel-time toggle button for Last backup
    const valEl = document.getElementById('summary-last-backup-val');
    if (!lastIso) {
      valEl.textContent = 'Never';
    } else {
      const btn = buildRelTimeButton(lastIso);
      valEl.appendChild(btn);
    }
  }

  function buildEmptyState() {
    const div = document.createElement('div');
    div.className = 'empty-state folder-list-empty';
    div.innerHTML = `
      <div class="empty-state-icon">☁️</div>
      <h3 class="empty-state-title">No folders backed up yet</h3>
      <p class="empty-state-body">Add a folder to start protecting your files in the cloud.</p>
    `;
    const btn = document.createElement('button');
    btn.className = 'empty-state-btn';
    btn.textContent = '+ Add Your First Folder';
    btn.addEventListener('click', handleAddFolder);
    div.appendChild(btn);
    return div;
  }

  function buildFolderCard(folder) {
    const name     = folder.name || lastSegment(folder.path);
    const health   = folderHealthStatus(folder.last_backed_up_at);
    const freshPct = folderFreshnessPercent(folder.last_backed_up_at);

    const healthTitle = health === 'green'
      ? 'Backed up recently'
      : health === 'amber'
        ? 'Backup is more than 24 hours old'
        : 'Never backed up';

    const card = document.createElement('div');
    card.className = 'folder-card';
    card.dataset.folderId = folder.id;

    card.innerHTML = `
      <div class="folder-card-row">
        <div class="folder-card-body">
          <span class="folder-card-icon">📁</span>
          <div class="folder-card-info">
            <div class="folder-card-name-row">
              <div class="folder-card-name" title="${escapeAttr(folder.path)}">${escapeHtml(name)}</div>
              <span class="folder-health-dot health-${health}" title="${healthTitle}"></span>
            </div>
            <div class="folder-card-path">${escapeHtml(folder.path)}</div>
            <div class="folder-card-stats" id="folder-stats-${folder.id}"></div>
          </div>
        </div>
        <div class="folder-card-actions">
          <button class="open-folder-btn">Open</button>
          <button class="rename-folder-btn">Rename</button>
          <button class="remove-folder-btn">Remove</button>
        </div>
      </div>
      <div class="folder-freshness-bar" title="Backup freshness: ${freshPct}%">
        <div class="folder-freshness-fill health-${health}"></div>
      </div>
    `;

    card.querySelector('.folder-freshness-fill').style.width = freshPct + '%';

    // #26 — Populate stats with rel-time toggle for last backup
    (function () {
      const statsEl = card.querySelector('.folder-card-stats');
      if (!statsEl) return;
      const count = folder.file_count || 0;
      const prefix = formatSize(folder.total_size_bytes || 0) +
        ' · ' + count + ' file' + (count === 1 ? '' : 's') + ' · ';
      statsEl.textContent = prefix;
      if (folder.last_backed_up_at) {
        const span = document.createElement('span');
        span.textContent = 'Last backup ';
        statsEl.appendChild(span);
        statsEl.appendChild(buildRelTimeButton(folder.last_backed_up_at));
      } else {
        statsEl.textContent += 'Never backed up';
      }
    }());

    card.querySelector('.open-folder-btn').addEventListener('click', function () {
      hide();
      window.Files.show(folder.id, folder.path);
    });

    card.querySelector('.rename-folder-btn').addEventListener('click', function () {
      handleRenameFolder(folder, card);
    });

    card.querySelector('.remove-folder-btn').addEventListener('click', function () {
      handleRemoveFolder(folder.id, name, folder.path);
    });

    return card;
  }

  // ---- Actions ------------------------------------------------------------

  async function handleAddFolder() {
    let dirPath;
    try {
      dirPath = await electronAPI.selectDirectory();
    } catch {
      window.UI.toast('Could not open folder picker', 'error');
      return;
    }
    if (!dirPath) return;

    try {
      const resp = await window.API.addFolder(dirPath);
      if (!resp.ok) {
        window.UI.toast('Could not add folder', 'error');
        return;
      }
      await loadAndRenderFolders();
    } catch {
      window.UI.toast('Could not reach server', 'error');
    }
  }

  // ---- #24 — Inline rename -------------------------------------------------

  async function handleRenameFolder(folder, card) {
    const nameRow = card.querySelector('.folder-card-name-row');
    const nameDiv = card.querySelector('.folder-card-name');
    const actionsDiv = card.querySelector('.folder-card-actions');
    if (!nameRow || !nameDiv || !actionsDiv) return;

    const currentName = nameDiv.textContent;

    // Replace name div with an input
    const input = document.createElement('input');
    input.type = 'text';
    input.className = 'folder-rename-input';
    input.value = currentName;
    nameDiv.replaceWith(input);
    input.focus();
    input.select();

    // Replace action buttons with Save/Cancel
    const originalButtons = actionsDiv.innerHTML;
    actionsDiv.innerHTML = '';

    const saveBtn = document.createElement('button');
    saveBtn.textContent = 'Save';
    saveBtn.className = 'open-folder-btn';

    const cancelBtn = document.createElement('button');
    cancelBtn.textContent = 'Cancel';
    cancelBtn.className = 'remove-folder-btn';

    actionsDiv.appendChild(saveBtn);
    actionsDiv.appendChild(cancelBtn);

    function restore() {
      input.replaceWith(nameDiv);
      actionsDiv.innerHTML = originalButtons;
      actionsDiv.querySelector('.open-folder-btn').addEventListener('click', function () {
        hide();
        window.Files.show(folder.id, folder.path);
      });
      actionsDiv.querySelector('.rename-folder-btn').addEventListener('click', function () {
        handleRenameFolder(folder, card);
      });
      actionsDiv.querySelector('.remove-folder-btn').addEventListener('click', function () {
        handleRemoveFolder(folder.id, nameDiv.textContent, folder.path);
      });
    }

    cancelBtn.addEventListener('click', restore);

    async function doSave() {
      const newName = input.value.trim();
      if (!newName || newName === currentName) { restore(); return; }
      saveBtn.disabled = true;
      try {
        const resp = await window.API.renameFolder(folder.id, newName);
        if (!resp.ok) {
          window.UI.toast('Could not rename folder', 'error');
          saveBtn.disabled = false;
          return;
        }
        nameDiv.textContent = newName;
        folder.name = newName;
        input.replaceWith(nameDiv);
        actionsDiv.innerHTML = originalButtons;
        actionsDiv.querySelector('.open-folder-btn').addEventListener('click', function () {
          hide();
          window.Files.show(folder.id, folder.path);
        });
        actionsDiv.querySelector('.rename-folder-btn').addEventListener('click', function () {
          handleRenameFolder(folder, card);
        });
        actionsDiv.querySelector('.remove-folder-btn').addEventListener('click', function () {
          handleRemoveFolder(folder.id, nameDiv.textContent, folder.path);
        });
        window.UI.toast('Folder renamed', 'success');
      } catch {
        window.UI.toast('Could not reach server', 'error');
        saveBtn.disabled = false;
      }
    }

    saveBtn.addEventListener('click', doSave);
    input.addEventListener('keydown', function (e) {
      if (e.key === 'Enter') { e.preventDefault(); doSave(); }
      if (e.key === 'Escape') { e.preventDefault(); restore(); }
    });
  }

  // ---- Folder removal — multi-step safe-delete flow -------------------------

  function makeModal(titleText, labelId) {
    const overlay = document.createElement('div');
    overlay.className = 'modal-overlay modal-overlay--confirm';

    const card = document.createElement('div');
    card.className = 'modal-card';
    card.setAttribute('role', 'dialog');
    card.setAttribute('aria-modal', 'true');
    card.setAttribute('aria-labelledby', labelId);

    const header = document.createElement('div');
    header.className = 'modal-header';
    const titleEl = document.createElement('h3');
    titleEl.className = 'modal-title';
    titleEl.id = labelId;
    titleEl.textContent = titleText;
    header.appendChild(titleEl);

    const body = document.createElement('div');
    body.className = 'modal-body';

    const actions = document.createElement('div');
    actions.className = 'modal-actions';

    card.appendChild(header);
    card.appendChild(body);
    card.appendChild(actions);
    overlay.appendChild(card);
    document.body.appendChild(overlay);

    function close() {
      document.removeEventListener('keydown', overlay._keyHandler);
      overlay.remove();
    }

    overlay._keyHandler = function (e) {
      if (e.key === 'Escape') { close(); }
      if (e.key === 'Tab') {
        const focusable = Array.from(card.querySelectorAll(
          'button:not([disabled]), [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
        ));
        if (focusable.length === 0) { e.preventDefault(); return; }
        const first = focusable[0], last = focusable[focusable.length - 1];
        if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
        else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
      }
    };
    document.addEventListener('keydown', overlay._keyHandler);
    overlay.addEventListener('click', function (e) { if (e.target === overlay) close(); });
    requestAnimationFrame(function () { overlay.classList.add('modal-visible'); });

    return { overlay, card, body, actions, close };
  }

  // Step 1 — offer to download first.
  function showDownloadPromptModal(id, name, folderPath, onSkip, onDownloadAndRemove) {
    const { body, actions, close } = makeModal('Remove "' + name + '"?', 'rm-step1-title');

    const msg = document.createElement('p');
    msg.textContent = 'Before removing this folder, you can download all cloud-backed files back to your local drive.';
    body.appendChild(msg);

    const note = document.createElement('p');
    note.className = 'remove-modal-note';
    note.textContent = 'Skipping will permanently erase all cloud backups for this folder.';
    body.appendChild(note);

    const dlBtn = document.createElement('button');
    dlBtn.className = 'modal-action-btn modal-action-download';
    dlBtn.textContent = '↓  Download All & Remove';

    const skipBtn = document.createElement('button');
    skipBtn.className = 'modal-action-btn modal-action-danger';
    skipBtn.textContent = 'Skip — Show What I\'ll Lose';

    const cancelBtn = document.createElement('button');
    cancelBtn.className = 'modal-action-btn';
    cancelBtn.textContent = 'Cancel';

    actions.appendChild(dlBtn);
    actions.appendChild(skipBtn);
    actions.appendChild(cancelBtn);

    cancelBtn.addEventListener('click', close);
    cancelBtn.focus();

    dlBtn.addEventListener('click', function () {
      close();
      onDownloadAndRemove();
    });
    skipBtn.addEventListener('click', function () {
      close();
      onSkip();
    });
  }

  // Step 2 — show what will be lost, let user preview files, then confirm deletion.
  async function showFileLossModal(id, name, folderPath, onConfirm, onBack) {
    const { overlay, body, actions, close } = makeModal('Checking for cloud-only files…', 'rm-step2-title');

    const loadingEl = document.createElement('p');
    loadingEl.className = 'remove-modal-loading';
    loadingEl.textContent = 'Loading backed-up files…';
    body.appendChild(loadingEl);

    const backBtn = document.createElement('button');
    backBtn.className = 'modal-action-btn';
    backBtn.textContent = '← Go Back';

    const cancelBtn = document.createElement('button');
    cancelBtn.className = 'modal-action-btn';
    cancelBtn.textContent = 'Cancel';

    const deleteBtn = document.createElement('button');
    deleteBtn.className = 'modal-action-btn modal-action-danger';
    deleteBtn.textContent = 'Permanently Delete Everything';
    deleteBtn.disabled = true;

    actions.appendChild(backBtn);
    actions.appendChild(cancelBtn);
    actions.appendChild(deleteBtn);

    cancelBtn.addEventListener('click', close);
    backBtn.addEventListener('click', function () { close(); onBack(); });
    deleteBtn.addEventListener('click', function () { close(); onConfirm(); });
    cancelBtn.focus();

    // Fetch backups and scan the actual local filesystem in parallel.
    let backups = [], localPaths = new Set();
    try {
      const [backupResp, localFiles] = await Promise.all([
        window.API.getFolderBackups(id),
        window.electronAPI.getAllFilePaths(folderPath),
      ]);
      if (backupResp.ok) backups = (await backupResp.json()).backups || [];
      for (const p of (localFiles || [])) localPaths.add(p);
    } catch {}

    // Only files that have no local copy will be gone forever.
    const cloudOnly = backups.filter(function (b) {
      return !localPaths.has(b.relative_path || b.path);
    });

    body.innerHTML = '';
    const titleEl = overlay.querySelector('#rm-step2-title');

    if (cloudOnly.length === 0) {
      if (titleEl) titleEl.textContent = 'Remove "' + name + '"';
      const msg = document.createElement('p');
      msg.textContent = backups.length > 0
        ? 'All backed-up files exist on your local drive — nothing in the cloud will be lost.'
        : 'No cloud backups exist for this folder.';
      body.appendChild(msg);

      // No files at risk — swap the scary actions for a plain confirmation.
      actions.innerHTML = '';
      const plainRemoveBtn = document.createElement('button');
      plainRemoveBtn.className = 'modal-action-btn';
      plainRemoveBtn.textContent = 'Remove Folder';
      plainRemoveBtn.addEventListener('click', function () { close(); onConfirm(); });
      const plainCancelBtn = document.createElement('button');
      plainCancelBtn.className = 'modal-action-btn';
      plainCancelBtn.textContent = 'Cancel';
      plainCancelBtn.addEventListener('click', close);
      actions.appendChild(plainRemoveBtn);
      actions.appendChild(plainCancelBtn);
      plainRemoveBtn.focus();
      return;
    }

    if (titleEl) titleEl.textContent = 'Files That Will Be Lost Forever';
    const summary = document.createElement('p');
    summary.className = 'remove-modal-summary';
    summary.textContent = cloudOnly.length + ' file' + (cloudOnly.length === 1 ? '' : 's') +
      ' exist only in the cloud and will be permanently deleted.';
    body.appendChild(summary);

    const list = document.createElement('ul');
    list.className = 'remove-file-list';

    for (const b of cloudOnly) {
      const relPath = b.relative_path || b.path || '';
      const li = document.createElement('li');
      li.className = 'remove-file-item';

      const nameSpan = document.createElement('span');
      nameSpan.className = 'remove-file-name';
      nameSpan.textContent = relPath;
      li.appendChild(nameSpan);

      const filename = relPath.split('/').pop();
      const canPreview = window.Files && window.Files.previewType && window.Files.previewType(filename);
      if (canPreview) {
        const previewBtn = document.createElement('button');
        previewBtn.className = 'remove-file-preview-btn';
        previewBtn.textContent = '🔍 Preview';
        previewBtn.setAttribute('aria-label', 'Preview ' + filename);
        previewBtn.addEventListener('click', function () {
          if (window.Files && window.Files.previewCloudFile) {
            window.Files.previewCloudFile(id, relPath, filename);
          }
        });
        li.appendChild(previewBtn);
      }

      list.appendChild(li);
    }
    body.appendChild(list);

    const confirm = document.createElement('label');
    confirm.className = 'remove-confirm-checkbox-label';
    const checkbox = document.createElement('input');
    checkbox.type = 'checkbox';
    checkbox.id = 'rm-confirm-checkbox';
    checkbox.addEventListener('change', function () {
      deleteBtn.disabled = !checkbox.checked;
      if (!deleteBtn.disabled) deleteBtn.focus();
    });
    confirm.appendChild(checkbox);
    confirm.appendChild(document.createTextNode(' I understand these files cannot be recovered'));
    body.appendChild(confirm);
  }

  // Download-and-remove flow: download all backups then delete.
  async function downloadAllAndRemove(id, name, folderPath) {
    let backups = [];
    try {
      const resp = await window.API.getFolderBackups(id);
      if (!resp.ok) throw new Error('HTTP ' + resp.status);
      const data = await resp.json();
      backups = data.backups || [];
    } catch (e) {
      window.UI.toast('Could not fetch backup list: ' + e.message, 'error');
      return;
    }

    if (backups.length === 0) {
      await doRemoveFolder(id, name);
      return;
    }

    window.UI.toast('Downloading ' + backups.length + ' file' + (backups.length === 1 ? '' : 's') + '…', 'success');
    let failed = 0;
    for (const b of backups) {
      const relPath = b.relative_path || b.path;
      try {
        const resp = await window.API.downloadFromFolder(id, relPath);
        if (!resp.ok) { failed++; continue; }
        const buffer = await resp.arrayBuffer();
        const result = await window.electronAPI.saveFile(folderPath, relPath, buffer);
        if (result.error) failed++;
      } catch { failed++; }
    }

    if (failed > 0) {
      window.UI.toast(failed + ' file' + (failed === 1 ? '' : 's') + ' could not be downloaded. Aborting removal.', 'error');
      return;
    }

    window.UI.toast('All files downloaded. Removing folder…', 'success');
    await doRemoveFolder(id, name);
  }

  async function doRemoveFolder(id, name) {
    try {
      const resp = await window.API.removeFolder(id);
      if (!resp.ok) {
        window.UI.toast('Could not remove folder', 'error');
        return;
      }
      const card = document.querySelector('.folder-card[data-folder-id="' + id + '"]');
      if (card) card.remove();
      const container = document.getElementById('folder-list');
      if (container && container.querySelectorAll('.folder-card').length === 0) {
        container.innerHTML = '';
        container.appendChild(buildEmptyState());
        renderSummaryStrip([]);
      }
      window.UI.toast('Folder removed', 'success');
    } catch {
      window.UI.toast('Could not reach server', 'error');
    }
  }

  async function handleRemoveFolder(id, name, folderPath) {
    showDownloadPromptModal(
      id, name, folderPath,
      // onSkip — show file loss warning
      function () {
        showFileLossModal(
          id, name, folderPath,
          // onConfirm
          function () { doRemoveFolder(id, name); },
          // onBack — return to step 1
          function () { handleRemoveFolder(id, name, folderPath); }
        );
      },
      // onDownloadAndRemove
      function () { downloadAllAndRemove(id, name, folderPath); }
    );
  }

  // ---- Helpers ------------------------------------------------------------

  function lastSegment(path) {
    return path.replace(/\\/g, '/').split('/').filter(Boolean).pop() || path;
  }

  function formatStats(folder) {
    const parts = [];
    parts.push(formatSize(folder.total_size_bytes || 0));
    const count = folder.file_count || 0;
    parts.push(count + ' file' + (count === 1 ? '' : 's'));
    if (folder.last_backed_up_at) {
      parts.push('Last backup ' + relativeTime(folder.last_backed_up_at));
    } else {
      parts.push('Never backed up');
    }
    return parts.join(' · ');
  }

  /**
   * #26 — Build a <button class="rel-time-btn"> that toggles between
   * relative time display and the absolute ISO date on click.
   * @param {string} isoString
   * @returns {HTMLButtonElement}
   */
  function buildRelTimeButton(isoString) {
    const btn = document.createElement('button');
    btn.className = 'rel-time-btn';
    btn.setAttribute('type', 'button');
    btn.setAttribute('title', 'Click to toggle absolute date');

    const relLabel = relativeTime(isoString);
    const absLabel = new Date(isoString).toLocaleString(undefined, {
      year: 'numeric', month: 'short', day: 'numeric',
      hour: '2-digit', minute: '2-digit',
    });

    let showingRel = true;
    btn.textContent = relLabel;

    btn.addEventListener('click', function (e) {
      e.stopPropagation();
      showingRel = !showingRel;
      btn.textContent = showingRel ? relLabel : absLabel;
    });

    return btn;
  }

  function formatSize(bytes) {
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
    if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
    return (bytes / (1024 * 1024 * 1024)).toFixed(1) + ' GB';
  }

  function relativeTime(isoString) {
    const ms = Date.now() - new Date(isoString).getTime();
    const secs = Math.floor(ms / 1000);
    if (secs < 60) return 'just now';
    const mins = Math.floor(secs / 60);
    if (mins < 60) return mins + 'm ago';
    const hours = Math.floor(mins / 60);
    if (hours < 24) return hours + 'h ago';
    const days = Math.floor(hours / 24);
    return days + 'd ago';
  }

  function escapeHtml(str) {
    return String(str)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  function escapeAttr(str) {
    return String(str)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  // ---- Expose -------------------------------------------------------------

  window.Dashboard = { show, hide };

})();
