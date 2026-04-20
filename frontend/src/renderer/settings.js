/**
 * Settings panel — appearance, notifications, server configuration.
 *
 * Depends on: ui.js (window.UI), dashboard.js (window.Dashboard)
 *
 * Exposes: window.Settings = { show, hide }
 */

'use strict';

(function () {

  if (typeof document === 'undefined' || typeof window === 'undefined') return;

  const STORAGE_KEYS = {
    theme:         'theme',
    notifications: 'settings_notifications',
    serverUrl:     'settings_server_url',
  };

  function getPref(key, defaultVal) {
    try { return localStorage.getItem(key) ?? defaultVal; } catch { return defaultVal; }
  }
  function setPref(key, val) {
    try { localStorage.setItem(key, val); } catch {}
  }

  // ---- Render ----------------------------------------------------------------

  function render() {
    const el = document.getElementById('settings');

    const currentTheme         = getPref(STORAGE_KEYS.theme, 'dark');
    const notificationsEnabled = getPref(STORAGE_KEYS.notifications, 'true') === 'true';
    const serverUrl            = getPref(STORAGE_KEYS.serverUrl, 'http://localhost:8080');

    el.innerHTML = `
      <div class="settings-panel">
        <div class="settings-panel-header">
          <h2>Settings</h2>
          <button id="settings-close-btn" class="panel-close-btn" aria-label="Close settings">✕</button>
        </div>

        <section class="settings-section">
          <h3 class="settings-section-title">Appearance</h3>
          <div class="settings-row">
            <div class="settings-row-label">
              <span>Theme</span>
              <span class="settings-row-hint">Dark or light color scheme</span>
            </div>
            <div class="settings-toggle-group" role="group" aria-label="Theme">
              <button class="settings-toggle-btn ${currentTheme !== 'light' ? 'active' : ''}" data-theme="dark" id="theme-dark-btn">🌙 Dark</button>
              <button class="settings-toggle-btn ${currentTheme === 'light' ? 'active' : ''}" data-theme="light" id="theme-light-btn">☀️ Light</button>
            </div>
          </div>
        </section>

        <section class="settings-section">
          <h3 class="settings-section-title">Notifications</h3>
          <div class="settings-row">
            <div class="settings-row-label">
              <span>Desktop notifications</span>
              <span class="settings-row-hint">Show a system notification when a backup completes</span>
            </div>
            <label class="settings-switch" aria-label="Enable desktop notifications">
              <input type="checkbox" id="notifications-toggle" ${notificationsEnabled ? 'checked' : ''} />
              <span class="settings-switch-track"></span>
            </label>
          </div>
        </section>

        <section class="settings-section">
          <h3 class="settings-section-title">Server</h3>
          <div class="settings-row settings-row--column">
            <div class="settings-row-label">
              <span>Backend URL</span>
              <span class="settings-row-hint">Address of the Cloud Backup Server instance</span>
            </div>
            <div class="settings-server-url-row">
              <input type="url" id="server-url-input" class="settings-url-input" value="${serverUrl}" placeholder="http://localhost:8080" />
              <button id="server-url-save-btn" class="settings-save-btn">Save</button>
            </div>
            <div class="form-error" id="server-url-error"></div>
            <p class="settings-row-hint settings-restart-note">Changes take effect after restarting the app.</p>
          </div>
        </section>
      </div>
    `;

    document.getElementById('settings-close-btn').addEventListener('click', hide);
    document.getElementById('theme-dark-btn').addEventListener('click', function () { applyTheme('dark'); });
    document.getElementById('theme-light-btn').addEventListener('click', function () { applyTheme('light'); });
    document.getElementById('notifications-toggle').addEventListener('change', function (e) {
      setPref(STORAGE_KEYS.notifications, e.target.checked ? 'true' : 'false');
    });
    document.getElementById('server-url-save-btn').addEventListener('click', saveServerUrl);
  }

  function applyTheme(theme) {
    if (theme === 'light') {
      document.documentElement.setAttribute('data-theme', 'light');
    } else {
      document.documentElement.removeAttribute('data-theme');
    }
    setPref(STORAGE_KEYS.theme, theme);

    // Keep the header theme toggle button in sync.
    const headerBtn = document.getElementById('theme-toggle-btn');
    if (headerBtn) headerBtn.textContent = theme === 'light' ? '☀️' : '🌙';

    // Re-render the toggle buttons to reflect new active state.
    const darkBtn  = document.getElementById('theme-dark-btn');
    const lightBtn = document.getElementById('theme-light-btn');
    if (darkBtn)  darkBtn.classList.toggle('active', theme !== 'light');
    if (lightBtn) lightBtn.classList.toggle('active', theme === 'light');
  }

  function saveServerUrl() {
    const input   = document.getElementById('server-url-input');
    const errorEl = document.getElementById('server-url-error');
    const btn     = document.getElementById('server-url-save-btn');
    errorEl.textContent = '';

    const val = input.value.trim();
    if (!val) {
      errorEl.textContent = 'URL cannot be empty';
      return;
    }
    try { new URL(val); } catch {
      errorEl.textContent = 'Enter a valid URL (e.g. http://localhost:8080)';
      return;
    }

    setPref(STORAGE_KEYS.serverUrl, val);
    btn.textContent = 'Saved ✓';
    setTimeout(function () { btn.textContent = 'Save'; }, 2000);
    window.UI.toast('Server URL saved — restart the app to connect', 'success');
  }

  // ---- Public interface ------------------------------------------------------

  let _returnToFiles = false;

  function show() {
    _returnToFiles = !document.getElementById('file-browser').classList.contains('hidden');
    const el = document.getElementById('settings');
    render();
    el.classList.remove('hidden');
    document.getElementById('dashboard').classList.add('hidden');
    document.getElementById('file-browser').classList.add('hidden');
  }

  function hide() {
    document.getElementById('settings').classList.add('hidden');
    if (_returnToFiles) {
      window.FileBrowser.show();
    } else {
      window.Dashboard.show();
    }
  }

  window.Settings = { show, hide };

}());
