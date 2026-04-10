/**
 * File browser UI — recursive directory tree with drill-down navigation.
 *
 * Depends on:
 *   window.electronAPI  (selectDirectory, readDirectory, watchDirectory,
 *                        unwatchDirectory, onDirectoryChange)
 *   window.API          (getWatchedPath, setWatchedPath, syncFiles)
 *   window.UI           (toast)
 *
 * Exposes: window.Files = { show, hide }
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

(function () {

  if (typeof document === 'undefined' || typeof window === 'undefined') return;

  const electronAPI = window.electronAPI;

  let currentPath = null;

  // Full flat entry list from the most recent disk read.
  let allEntries = [];

  // Drill-down navigation stack: [{ name: string, relativePath: string }, …]
  let viewStack = [];

  // Which relative-paths are currently expanded inline (persisted across refreshes).
  const expandedPaths = new Set();

  // Register the live-watch listener once at module startup.
  electronAPI.onDirectoryChange(function () {
    if (currentPath) refreshDirectory(currentPath, { silent: true });
  });

  // ---- Public interface ---------------------------------------------------

  async function show() {
    const el = document.getElementById('file-browser');
    el.classList.remove('hidden');
    renderScaffold(el);

    try {
      const resp = await window.API.getWatchedPath();
      if (resp.ok) {
        const data = await resp.json();
        currentPath = data.path;
        document.getElementById('current-path').textContent = data.path;
        await electronAPI.watchDirectory(data.path);
        await refreshDirectory(data.path, { silent: true });
      }
    } catch {
      // Not critical — user can still pick a folder manually.
    }
  }

  function hide() {
    const el = document.getElementById('file-browser');
    el.classList.add('hidden');
    el.innerHTML = '';
    currentPath = null;
    allEntries = [];
    viewStack = [];
    expandedPaths.clear();
    electronAPI.unwatchDirectory();
  }

  // ---- Scaffold -----------------------------------------------------------
  // Creates the stable card HTML once; subsequent renders only update
  // #breadcrumb and #file-list without touching the rest of the card.

  function renderScaffold(el) {
    el.className = 'file-browser';
    el.innerHTML = `
      <div class="card">
        <div class="file-browser-header">
          <div id="breadcrumb" class="breadcrumb-area">
            <h2>Local Folder</h2>
          </div>
          <button id="select-dir-btn">Select Folder</button>
        </div>
        <p id="current-path" class="file-current-path"></p>
        <ul id="file-list" class="file-list">
          <li class="file-item file-empty">No folder selected</li>
        </ul>
      </div>
    `;
    document.getElementById('select-dir-btn').addEventListener('click', handleSelectDirectory);
  }

  // ---- Select & refresh ---------------------------------------------------

  async function handleSelectDirectory() {
    const dirPath = await electronAPI.selectDirectory();
    if (!dirPath) return;

    currentPath = dirPath;
    viewStack = [];
    expandedPaths.clear();
    document.getElementById('current-path').textContent = dirPath;
    await electronAPI.watchDirectory(dirPath);

    try {
      const resp = await window.API.setWatchedPath(dirPath);
      window.UI.toast(resp.ok ? 'Folder saved to server' : 'Could not save folder to server', resp.ok ? 'success' : 'error');
    } catch {
      window.UI.toast('Could not reach server', 'error');
    }

    await refreshDirectory(dirPath);
  }

  /**
   * Read the full directory tree from disk, cache entries, sync to backend,
   * then re-render the current view (breadcrumb + file list).
   *
   * @param {string} dirPath
   * @param {{ silent?: boolean }} [opts]  silent = no sync toast (used on restore / live watch)
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

    // Sync to backend (non-blocking — failures don't affect the UI).
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
      const resp = await window.API.syncFiles(files);
      if (resp.ok && !opts?.silent) {
        window.UI.toast('File list synced (' + files.length + ' item' + (files.length === 1 ? '' : 's') + ')');
      }
    } catch {
      // Sync failures are non-fatal.
    }
  }

  // ---- View rendering -----------------------------------------------------

  /**
   * Render the breadcrumb and file list for the current viewStack position.
   * Called after every navigation action and after every directory refresh.
   */
  function renderView() {
    renderBreadcrumb();

    const listEl = document.getElementById('file-list');
    if (!listEl) return;

    if (allEntries.length === 0) {
      listEl.innerHTML = '<li class="file-item file-empty">Folder is empty</li>';
      return;
    }

    const tree = buildTree(allEntries);
    const viewNode = resolveViewNode(tree);

    listEl.innerHTML = '';
    if (Object.keys(viewNode.children).length === 0) {
      listEl.innerHTML = '<li class="file-item file-empty">Folder is empty</li>';
    } else {
      renderTree(listEl, viewNode.children);
    }
  }

  /**
   * Render the breadcrumb navigation header into #breadcrumb.
   *
   * When at root (viewStack empty):
   *   📁 documents
   *
   * When drilled in (viewStack = [{name:'photos'},{name:'2024'}]):
   *   ← Back   📁 documents › photos › 2024
   *
   * Every segment is a button that navigates back to that depth.
   */
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

    // Use just the last segment of the OS path as the root label.
    const rootName = currentPath.replace(/\\/g, '/').split('/').filter(Boolean).pop() || 'Root';

    const nav = document.createElement('nav');
    nav.className = 'breadcrumb';
    nav.setAttribute('aria-label', 'Folder navigation');

    // ← Back button (shown when not at root)
    if (viewStack.length > 0) {
      const back = document.createElement('button');
      back.className = 'breadcrumb-back';
      back.setAttribute('aria-label', 'Go up one level');
      back.textContent = '← Back';
      back.addEventListener('click', function () { navigateTo(viewStack.length - 1); });
      nav.appendChild(back);
    }

    // Root segment
    const rootBtn = document.createElement('button');
    rootBtn.className = 'breadcrumb-seg' + (viewStack.length === 0 ? ' active' : '');
    rootBtn.setAttribute('title', currentPath);
    rootBtn.innerHTML = '📁 ' + truncateName(rootName, 22);
    rootBtn.addEventListener('click', function () { navigateTo(0); });
    nav.appendChild(rootBtn);

    // One button per drill-down level
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

  /** Drill into a directory — push it onto viewStack and re-render. */
  function navigateInto(entry) {
    viewStack.push({ name: entry.name, relativePath: entry.relativePath });
    renderView();
  }

  /**
   * Navigate to an ancestor level.
   * @param {number} depth  0 = root, 1 = first child, etc.
   */
  function navigateTo(depth) {
    viewStack = viewStack.slice(0, depth);
    renderView();
  }

  /**
   * Walk the built tree to the node at the current viewStack position.
   * Returns the root if any segment is missing (defensive).
   */
  function resolveViewNode(tree) {
    let node = tree;
    for (const seg of viewStack) {
      if (node.children[seg.name]) {
        node = node.children[seg.name];
      } else {
        return tree; // Fallback to root if path no longer exists.
      }
    }
    return node;
  }

  // ---- Tree building ------------------------------------------------------

  /**
   * Convert a flat entry array into a nested tree keyed by name.
   *
   * Input:  [{ name, relativePath, isDirectory, size, modified }, …]
   * Output: { children: { [name]: { entry, children: {…} } } }
   *
   * Tree position is derived from relativePath (e.g. "photos/2024/img.jpg"
   * becomes root › photos › 2024 › img.jpg).
   */
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

  /**
   * Recursively render a level of the tree into `ul`.
   *
   * For directories:
   *   [▶/▼ toggle]  📁  [name — clickable → navigateInto]
   *   └─ (nested <ul.tree-children> when expanded)
   *
   * For files:
   *   📄  [name]  [size]
   *
   * @param {HTMLUListElement} ul
   * @param {Object.<string, TreeNode>} children
   */
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

        // ▶/▼ toggle — inline expand/collapse only
        const toggle = document.createElement('button');
        toggle.className = 'tree-toggle';
        toggle.textContent = isOpen ? '▼' : '▶';
        toggle.setAttribute('aria-label', isOpen ? 'Collapse' : 'Expand');

        const icon = document.createElement('span');
        icon.className = 'file-icon';
        icon.textContent = '📁';

        // Folder name — clicking navigates into the folder
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
        const icon = document.createElement('span');
        icon.className = 'file-icon';
        icon.textContent = '📄';

        const nameEl = document.createElement('span');
        nameEl.className = 'file-name';
        nameEl.setAttribute('title', name);
        nameEl.innerHTML = truncateName(name, 40);

        const meta = document.createElement('span');
        meta.className = 'file-meta';
        meta.textContent = entry ? formatSize(entry.size) : '';

        li.appendChild(icon);
        li.appendChild(nameEl);
        li.appendChild(meta);
        ul.appendChild(li);
      }
    }
  }

  // ---- Helpers ------------------------------------------------------------

  /**
   * Middle-truncate a name to `max` chars: first half + … + last half.
   * Returns safe HTML. Full name is always in the element's title attribute.
   */
  function truncateName(name, max) {
    if (name.length <= max) return escapeHtml(name);
    const half = Math.floor((max - 1) / 2);
    return escapeHtml(name.slice(0, half)) + '…' + escapeHtml(name.slice(-half));
  }

  function escapeHtml(str) {
    const div = document.createElement('div');
    div.appendChild(document.createTextNode(String(str)));
    return div.innerHTML;
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
