/**
 * E2E smoke tests for the Cloud Backup Electron desktop app.
 *
 * Prerequisites: docker compose up (backend + postgres + minio must be running).
 * Run with:  make test-e2e
 *        or  cd frontend && npm run test:e2e
 *        or  npm run test:e2e:headed  (to watch the app window)
 *
 * Tests T2–T13 skip gracefully when the backend is not running (safeStorage tests also skip if unavailable).
 */

'use strict';

const { test, expect, _electron: electron } = require('@playwright/test');
const path = require('path');
const fs   = require('fs');
const os   = require('os');
const http = require('http');

// ---- Constants ---------------------------------------------------------------

const MAIN_JS  = path.resolve(__dirname, '../src/main.js');
const BASE_URL = process.env.API_BASE_URL || 'http://localhost:8080';

// ---- HTTP helpers ------------------------------------------------------------

function httpRequest(method, urlPath, body, authToken) {
  return new Promise((resolve, reject) => {
    const data   = body ? JSON.stringify(body) : null;
    const parsed = new URL(`${BASE_URL}${urlPath}`);
    const headers = { 'Content-Type': 'application/json' };
    if (data) headers['Content-Length'] = Buffer.byteLength(data);
    if (authToken) headers['Authorization'] = `Bearer ${authToken}`;

    const req = http.request(
      { hostname: parsed.hostname, port: Number(parsed.port) || 80,
        path: parsed.pathname, method, headers },
      (res) => {
        let raw = '';
        res.on('data', c => { raw += c; });
        res.on('end', () => {
          try { resolve({ status: res.statusCode, body: JSON.parse(raw) }); }
          catch { resolve({ status: res.statusCode, body: {} }); }
        });
      }
    );
    req.on('error', reject);
    if (data) req.write(data);
    req.end();
  });
}

const httpPost   = (path, body, token) => httpRequest('POST',   path, body, token);
const httpGet    = (path, token)        => httpRequest('GET',    path, null, token);
const httpDelete = (path, token)        => httpRequest('DELETE', path, null, token);

/** Upload raw file bytes to a backup endpoint. Returns { status, body }. */
function httpUploadFile(urlPath, content, token) {
  return new Promise((resolve, reject) => {
    const buf    = Buffer.isBuffer(content) ? content : Buffer.from(content);
    const sha256 = require('crypto').createHash('sha256').update(buf).digest('hex');
    const parsed = new URL(`${BASE_URL}${urlPath}`);
    const headers = {
      'Content-Type': 'application/octet-stream',
      'Content-Length': String(buf.length),
      'X-Checksum-SHA256': sha256,
      'X-File-Size': String(buf.length),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    };
    const req = http.request(
      { hostname: parsed.hostname, port: Number(parsed.port) || 80,
        path: parsed.pathname, method: 'PUT', headers },
      (res) => {
        let raw = '';
        res.on('data', c => { raw += c; });
        res.on('end', () => {
          try { resolve({ status: res.statusCode, body: JSON.parse(raw) }); }
          catch { resolve({ status: res.statusCode, body: {} }); }
        });
      }
    );
    req.on('error', reject);
    req.write(buf);
    req.end();
  });
}

// ---- Helpers -----------------------------------------------------------------

/** Create a real temp directory with a couple of files for upload tests. */
function makeTempBackupDir() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'e2e-backup-'));
  fs.writeFileSync(path.join(dir, 'hello.txt'), 'hello from e2e test');
  fs.writeFileSync(path.join(dir, 'data.bin'), Buffer.alloc(256, 0xab));
  return dir;
}

/** Launch the Electron app, pointing E2E_SELECT_DIR at tmpDir. */
function launchApp(tmpDir, { userDataDir } = {}) {
  return electron.launch({
    args: [MAIN_JS, ...(userDataDir ? [`--user-data-dir=${userDataDir}`] : [])],
    env: {
      ...process.env,
      NODE_ENV: 'test',
      E2E_SELECT_DIR: tmpDir || '',
    },
  });
}

/**
 * Register a fresh isolated user and return { email, password, token }.
 * Each test that mutates state uses its own user so tests don't interfere.
 */
async function registerFreshUser(suffix) {
  const ts  = Date.now();
  const tag = suffix || 'user';
  const email    = `e2e_${tag}_${ts}@test.example`;
  const password = `E2ePass_${ts}!`;
  const res = await httpPost('/api/auth/register', { email, password });
  if (res.status !== 201) throw new Error(`Registration failed: HTTP ${res.status}`);
  return { email, password, token: res.body.access_token };
}

/** Log in via the UI login form. Assumes the form is already visible. */
async function loginViaUI(page, email, password) {
  await page.waitForSelector('#login-form', { timeout: 10_000 });
  await page.fill('#email',    email);
  await page.fill('#password', password);
  await page.click('#login-form button[type="submit"]');
  await page.waitForSelector('#logout-btn', { timeout: 10_000 });
}

// ---- Per-test fixtures -------------------------------------------------------
// Each test gets its own Electron process and temp directory.

let app, page, tmpDir;

test.beforeEach(async () => {
  tmpDir = makeTempBackupDir();
  app    = await launchApp(tmpDir);
  page   = await app.firstWindow();
  await page.waitForLoadState('domcontentloaded');
});

test.afterEach(async () => {
  await app.close();
  try { fs.rmSync(tmpDir, { recursive: true, force: true }); } catch {}
});

// ---- T1: Boot (no backend required) -----------------------------------------

test('T1: app launches and renders session status card', async () => {
  await page.waitForSelector('#session-status:not(.loading)', { timeout: 12_000 });

  const sessionStatus = page.locator('#session-status');
  await expect(sessionStatus).toBeVisible();

  // Must be logged-out (backend up → login form) or error (backend down → retry).
  const classes = await sessionStatus.getAttribute('class');
  expect(classes.includes('logged-out') || classes.includes('error')).toBe(true);
});

// ---- T2: Login shows dashboard -----------------------------------------------

test('T2: login with valid credentials shows dashboard', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('login');
  await loginViaUI(page, email, password);

  // Dashboard must appear; file browser stays hidden.
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });
  await expect(page.locator('#dashboard')).toBeVisible();
  await expect(page.locator('#file-browser')).toHaveClass(/hidden/);

  // Dashboard shows the "My Folders" heading and Add Folder button.
  await expect(page.locator('.dashboard h2')).toHaveText('My Folders');
  await expect(page.locator('#add-folder-btn')).toBeVisible();
});

// ---- T3: Add Folder via dashboard -------------------------------------------

test('T3: clicking Add Folder adds a folder card to the dashboard', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('addfolder');
  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });


  const cardsBefore = await page.locator('.folder-card').count();

  // Click Add Folder — E2E_SELECT_DIR bypasses the OS dialog.
  await page.click('#add-folder-btn');

  // A new folder card must appear.
  await page.waitForSelector('.folder-card', { timeout: 10_000 });
  const cardsAfter = await page.locator('.folder-card').count();
  expect(cardsAfter).toBe(cardsBefore + 1);

  // The card shows the correct path.
  const cardPath = await page.locator('.folder-card .folder-card-path').first().textContent();
  expect(cardPath.trim()).toBe(tmpDir);
});

// ---- T4: Pre-seeded folder visible in dashboard -----------------------------

test('T4: pre-seeded folder appears in dashboard on login', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('preseed');

  // Add a folder via the API before logging in.
  const addRes = await httpPost('/api/folders', { path: tmpDir }, token);
  expect(addRes.status).toBe(201);

  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

  // Dashboard must show the seeded folder.
  await page.waitForSelector('.folder-card', { timeout: 8_000 });
  const cardPath = await page.locator('.folder-card .folder-card-path').first().textContent();
  expect(cardPath.trim()).toBe(tmpDir);
});

// ---- T5: Open folder from dashboard → file browser --------------------------

test('T5: opening a folder navigates to the file browser', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('open');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });
  await page.waitForSelector('.folder-card', { timeout: 8_000 });

  // Click "Open" on the folder card.
  await page.click('.open-folder-btn');

  // File browser must appear; dashboard must hide.
  await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });
  await expect(page.locator('#file-browser')).toBeVisible();
  await expect(page.locator('#dashboard')).toHaveClass(/hidden/);

  // "← All Folders" button is present; "Backup Now" is enabled.
  await expect(page.locator('#all-folders-btn')).toBeVisible();
  await expect(page.locator('#backup-now-btn')).not.toBeDisabled();
});

// ---- T6: Backup Now ----------------------------------------------------------

test('T6: Backup Now uploads files and shows result toast', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('backup');
  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

  // Add folder via UI (E2E_SELECT_DIR points at tmpDir with hello.txt + data.bin).
  await page.click('#add-folder-btn');
  await page.waitForSelector('.folder-card', { timeout: 10_000 });

  // Open the folder.
  await page.click('.open-folder-btn');
  await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });
  await expect(page.locator('#backup-now-btn')).not.toBeDisabled();

  // Trigger backup.
  await page.click('#backup-now-btn');

  // A toast with "Backup:" appears when done (allow 20 s for local HTTP uploads).
  await page.waitForSelector('.toast-visible', { timeout: 20_000 });
  const toastText = await page.locator('.toast-visible').first().textContent();
  expect(toastText).toMatch(/Backup:/);

  // Button must revert.
  await expect(page.locator('#backup-now-btn')).toHaveText('Backup Now', { timeout: 8_000 });
});

// ---- T7: All Folders back navigation ----------------------------------------

test('T7: "← All Folders" returns to dashboard from file browser', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('back');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });
  await page.waitForSelector('.folder-card', { timeout: 8_000 });

  // Open the folder.
  await page.click('.open-folder-btn');
  await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });

  // Navigate back.
  await page.click('#all-folders-btn');

  // Dashboard visible; file browser hidden.
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });
  await expect(page.locator('#dashboard')).toBeVisible();
  await expect(page.locator('#file-browser')).toHaveClass(/hidden/);
});

// ---- T8: Remove folder -------------------------------------------------------

test('T8: removing a folder removes its card from the dashboard', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('remove');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });
  await page.waitForSelector('.folder-card', { timeout: 8_000 });

  await page.click('.remove-folder-btn');

  // Step 1 — download prompt. Skip straight to file-loss warning.
  await page.waitForSelector('.modal-overlay.modal-visible', { timeout: 5_000 });
  await page.click('.modal-action-danger'); // "Skip — Show What I'll Lose"

  // Step 2 — no cloud-only files: plain "Remove Folder" button, no danger styling.
  await page.waitForSelector('.modal-overlay.modal-visible', { timeout: 5_000 });
  const deleteBtn = page.locator('.modal-action-btn', { hasText: 'Remove Folder' });
  await expect(deleteBtn).toBeEnabled({ timeout: 5_000 });
  await deleteBtn.click();

  // Card must disappear.
  await expect(page.locator('.folder-card')).toHaveCount(0, { timeout: 8_000 });

  // Empty state message must appear.
  await expect(page.locator('.folder-list-empty')).toBeVisible();
});

// ---- T9: Logout --------------------------------------------------------------

test('T9: logout clears session and returns to login form', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('logout');
  await loginViaUI(page, email, password);
  await page.waitForSelector('#logout-btn', { timeout: 10_000 });

  await page.click('#logout-btn');

  // Login form reappears; dashboard and file browser are hidden.
  await page.waitForSelector('#login-form', { timeout: 10_000 });
  await expect(page.locator('#login-form')).toBeVisible();
  await expect(page.locator('#dashboard')).toHaveClass(/hidden/, { timeout: 5_000 });
  await expect(page.locator('#file-browser')).toHaveClass(/hidden/, { timeout: 5_000 });
  await expect(page.locator('#logout-btn')).not.toBeVisible();
});

// ---- T10: clicking a file name opens it with the OS default app -------------

test('T10: clicking a file name calls shell.openPath with the correct absolute path', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('openfile');
  await httpPost('/api/folders', { path: tmpDir }, token);

  // Spy on shell.openPath in the main process before the click.
  await app.evaluate(({ shell }) => {
    global._e2eOpenedPath = null;
    shell.openPath = async (p) => { global._e2eOpenedPath = p; return ''; };
  });

  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });
  await page.waitForSelector('.folder-card', { timeout: 8_000 });

  await page.click('.open-folder-btn');
  await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });
  await page.waitForSelector('.file-item:not(.is-dir):not(.file-empty)', { timeout: 8_000 });

  // Click the first file name link.
  await page.locator('.file-item:not(.is-dir) .file-link').first().click();

  // Give the IPC round-trip a moment, then assert.
  await page.waitForTimeout(500);
  const openedPath = await app.evaluate(() => global._e2eOpenedPath);
  expect(openedPath).toBeTruthy();
  expect(openedPath.startsWith(tmpDir)).toBe(true);
});

// ---- T11: Remember me checkbox appears on login form ------------------------

test('T11: remember-me checkbox is visible on the login form in Electron', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  // The checkbox is only shown when safeStorage is available; skip if not.
  const available = await app.evaluate(({ safeStorage }) => safeStorage.isEncryptionAvailable());
  test.skip(!available, 'safeStorage not available on this platform');

  await page.waitForSelector('#login-form', { timeout: 10_000 });
  await expect(page.locator('#remember-me')).toBeVisible();
});

// ---- T12: Remember me — relaunch auto-logs in --------------------------------

test('T12: logging in with remember-me checked auto-logs in on next launch', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const available = await app.evaluate(({ safeStorage }) => safeStorage.isEncryptionAvailable());
  test.skip(!available, 'safeStorage not available on this platform');

  const userDataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'e2e-userdata-'));
  try {
    // Close the default per-test app; we need one pinned to userDataDir.
    await app.close();
    app = await launchApp(tmpDir, { userDataDir });
    page = await app.firstWindow();
    await page.waitForLoadState('domcontentloaded');

    const { email, password } = await registerFreshUser('rememberme');

    // Login with remember-me checked.
    await page.waitForSelector('#login-form', { timeout: 10_000 });
    await page.fill('#email', email);
    await page.fill('#password', password);
    await page.check('#remember-me');
    await page.click('#login-form button[type="submit"]');
    await page.waitForSelector('#logout-btn', { timeout: 10_000 });

    // Close and relaunch with the same userData — expect auto-login.
    await app.close();
    app = await launchApp(tmpDir, { userDataDir });
    page = await app.firstWindow();
    await page.waitForLoadState('domcontentloaded');

    // Should land on the dashboard without seeing the login form.
    await page.waitForSelector('#logout-btn', { timeout: 12_000 });
    await expect(page.locator('#login-form')).toHaveCount(0);
  } finally {
    try { fs.rmSync(userDataDir, { recursive: true, force: true }); } catch {}
  }
});

// ---- T14: Bad credentials show field error ----------------------------------

test('T14: bad login credentials show a form error and keep the login form visible', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  await page.waitForSelector('#login-form', { timeout: 10_000 });
  await page.fill('#email',    'nosuchuser@test.example');
  await page.fill('#password', 'wrongpassword');
  await page.click('#login-form button[type="submit"]');

  // Error message must appear.
  await page.waitForSelector('#form-error:not(:empty)', { timeout: 8_000 });
  const errorText = await page.locator('#form-error').textContent();
  expect(errorText.trim().length).toBeGreaterThan(0);

  // Must still be on the login form — no dashboard, no logout button visible.
  await expect(page.locator('#login-form')).toBeVisible();
  await expect(page.locator('#logout-btn')).not.toBeVisible();
  await expect(page.locator('#dashboard')).toHaveClass(/hidden/);
});

// ---- T15: Keyboard shortcut 'b' triggers backup -----------------------------

test('T15: keyboard shortcut "b" triggers Backup Now in the file browser', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('kbbackup');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });
  await page.waitForSelector('.folder-card', { timeout: 8_000 });

  await page.click('.open-folder-btn');
  await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });
  await expect(page.locator('#backup-now-btn')).not.toBeDisabled();

  // Press 'b' — must behave identically to clicking Backup Now.
  await page.keyboard.press('b');

  // A toast with backup results must appear.
  await page.waitForSelector('.toast-visible', { timeout: 20_000 });
  const toastText = await page.locator('.toast-visible').first().textContent();
  expect(toastText).toMatch(/Backup:/);

  // Button must revert to idle state.
  await expect(page.locator('#backup-now-btn')).toHaveText('Backup Now', { timeout: 8_000 });
});

// ---- T16: Backup progress counter and fill bar ------------------------------

test('T16: backup progress counter and fill bar reach the done state', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('progress');
  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

  // Add folder via UI (E2E_SELECT_DIR → tmpDir with hello.txt + data.bin).
  await page.click('#add-folder-btn');
  await page.waitForSelector('.folder-card', { timeout: 10_000 });
  await page.click('.open-folder-btn');
  await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });

  await page.click('#backup-now-btn');

  // Wait for the progress counter to reach its done state ("N / N ✓").
  await page.waitForSelector('.backup-progress.done', { timeout: 20_000 });
  const counterText = await page.locator('#backup-progress').textContent();
  expect(counterText).toMatch(/✓/);

  // Fill bar must carry the "done" class (green) at completion.
  const fillClass = await page.locator('#backup-progress-fill').getAttribute('class');
  expect(fillClass).toContain('done');
});

// ---- T17: Dashboard summary strip appears after adding a folder -------------

test('T17: dashboard summary strip is hidden with no folders and visible after adding one', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('summary');
  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

  // Wait for the skeleton to be replaced — an empty account shows the empty-state element.
  await page.waitForSelector('.folder-list-empty', { timeout: 8_000 });

  // With no folders the summary strip must be hidden.
  await expect(page.locator('.dashboard-summary')).toHaveClass(/hidden/);

  // Add a folder via UI.
  await page.click('#add-folder-btn');
  await page.waitForSelector('.folder-card', { timeout: 10_000 });

  // Summary strip must now be visible and show 1 folder.
  await expect(page.locator('.dashboard-summary')).not.toHaveClass(/hidden/);
  await expect(page.locator('.dashboard-summary')).toBeVisible();
  const folderCount = await page.locator('.summary-stat-value').first().textContent();
  expect(folderCount.trim()).toBe('1');
});

// ---- T13: Metadata modal -----------------------------------------------------

test('T13: clicking the info button on a file shows the metadata modal', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('meta');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });
  await page.waitForSelector('.folder-card', { timeout: 8_000 });

  await page.click('.open-folder-btn');
  await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });

  // Wait for real file rows (not the loading/empty placeholder), then force-click the info button.
  await page.waitForSelector('.file-item:not(.is-dir):not(.file-empty)', { timeout: 8_000 });
  await page.locator('.file-item:not(.is-dir) .file-info-btn').first().click({ force: true });

  // Modal must appear.
  await page.waitForSelector('#metadata-modal.modal-visible', { timeout: 5_000 });
  await expect(page.locator('.modal-title')).toBeVisible();
  await expect(page.locator('.meta-list')).toBeVisible();

  // First meta-value row must say "File" (the Type row).
  const typeValue = await page.locator('.meta-value').first().textContent();
  expect(typeValue.trim()).toBe('File');

  // Escape key must close the modal.
  await page.keyboard.press('Escape');
  await expect(page.locator('#metadata-modal')).toHaveCount(0, { timeout: 3_000 });
});

// ---- T18: Account panel -------------------------------------------------------

test('T18: account panel opens via the 👤 button and shows the current email', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('account-panel');
  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

  // Click the Account nav button in the header.
  await page.click('#account-nav-btn');

  // Account panel must be visible and dashboard hidden.
  await expect(page.locator('#account')).not.toHaveClass(/hidden/);
  await expect(page.locator('#dashboard')).toHaveClass(/hidden/);

  // Current email must be displayed.
  const emailEl = await page.locator('#account-current-email').textContent();
  expect(emailEl.trim()).toBe(email);

  // Close button returns to dashboard.
  await page.click('#account-close-btn');
  await expect(page.locator('#dashboard')).not.toHaveClass(/hidden/);
  await expect(page.locator('#account')).toHaveClass(/hidden/);
});

// ---- T19: Settings panel ------------------------------------------------------

test('T19: settings panel opens via the ⚙️ button and shows appearance controls', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('settings-panel');
  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

  // Click the Settings nav button.
  await page.click('#settings-nav-btn');

  // Settings panel must be visible.
  await expect(page.locator('#settings')).not.toHaveClass(/hidden/);
  await expect(page.locator('#dashboard')).toHaveClass(/hidden/);

  // Theme toggle buttons must be present.
  await expect(page.locator('#theme-dark-btn')).toBeVisible();
  await expect(page.locator('#theme-light-btn')).toBeVisible();

  // Notifications toggle switch must be present (the checkbox itself is visually hidden by CSS).
  await expect(page.locator('.settings-switch')).toBeVisible();

  // Close button returns to dashboard.
  await page.click('#settings-close-btn');
  await expect(page.locator('#dashboard')).not.toHaveClass(/hidden/);
  await expect(page.locator('#settings')).toHaveClass(/hidden/);
});

// ---- T20–T24: Safe-delete flow -----------------------------------------------

// T20: Clicking Remove shows the download-prompt modal with all three options.
test('T20: remove folder shows download-prompt modal with all three action buttons', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('rm-prompt');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await page.waitForSelector('.folder-card', { timeout: 8_000 });

  await page.click('.remove-folder-btn');
  await page.waitForSelector('.modal-overlay.modal-visible', { timeout: 5_000 });

  // All three buttons must be present.
  await expect(page.locator('.modal-action-download')).toBeVisible();  // Download All & Remove
  await expect(page.locator('.modal-action-danger')).toBeVisible();    // Skip
  await expect(page.locator('.modal-action-btn:has-text("Cancel")')).toBeVisible();
});

// T21: Cancel on the download-prompt modal leaves the folder card intact.
test('T21: cancelling the download prompt leaves the folder card intact', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('rm-cancel');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await page.waitForSelector('.folder-card', { timeout: 8_000 });

  await page.click('.remove-folder-btn');
  await page.waitForSelector('.modal-overlay.modal-visible', { timeout: 5_000 });
  await page.click('.modal-action-btn:has-text("Cancel")');

  // Modal must be gone, card must still be there.
  await expect(page.locator('.modal-overlay')).toHaveCount(0, { timeout: 3_000 });
  await expect(page.locator('.folder-card')).toHaveCount(1);
});

// T22: Skip with no cloud-only files shows a safe message and delete button enabled immediately.
test('T22: skip with no cloud-only files shows safe message and enables delete without checkbox', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('rm-safe');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await page.waitForSelector('.folder-card', { timeout: 8_000 });

  await page.click('.remove-folder-btn');
  await page.waitForSelector('.modal-overlay.modal-visible', { timeout: 5_000 });
  await page.click('.modal-action-danger'); // Skip

  // Step 2 modal appears — no cloud-only files, so plain (non-red) Remove Folder button shown.
  await page.waitForSelector('.modal-overlay.modal-visible', { timeout: 5_000 });

  // No danger button and no file list — safe path only.
  await expect(page.locator('.modal-action-danger')).toHaveCount(0, { timeout: 5_000 });
  await expect(page.locator('.remove-file-list')).toHaveCount(0);

  await page.click('.modal-action-btn:has-text("Remove Folder")');
  await expect(page.locator('.folder-card')).toHaveCount(0, { timeout: 8_000 });
});

// T23: "Go Back" on step 2 returns to the download-prompt modal.
test('T23: go back from file-loss modal returns to download-prompt modal', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('rm-goback');

  // Upload a cloud-only file so the full step-2 modal (with Go Back) is shown.
  const addRes = await httpPost('/api/folders', { path: tmpDir }, token);
  const folderId = addRes.body.id;
  await httpUploadFile(`/api/folders/${folderId}/backup/hello.txt`, 'hello from e2e', token);
  fs.unlinkSync(path.join(tmpDir, 'hello.txt'));

  await loginViaUI(page, email, password);
  await page.waitForSelector('.folder-card', { timeout: 8_000 });

  // Open step 1, skip to step 2.
  await page.click('.remove-folder-btn');
  await page.waitForSelector('.modal-overlay.modal-visible', { timeout: 5_000 });
  await page.click('.modal-action-danger');
  await page.waitForSelector('.remove-file-list', { timeout: 8_000 });

  // Go Back.
  await page.click('.modal-action-btn:has-text("Go Back")');

  // Step 1 modal must reappear with the download button.
  await page.waitForSelector('.modal-overlay.modal-visible', { timeout: 5_000 });
  await expect(page.locator('.modal-action-download')).toBeVisible();

  // Folder must still be there.
  await expect(page.locator('.folder-card')).toHaveCount(1);
});

// T24: Skip with a cloud-only file shows it in the list, requires checkbox, preview button present.
test('T24: skip with cloud-only file shows file in loss list, requires checkbox to enable delete', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('rm-cloudonly');

  // Create folder, upload hello.txt to cloud, then delete it locally so it becomes cloud-only.
  const addRes = await httpPost('/api/folders', { path: tmpDir }, token);
  const folderId = addRes.body.id;
  const fileContent = 'hello from e2e test';
  await httpUploadFile(`/api/folders/${folderId}/backup/hello.txt`, fileContent, token);
  fs.unlinkSync(path.join(tmpDir, 'hello.txt'));

  await loginViaUI(page, email, password);
  await page.waitForSelector('.folder-card', { timeout: 8_000 });

  // Open step 1, skip to step 2.
  await page.click('.remove-folder-btn');
  await page.waitForSelector('.modal-overlay.modal-visible', { timeout: 5_000 });
  await page.click('.modal-action-danger'); // Skip

  // Step 2 — file-loss modal.
  await page.waitForSelector('.remove-file-list', { timeout: 8_000 });

  // hello.txt must appear in the loss list.
  await expect(page.locator('.remove-file-name')).toContainText('hello.txt');

  // Preview button must be present (hello.txt is a text file).
  await expect(page.locator('.remove-file-preview-btn')).toBeVisible();

  // Delete button must be disabled until checkbox is checked.
  const deleteBtn = page.locator('.modal-action-danger');
  await expect(deleteBtn).toBeDisabled();

  await page.check('#rm-confirm-checkbox');
  await expect(deleteBtn).toBeEnabled({ timeout: 2_000 });

  // Confirm deletion.
  await deleteBtn.click();
  await expect(page.locator('.folder-card')).toHaveCount(0, { timeout: 8_000 });
});

// ---- T25–T27: File preview modal ---------------------------------------------

/** Open the file browser for the single folder card already on the dashboard. */
async function openFileBrowser(page, token) {
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });
  await page.waitForSelector('.folder-card', { timeout: 8_000 });
  await page.click('.open-folder-btn');
  await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });
  await page.waitForSelector('.file-item:not(.is-dir):not(.file-empty)', { timeout: 8_000 });
}

// T25: Clicking a previewable file name opens the preview modal.
test('T25: clicking a previewable file name opens the in-app preview modal', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('preview-click');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await openFileBrowser(page, token);

  // hello.txt is a text file — previewable. Click its name.
  await page.locator('.file-name.file-link', { hasText: 'hello.txt' }).click();

  // Preview modal must appear with the filename in the header.
  await expect(page.locator('#preview-modal')).toBeVisible({ timeout: 5_000 });
  await expect(page.locator('#preview-modal .modal-title')).toHaveText('hello.txt');
});

// T26: The row-level 🔍 preview button opens the preview modal.
test('T26: the row preview button opens the preview modal', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('preview-btn');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await openFileBrowser(page, token);

  // Hover the hello.txt row to reveal its preview button, then click.
  const helloRow = page.locator('.file-item', { hasText: 'hello.txt' });
  await helloRow.hover();
  await helloRow.locator('.file-preview-btn').click();

  await expect(page.locator('#preview-modal')).toBeVisible({ timeout: 5_000 });
  await expect(page.locator('#preview-modal .modal-title')).toHaveText('hello.txt');
});

// T27: Closing the preview modal with × removes it from the DOM.
test('T27: closing the preview modal with × removes it', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('preview-close');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await openFileBrowser(page, token);

  await page.locator('.file-name.file-link', { hasText: 'hello.txt' }).click();
  await expect(page.locator('#preview-modal')).toBeVisible({ timeout: 5_000 });

  await page.locator('#preview-modal .modal-close').click();
  await expect(page.locator('#preview-modal')).toHaveCount(0, { timeout: 3_000 });
});

// ---- T28–T30: Activity log (history) -----------------------------------------

// T28: Clicking the 📋 nav button shows the activity log screen.
test('T28: history nav button shows the activity log screen', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('history-open');
  await loginViaUI(page, email, password);

  await page.click('#history-nav-btn');
  await expect(page.locator('.history-card')).toBeVisible({ timeout: 5_000 });
  await expect(page.locator('.history-title')).toHaveText('Activity Log');
});

// T29: The ← Back button on the activity log returns to the dashboard.
test('T29: history back button returns to dashboard', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('history-back');
  await loginViaUI(page, email, password);

  await page.click('#history-nav-btn');
  await expect(page.locator('.history-card')).toBeVisible({ timeout: 5_000 });

  await page.click('#history-back-btn');
  await expect(page.locator('#dashboard')).not.toHaveClass(/hidden/, { timeout: 3_000 });
  await expect(page.locator('.history-card')).toHaveCount(0);
});

// T30: After a backup, the activity log shows a history row for the backed-up file.
test('T30: activity log shows a row after a file is backed up', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('history-row');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await openFileBrowser(page, token);

  // Trigger a backup so there is at least one history row.
  await page.click('#backup-now-btn');
  await page.waitForTimeout(2_000);

  await page.click('#history-nav-btn');
  await expect(page.locator('.history-card')).toBeVisible({ timeout: 5_000 });
  // At least one history-item row should be visible.
  await expect(page.locator('.history-item').first()).toBeVisible({ timeout: 5_000 });
});
