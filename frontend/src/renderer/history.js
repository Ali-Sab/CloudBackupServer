'use strict';

(function () {

  if (typeof document === 'undefined' || typeof window === 'undefined' || window._testMode) return;

  const PAGE_SIZE = 50;

  let _folderId = 0;   // 0 = all folders
  let _offset   = 0;
  let _loading  = false;
  let _hasMore  = true;
  let _folders  = [];  // [{ id, name }] cached for the filter dropdown
  let _returnTo = null; // 'dashboard' | 'files'

  // ---- Public -----------------------------------------------------------------

  function show() {
    _returnTo = document.getElementById('file-browser').classList.contains('hidden')
      ? 'dashboard' : 'files';

    ['dashboard', 'file-browser', 'account', 'settings'].forEach(function (id) {
      document.getElementById(id).classList.add('hidden');
    });

    const el = document.getElementById('history');
    el.classList.remove('hidden');
    _folderId = 0;
    _offset   = 0;
    _hasMore  = true;
    _render(el);
    _loadFolders().then(function () { _loadPage(true); });
  }

  function hide() {
    document.getElementById('history').classList.add('hidden');
    document.getElementById('history').innerHTML = '';
    if (_returnTo === 'files') {
      const cur = window.Files.getCurrent();
      window.Files.show(cur.folderId, cur.folderPath);
    } else {
      window.Dashboard.show();
    }
  }

  window.History = { show, hide };

  // ---- Render scaffold --------------------------------------------------------

  function _render(el) {
    el.className = 'app-view history-view';
    el.innerHTML = `
      <div class="card history-card">
        <div class="view-header history-header">
          <button id="history-back-btn" class="view-back-btn">← Back</button>
          <h2 class="view-title history-title">Activity Log</h2>
          <select id="history-folder-filter" class="history-folder-filter" aria-label="Filter by folder">
            <option value="0">All folders</option>
          </select>
        </div>
        <ul id="history-list" class="history-list"></ul>
        <div id="history-load-more-wrap" class="history-load-more-wrap hidden">
          <button id="history-load-more-btn" class="history-load-more-btn">Load more</button>
        </div>
      </div>
    `;

    document.getElementById('history-back-btn').addEventListener('click', hide);
    document.getElementById('history-folder-filter').addEventListener('change', function (e) {
      _folderId = Number(e.target.value);
      _offset   = 0;
      _hasMore  = true;
      document.getElementById('history-list').innerHTML = '';
      _loadPage(true);
    });
    document.getElementById('history-load-more-btn').addEventListener('click', function () {
      _loadPage(false);
    });

    _renderSkeletons();
  }

  function _renderSkeletons() {
    const ul = document.getElementById('history-list');
    if (!ul) return;
    ul.innerHTML = '';
    for (let i = 0; i < 8; i++) {
      const li = document.createElement('li');
      li.className = 'history-item skeleton-row';
      li.innerHTML = '<span class="skeleton skeleton-icon"></span>' +
        '<span class="skeleton skeleton-line" style="max-width:' + [55,70,60,80,50,65,72,45][i] + '%"></span>' +
        '<span class="skeleton skeleton-short skeleton-right"></span>';
      ul.appendChild(li);
    }
  }

  // ---- Data loading -----------------------------------------------------------

  async function _loadFolders() {
    try {
      const resp = await window.API.getFolders();
      if (!resp.ok) return;
      const data = await resp.json();
      _folders = (data.folders || []).map(function (f) { return { id: f.id, name: f.name || f.path }; });
      const sel = document.getElementById('history-folder-filter');
      if (!sel) return;
      _folders.forEach(function (f) {
        const opt = document.createElement('option');
        opt.value = f.id;
        opt.textContent = f.name;
        sel.appendChild(opt);
      });
    } catch (_) {}
  }

  async function _loadPage(reset) {
    if (_loading || (!_hasMore && !reset)) return;
    _loading = true;

    const ul = document.getElementById('history-list');
    const moreWrap = document.getElementById('history-load-more-wrap');
    if (!ul) { _loading = false; return; }

    if (reset) {
      ul.innerHTML = '';
      _renderSkeletons();
    }

    try {
      const resp = await window.API.getBackupHistory({
        folderId: _folderId || undefined,
        limit:  PAGE_SIZE,
        offset: _offset,
      });
      if (!resp.ok) throw new Error('HTTP ' + resp.status);
      const data = await resp.json();
      const items = data.items || [];

      if (reset) ul.innerHTML = '';

      if (items.length === 0 && _offset === 0) {
        ul.innerHTML = '';
        const empty = document.createElement('li');
        empty.className = 'history-empty';
        empty.innerHTML = '<div class="empty-state empty-state--compact">' +
          '<div class="empty-state-icon">📋</div>' +
          '<h3 class="empty-state-title">No backup activity yet</h3>' +
          '<p class="empty-state-body">Files you back up will appear here.</p>' +
          '</div>';
        ul.appendChild(empty);
        _hasMore = false;
      } else {
        items.forEach(function (item) { ul.appendChild(_buildRow(item)); });
        _offset += items.length;
        _hasMore = items.length === PAGE_SIZE;
      }

      if (moreWrap) moreWrap.classList.toggle('hidden', !_hasMore);
    } catch (e) {
      if (reset) ul.innerHTML = '';
      const err = document.createElement('li');
      err.className = 'history-empty';
      err.textContent = 'Could not load activity: ' + e.message;
      ul.appendChild(err);
    } finally {
      _loading = false;
    }
  }

  // ---- Row builder ------------------------------------------------------------

  function _buildRow(item) {
    const li = document.createElement('li');
    li.className = 'history-item';

    const icon = document.createElement('span');
    icon.className = 'history-item-icon';
    icon.textContent = _fileIcon(item.relative_path.split('/').pop());

    const body = document.createElement('div');
    body.className = 'history-item-body';

    const name = document.createElement('span');
    name.className = 'history-item-name';
    name.textContent = item.relative_path.split('/').pop();
    name.setAttribute('title', item.relative_path);

    const path = document.createElement('span');
    path.className = 'history-item-path';
    path.textContent = item.folder_name + (item.relative_path.includes('/') ?
      ' › ' + item.relative_path.split('/').slice(0, -1).join(' › ') : '');

    body.appendChild(name);
    body.appendChild(path);

    const meta = document.createElement('div');
    meta.className = 'history-item-meta';

    const time = document.createElement('span');
    time.className = 'history-item-time';
    const ts = new Date(item.backed_up_at);
    time.textContent = _relativeTime(ts);
    time.setAttribute('title', ts.toLocaleString());
    let showRelative = true;
    time.addEventListener('click', function () {
      showRelative = !showRelative;
      time.textContent = showRelative ? _relativeTime(ts) : ts.toLocaleString();
    });

    const size = document.createElement('span');
    size.className = 'history-item-size';
    size.textContent = _formatSize(item.size);

    const ver = document.createElement('span');
    ver.className = 'history-item-version';
    ver.textContent = 'v' + item.version;
    ver.setAttribute('title', 'Backup version ' + item.version);

    meta.appendChild(time);
    meta.appendChild(size);
    meta.appendChild(ver);

    li.appendChild(icon);
    li.appendChild(body);
    li.appendChild(meta);
    return li;
  }

  // ---- Helpers ----------------------------------------------------------------

  function _relativeTime(date) {
    const diff = Date.now() - date.getTime();
    const s = Math.floor(diff / 1000);
    if (s < 60)  return 'just now';
    const m = Math.floor(s / 60);
    if (m < 60)  return m + 'm ago';
    const h = Math.floor(m / 60);
    if (h < 24)  return h + 'h ago';
    const d = Math.floor(h / 24);
    if (d < 30)  return d + 'd ago';
    return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' });
  }

  function _formatSize(bytes) {
    if (bytes == null) return '—';
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
    if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
    return (bytes / (1024 * 1024 * 1024)).toFixed(1) + ' GB';
  }

  function _fileIcon(filename) {
    const ext = (filename.split('.').pop() || '').toLowerCase();
    const map = {
      jpg: '🖼️', jpeg: '🖼️', png: '🖼️', gif: '🖼️', webp: '🖼️', svg: '🖼️',
      mp4: '🎬', mov: '🎬', avi: '🎬', mkv: '🎬',
      mp3: '🎵', wav: '🎵', flac: '🎵', aac: '🎵',
      pdf: '📕',
      zip: '📦', tar: '📦', gz: '📦', rar: '📦',
      js: '💻', ts: '💻', py: '💻', go: '💻', rs: '💻', java: '💻',
      html: '💻', css: '💻', json: '💻', yaml: '💻', yml: '💻',
      doc: '📃', docx: '📃', xls: '📃', xlsx: '📃', ppt: '📃',
      db: '🗄️', sqlite: '🗄️',
    };
    return map[ext] || '📄';
  }

})();
