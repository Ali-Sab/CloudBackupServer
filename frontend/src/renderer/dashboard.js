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

(function () {

  if (typeof document === 'undefined' || typeof window === 'undefined') return;

  const electronAPI = window.electronAPI;

  // ---- Public interface ---------------------------------------------------

  async function show() {
    const el = document.getElementById('dashboard');
    el.classList.remove('hidden');
    renderScaffold(el);
    await loadAndRenderFolders();
  }

  function hide() {
    const el = document.getElementById('dashboard');
    el.classList.add('hidden');
    el.innerHTML = '';
  }

  // ---- Scaffold -----------------------------------------------------------

  function renderScaffold(el) {
    el.className = 'dashboard';
    el.innerHTML = `
      <div class="card">
        <div class="dashboard-header">
          <h2>My Folders</h2>
          <button id="add-folder-btn">+ Add Folder</button>
        </div>
        <div id="folder-list" class="folder-list">
          <p class="folder-list-empty">Loading…</p>
        </div>
      </div>
    `;
    document.getElementById('add-folder-btn').addEventListener('click', handleAddFolder);
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

    if (folders.length === 0) {
      container.innerHTML = '<p class="folder-list-empty">No folders yet — click "+ Add Folder" to get started.</p>';
      return;
    }

    container.innerHTML = '';
    for (const folder of folders) {
      container.appendChild(buildFolderCard(folder));
    }
  }

  function buildFolderCard(folder) {
    const name = folder.name || lastSegment(folder.path);
    const stats = formatStats(folder);

    const card = document.createElement('div');
    card.className = 'folder-card';
    card.dataset.folderId = folder.id;

    card.innerHTML = `
      <div class="folder-card-body">
        <span class="folder-card-icon">📁</span>
        <div class="folder-card-info">
          <div class="folder-card-name" title="${escapeAttr(folder.path)}">${escapeHtml(name)}</div>
          <div class="folder-card-path">${escapeHtml(folder.path)}</div>
          <div class="folder-card-stats">${escapeHtml(stats)}</div>
        </div>
      </div>
      <div class="folder-card-actions">
        <button class="open-folder-btn">Open</button>
        <button class="remove-folder-btn">Remove</button>
      </div>
    `;

    card.querySelector('.open-folder-btn').addEventListener('click', function () {
      hide();
      window.Files.show(folder.id, folder.path);
    });

    card.querySelector('.remove-folder-btn').addEventListener('click', function () {
      handleRemoveFolder(folder.id, name);
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

  async function handleRemoveFolder(id, name) {
    if (!confirm('Remove "' + name + '" and all its cloud backups?')) return;

    try {
      const resp = await window.API.removeFolder(id);
      if (!resp.ok) {
        window.UI.toast('Could not remove folder', 'error');
        return;
      }
      // Remove the card immediately without a full reload.
      const card = document.querySelector('.folder-card[data-folder-id="' + id + '"]');
      if (card) card.remove();

      const container = document.getElementById('folder-list');
      if (container && container.children.length === 0) {
        container.innerHTML = '<p class="folder-list-empty">No folders yet — click "+ Add Folder" to get started.</p>';
      }

      window.UI.toast('Folder removed', 'success');
    } catch {
      window.UI.toast('Could not reach server', 'error');
    }
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
    return String(str).replace(/"/g, '&quot;');
  }

  // ---- Expose -------------------------------------------------------------

  window.Dashboard = { show, hide };

})();
