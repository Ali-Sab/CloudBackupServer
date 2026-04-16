/**
 * E2E smoke tests for the Cloud Backup Electron desktop app.
 *
 * Prerequisites: docker compose up (backend + postgres + minio must be running).
 * Run with:  make test-e2e
 *        or  cd frontend && npm run test:e2e
 *        or  npm run test:e2e:headed  (to watch the app window)
 *
 * Tests 2–5 skip gracefully when the backend is not running.
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

// ---- Helpers -----------------------------------------------------------------

/** Create a real temp directory containing a couple of files for upload tests. */
function makeTempBackupDir() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'e2e-backup-'));
  fs.writeFileSync(path.join(dir, 'hello.txt'), 'hello from e2e test');
  fs.writeFileSync(path.join(dir, 'data.bin'), Buffer.alloc(256, 0xab));
  return dir;
}

/** Launch the Electron app, pointing E2E_SELECT_DIR at tmpDir. */
function launchApp(tmpDir) {
  return electron.launch({
    args: [MAIN_JS],
    env: {
      ...process.env,
      NODE_ENV: 'test',
      E2E_SELECT_DIR: tmpDir || '',
    },
  });
}

/** HTTP POST helper (Node built-in — no extra deps). */
function httpPost(urlPath, body, authToken) {
  return new Promise((resolve, reject) => {
    const data   = JSON.stringify(body);
    const parsed = new URL(`${BASE_URL}${urlPath}`);
    const headers = {
      'Content-Type': 'application/json',
      'Content-Length': Buffer.byteLength(data),
    };
    if (authToken) headers['Authorization'] = `Bearer ${authToken}`;
    const req = http.request(
      { hostname: parsed.hostname, port: Number(parsed.port) || 80,
        path: parsed.pathname, method: 'POST', headers },
      (res) => {
        let raw = '';
        res.on('data', c => { raw += c; });
        res.on('end', () => resolve({ status: res.statusCode, body: JSON.parse(raw) }));
      }
    );
    req.on('error', reject);
    req.write(data);
    req.end();
  });
}

/** HTTP PUT helper (Node built-in). */
function httpPut(urlPath, body, authToken) {
  return new Promise((resolve, reject) => {
    const data   = JSON.stringify(body);
    const parsed = new URL(`${BASE_URL}${urlPath}`);
    const headers = {
      'Content-Type': 'application/json',
      'Content-Length': Buffer.byteLength(data),
    };
    if (authToken) headers['Authorization'] = `Bearer ${authToken}`;
    const req = http.request(
      { hostname: parsed.hostname, port: Number(parsed.port) || 80,
        path: parsed.pathname, method: 'PUT', headers },
      (res) => {
        let raw = '';
        res.on('data', c => { raw += c; });
        res.on('end', () => resolve({ status: res.statusCode, body: JSON.parse(raw) }));
      }
    );
    req.on('error', reject);
    req.write(data);
    req.end();
  });
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
  // auth.js starts with .loading and transitions once the session fetch settles.
  // We wait for that transition — app must not crash or hang.
  await page.waitForSelector('#session-status:not(.loading)', { timeout: 12_000 });

  const sessionStatus = page.locator('#session-status');
  await expect(sessionStatus).toBeVisible();

  // Must be logged-out (backend up → login form) or error (backend down → retry card).
  const classes = await sessionStatus.getAttribute('class');
  const isLoggedOut = classes.includes('logged-out');
  const isError     = classes.includes('error');
  expect(isLoggedOut || isError).toBe(true);
});

// ---- T2: Login ---------------------------------------------------------------

test('T2: login with valid credentials shows file browser', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  await page.waitForSelector('#login-form', { timeout: 10_000 });

  await page.fill('#email',    process.env.E2E_TEST_EMAIL);
  await page.fill('#password', process.env.E2E_TEST_PASSWORD);
  await page.click('#login-form button[type="submit"]');

  // Successful login renders the "Welcome back" card with a Sign Out button.
  await page.waitForSelector('#logout-btn', { timeout: 10_000 });
  await expect(page.locator('#logout-btn')).toHaveText('Sign Out');

  // File browser scaffold must be visible.
  await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });
  await expect(page.locator('#select-dir-btn')).toBeVisible();
  await expect(page.locator('#backup-now-btn')).toBeVisible();

  // Backup Now is disabled until a folder is selected.
  await expect(page.locator('#backup-now-btn')).toBeDisabled();
});

// ---- T3: Saved path restored on login ---------------------------------------

test('T3: saved watched path is restored on session restore', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  // Register a fresh user so this test has its own isolated state.
  const ts  = Date.now();
  const reg = await httpPost('/api/auth/register', {
    email:    `e2e_path_${ts}@test.example`,
    password: `PathPass_${ts}!`,
  });
  expect(reg.status).toBe(201);
  const token = reg.body.access_token;

  // Pre-set the watched path via the API — no UI interaction needed.
  const put = await httpPut('/api/files/path', { path: tmpDir }, token);
  expect(put.status).toBe(200);

  // Log in as this user via the UI.
  await page.waitForSelector('#login-form', { timeout: 10_000 });
  await page.fill('#email',    `e2e_path_${ts}@test.example`);
  await page.fill('#password', `PathPass_${ts}!`);
  await page.click('#login-form button[type="submit"]');
  await page.waitForSelector('#logout-btn', { timeout: 10_000 });

  // files.js show() calls GET /api/files/path and populates #current-path.
  await page.waitForFunction(
    (expected) => {
      const el = document.getElementById('current-path');
      return el && el.textContent.trim() === expected;
    },
    tmpDir,
    { timeout: 8_000 }
  );

  const pathText = await page.locator('#current-path').textContent();
  expect(pathText.trim()).toBe(tmpDir);

  // Backup Now must be enabled (saved path is set).
  await expect(page.locator('#backup-now-btn')).not.toBeDisabled();
});

// ---- T4: Backup Now ----------------------------------------------------------

test('T4: Backup Now uploads files and shows result toast', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  // Log in.
  await page.waitForSelector('#login-form', { timeout: 10_000 });
  await page.fill('#email',    process.env.E2E_TEST_EMAIL);
  await page.fill('#password', process.env.E2E_TEST_PASSWORD);
  await page.click('#login-form button[type="submit"]');
  await page.waitForSelector('#logout-btn', { timeout: 10_000 });
  await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });

  // Click "Select Folder" — E2E_SELECT_DIR bypasses the OS dialog and
  // returns tmpDir (which contains hello.txt and data.bin).
  await page.click('#select-dir-btn');

  // Wait for #current-path to reflect the selected directory.
  await page.waitForFunction(
    (expected) => {
      const el = document.getElementById('current-path');
      return el && el.textContent.trim() === expected;
    },
    tmpDir,
    { timeout: 8_000 }
  );

  await expect(page.locator('#backup-now-btn')).not.toBeDisabled();

  // Trigger backup.
  await page.click('#backup-now-btn');

  // Button shows "Backing up…" while in progress, then reverts.
  // A toast appears with the backup summary from buildBackupSummary().
  // Allow up to 20 s for real file uploads over local HTTP.
  await page.waitForSelector('.toast-visible', { timeout: 20_000 });

  const toastText = await page.locator('.toast-visible').first().textContent();
  expect(toastText).toMatch(/Backup:/);

  // Button must revert to its original label once done.
  await expect(page.locator('#backup-now-btn')).toHaveText('Backup Now', { timeout: 8_000 });
});

// ---- T5: Logout --------------------------------------------------------------

test('T5: logout clears session and returns to login form', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  // Log in.
  await page.waitForSelector('#login-form', { timeout: 10_000 });
  await page.fill('#email',    process.env.E2E_TEST_EMAIL);
  await page.fill('#password', process.env.E2E_TEST_PASSWORD);
  await page.click('#login-form button[type="submit"]');
  await page.waitForSelector('#logout-btn', { timeout: 10_000 });

  // Click Sign Out.
  await page.click('#logout-btn');

  // auth.js calls checkSession() after logout — which returns logged-out
  // and renders the login form.
  await page.waitForSelector('#login-form', { timeout: 10_000 });
  await expect(page.locator('#login-form')).toBeVisible();

  // File browser must be hidden.
  await expect(page.locator('#file-browser')).toHaveClass(/hidden/, { timeout: 5_000 });

  // Sign Out button must be gone.
  await expect(page.locator('#logout-btn')).not.toBeVisible();
});
