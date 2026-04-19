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

  // Accept the confirm() dialog that the Remove button triggers.
  page.on('dialog', dialog => dialog.accept());

  await page.click('.remove-folder-btn');

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
