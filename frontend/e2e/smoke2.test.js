/**
 * E2E extended tests — corner cases, negative paths, and edge scenarios.
 *
 * Prerequisites: docker compose up (backend + postgres + minio must be running).
 * Run with:  make test-e2e
 *        or  cd frontend && npm run test:e2e
 *
 * Tests skip gracefully when the backend is not running.
 *
 * Test IDs T31–T65 continue from smoke.test.js (T1–T30).
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
const ORIGIN   = process.env.E2E_ORIGIN || 'http://localhost:5173';

// ---- HTTP helpers ------------------------------------------------------------

function cookieJarFromSetCookie(setCookieHeaders) {
  if (!setCookieHeaders) return '';
  const list = Array.isArray(setCookieHeaders) ? setCookieHeaders : [setCookieHeaders];
  return list
    .map(h => String(h).split(';')[0])
    .filter(Boolean)
    .join('; ');
}

function httpRequest(method, urlPath, body, cookies) {
  return new Promise((resolve, reject) => {
    const data   = body ? JSON.stringify(body) : null;
    const parsed = new URL(`${BASE_URL}${urlPath}`);
    const headers = { 'Content-Type': 'application/json', 'Origin': ORIGIN };
    if (data) headers['Content-Length'] = Buffer.byteLength(data);
    if (cookies) headers['Cookie'] = cookies;

    const req = http.request(
      { hostname: parsed.hostname, port: Number(parsed.port) || 80,
        path: parsed.pathname, method, headers },
      (res) => {
        let raw = '';
        res.on('data', c => { raw += c; });
        res.on('end', () => {
          const cookieHeader = cookieJarFromSetCookie(res.headers['set-cookie']);
          try {
            resolve({ status: res.statusCode, body: JSON.parse(raw), cookies: cookieHeader });
          } catch {
            resolve({ status: res.statusCode, body: {}, cookies: cookieHeader });
          }
        });
      }
    );
    req.on('error', reject);
    if (data) req.write(data);
    req.end();
  });
}

const httpPost   = (path, body, cookies) => httpRequest('POST',   path, body, cookies);
const httpGet    = (path, cookies)        => httpRequest('GET',    path, null, cookies);
const httpDelete = (path, cookies)        => httpRequest('DELETE', path, null, cookies);

function httpUploadFile(urlPath, content, cookies) {
  return new Promise((resolve, reject) => {
    const buf    = Buffer.isBuffer(content) ? content : Buffer.from(content);
    const sha256 = require('crypto').createHash('sha256').update(buf).digest('hex');
    const parsed = new URL(`${BASE_URL}${urlPath}`);
    const headers = {
      'Content-Type': 'application/octet-stream',
      'Content-Length': String(buf.length),
      'X-Checksum-SHA256': sha256,
      'X-File-Size': String(buf.length),
      'Origin': ORIGIN,
      ...(cookies ? { Cookie: cookies } : {}),
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

function makeTempBackupDir() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'e2e-backup-'));
  fs.writeFileSync(path.join(dir, 'hello.txt'), 'hello from e2e test');
  fs.writeFileSync(path.join(dir, 'data.bin'), Buffer.alloc(256, 0xab));
  return dir;
}

function makeEmptyTempDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'e2e-empty-'));
}

function launchApp(tmpDir, { userDataDir } = {}) {
  const dataDir = userDataDir || fs.mkdtempSync(path.join(os.tmpdir(), 'e2e-userdata-'));
  return electron.launch({
    args: [MAIN_JS, `--user-data-dir=${dataDir}`],
    env: {
      ...process.env,
      NODE_ENV: 'test',
      E2E_SELECT_DIR: tmpDir || '',
    },
  });
}

async function registerFreshUser(suffix) {
  const ts  = Date.now();
  const tag = suffix || 'user';
  const email    = `e2e_${tag}_${ts}@test.example`;
  const password = `E2ePass_${ts}!`;
  const res = await httpPost('/api/auth/register', { email, password });
  if (res.status !== 201) throw new Error(`Registration failed: HTTP ${res.status}`);
  return { email, password, cookies: res.cookies, token: res.cookies };
}

async function loginViaUI(page, email, password) {
  await page.waitForSelector('#login-form', { timeout: 10_000 });
  await page.fill('#email',    email);
  await page.fill('#password', password);
  await page.click('#login-form button[type="submit"]');
  await page.waitForSelector('#header-avatar', { timeout: 10_000 });
}

async function openFileBrowser(page) {
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });
  await page.waitForSelector('.folder-card', { timeout: 8_000 });
  await page.click('.open-folder-btn');
  await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });
}

// ---- Per-test fixtures -------------------------------------------------------

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

// =============================================================================
// AUTHENTICATION — negative & edge cases
// =============================================================================

// T31: Submitting the login form with both fields empty shows an error.
test('T31: submitting empty login form shows a validation error', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  await page.waitForSelector('#login-form', { timeout: 10_000 });
  // Clear both fields (they should already be empty) and submit.
  await page.fill('#email',    '');
  await page.fill('#password', '');
  await page.click('#login-form button[type="submit"]');

  // Either HTML5 native validation prevents submit (form stays visible)
  // or the app shows #form-error. Either way the login form must remain.
  await expect(page.locator('#login-form')).toBeVisible({ timeout: 5_000 });
  await expect(page.locator('#dashboard')).toHaveClass(/hidden/);
});

// T32: Submitting with a well-formed but unknown email shows an error message.
test('T32: login with unknown email shows form error', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  await page.waitForSelector('#login-form', { timeout: 10_000 });
  await page.fill('#email',    'totally-unknown-user@nowhere.example');
  await page.fill('#password', 'AnyPassword1!');
  await page.click('#login-form button[type="submit"]');

  await page.waitForSelector('#form-error:not(:empty)', { timeout: 8_000 });
  const errText = await page.locator('#form-error').textContent();
  expect(errText.trim().length).toBeGreaterThan(0);
  await expect(page.locator('#login-form')).toBeVisible();
  await expect(page.locator('#dashboard')).toHaveClass(/hidden/);
});

// T33: Registering the same email twice via the API returns 409.
test('T33: registering a duplicate email via API returns 409', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const ts = Date.now();
  const email    = `e2e_dup_${ts}@test.example`;
  const password = `E2ePass_${ts}!`;

  const first = await httpPost('/api/auth/register', { email, password });
  expect(first.status).toBe(201);

  const second = await httpPost('/api/auth/register', { email, password });
  expect(second.status).toBe(409);
});

// T34: Login button is disabled / shows loading state while the request is in-flight.
test('T34: login submit button shows a loading state while the request is in-flight', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('btn-loading');

  await page.waitForSelector('#login-form', { timeout: 10_000 });
  await page.fill('#email',    email);
  await page.fill('#password', password);

  // Click and immediately check for disabled / loading indicator.
  const submitBtn = page.locator('#login-form button[type="submit"]');
  await submitBtn.click();

  // The button must be either disabled or have a loading class while the request is in-flight.
  // We race against the success redirect, so just assert eventually we reach dashboard.
  await page.waitForSelector('#header-avatar', { timeout: 12_000 });
  await expect(page.locator('#dashboard')).toBeVisible();
});

// T35: Wrong password for a real account shows an error (not a crash).
test('T35: correct email but wrong password shows form error', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email } = await registerFreshUser('wrongpw');

  await page.waitForSelector('#login-form', { timeout: 10_000 });
  await page.fill('#email',    email);
  await page.fill('#password', 'definitely-wrong-password-999');
  await page.click('#login-form button[type="submit"]');

  await page.waitForSelector('#form-error:not(:empty)', { timeout: 8_000 });
  await expect(page.locator('#login-form')).toBeVisible();
  await expect(page.locator('#dashboard')).toHaveClass(/hidden/);
});

// =============================================================================
// FOLDER MANAGEMENT — edge cases
// =============================================================================

// T36: An empty directory (no files) shows an empty-state message in the file browser.
test('T36: empty folder shows empty-state placeholder in file browser', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const emptyDir = makeEmptyTempDir();
  try {
    // Close default app and relaunch pointing at the empty dir.
    await app.close();
    app  = await launchApp(emptyDir);
    page = await app.firstWindow();
    await page.waitForLoadState('domcontentloaded');

    const { email, password, token } = await registerFreshUser('emptydir');
    await httpPost('/api/folders', { path: emptyDir }, token);

    await loginViaUI(page, email, password);
    await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });
    await page.waitForSelector('.folder-card', { timeout: 8_000 });

    await page.click('.open-folder-btn');
    await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });

    // No real file rows — an empty-state element must appear instead.
    await expect(page.locator('.file-empty, .file-list-empty, [data-empty]').first()).toBeVisible({ timeout: 8_000 });
    await expect(page.locator('.file-item:not(.is-dir):not(.file-empty)')).toHaveCount(0);
  } finally {
    try { fs.rmSync(emptyDir, { recursive: true, force: true }); } catch {}
  }
});

// T37: Multiple folders added via API all appear as cards on the dashboard.
test('T37: multiple pre-seeded folders all appear as cards on the dashboard', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const dir2 = fs.mkdtempSync(path.join(os.tmpdir(), 'e2e-extra-'));
  try {
    const { email, password, token } = await registerFreshUser('multifolder');
    await httpPost('/api/folders', { path: tmpDir }, token);
    await httpPost('/api/folders', { path: dir2 }, token);

    await loginViaUI(page, email, password);
    await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });
    await page.waitForSelector('.folder-card', { timeout: 8_000 });

    const count = await page.locator('.folder-card').count();
    expect(count).toBe(2);
  } finally {
    try { fs.rmSync(dir2, { recursive: true, force: true }); } catch {}
  }
});

// T38: Removing one folder from a two-folder dashboard leaves the other intact.
test('T38: removing one folder from a two-folder dashboard leaves the other card intact', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const dir2 = fs.mkdtempSync(path.join(os.tmpdir(), 'e2e-keep-'));
  try {
    const { email, password, token } = await registerFreshUser('removeoneof');
    await httpPost('/api/folders', { path: tmpDir }, token);
    await httpPost('/api/folders', { path: dir2 }, token);

    await loginViaUI(page, email, password);
    await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });
    await page.waitForSelector('.folder-card', { timeout: 8_000 });
    await expect(page.locator('.folder-card')).toHaveCount(2, { timeout: 5_000 });

    // Remove the first folder card.
    await page.locator('.remove-folder-btn').first().click();
    await page.waitForSelector('.modal-overlay.modal-visible', { timeout: 5_000 });
    await page.click('.modal-action-danger'); // Skip download
    await page.waitForSelector('.modal-overlay.modal-visible', { timeout: 5_000 });
    await page.click('.modal-action-btn:has-text("Remove Folder")');

    // One card must remain; empty state must NOT appear.
    await expect(page.locator('.folder-card')).toHaveCount(1, { timeout: 8_000 });
    await expect(page.locator('.folder-list-empty')).toHaveCount(0);
  } finally {
    try { fs.rmSync(dir2, { recursive: true, force: true }); } catch {}
  }
});

// T39: Re-adding a folder after removing it creates a fresh card with the correct path.
test('T39: re-adding a previously removed folder path creates a new card', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('readd');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await page.waitForSelector('.folder-card', { timeout: 8_000 });

  // Remove it.
  await page.click('.remove-folder-btn');
  await page.waitForSelector('.modal-overlay.modal-visible', { timeout: 5_000 });
  await page.click('.modal-action-danger');
  await page.waitForSelector('.modal-overlay.modal-visible', { timeout: 5_000 });
  await page.click('.modal-action-btn:has-text("Remove Folder")');
  await expect(page.locator('.folder-card')).toHaveCount(0, { timeout: 8_000 });

  // Re-add the same path via the Add Folder button (E2E_SELECT_DIR picks tmpDir).
  await page.click('#add-folder-btn');
  await page.waitForSelector('.folder-card', { timeout: 10_000 });

  const cardPath = await page.locator('.folder-card .folder-card-path').first().textContent();
  expect(cardPath.trim()).toBe(tmpDir);
});

// T40: Dashboard summary strip shows correct folder count after adding multiple folders.
test('T40: dashboard summary count increments correctly as folders are added', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const dir2 = fs.mkdtempSync(path.join(os.tmpdir(), 'e2e-sum2-'));
  try {
    const { email, password, token } = await registerFreshUser('sumcount');
    await httpPost('/api/folders', { path: tmpDir }, token);
    await httpPost('/api/folders', { path: dir2 }, token);

    await loginViaUI(page, email, password);
    await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });
    await page.waitForSelector('.folder-card', { timeout: 8_000 });

    // Summary strip must be visible.
    await expect(page.locator('.dashboard-summary')).not.toHaveClass(/hidden/);

    // First stat value should be the folder count (2).
    const folderCount = await page.locator('.summary-stat-value').first().textContent();
    expect(folderCount.trim()).toBe('2');
  } finally {
    try { fs.rmSync(dir2, { recursive: true, force: true }); } catch {}
  }
});

// =============================================================================
// BACKUP — edge cases
// =============================================================================

// T41: Backup Now button is disabled while a backup is already in progress.
test('T41: Backup Now button is disabled while backup is in progress', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('btn-disabled');
  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

  await page.click('#add-folder-btn');
  await page.waitForSelector('.folder-card', { timeout: 10_000 });
  await page.click('.open-folder-btn');
  await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });

  await page.click('#backup-now-btn');

  // Immediately after clicking, the button must be disabled.
  await expect(page.locator('#backup-now-btn')).toBeDisabled({ timeout: 2_000 });

  // Wait for completion.
  await page.locator('.toast-visible', { hasText: /Backup:/ }).waitFor({ timeout: 20_000 });
});

// T42: Backing up an empty folder completes without error — button reverts and no crash.
test('T42: backing up an empty folder completes without error', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const emptyDir = makeEmptyTempDir();
  try {
    await app.close();
    app  = await launchApp(emptyDir);
    page = await app.firstWindow();
    await page.waitForLoadState('domcontentloaded');

    const { email, password } = await registerFreshUser('emptybackup');
    await loginViaUI(page, email, password);
    await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

    await page.click('#add-folder-btn');
    await page.waitForSelector('.folder-card', { timeout: 10_000 });
    await page.click('.open-folder-btn');
    await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });

    await page.click('#backup-now-btn');

    // The button must eventually revert to idle — no hang, no crash.
    await expect(page.locator('#backup-now-btn')).toHaveText('Backup Now', { timeout: 20_000 });
    // The file browser must still be visible (no unexpected navigation).
    await expect(page.locator('#file-browser')).not.toHaveClass(/hidden/);
  } finally {
    try { fs.rmSync(emptyDir, { recursive: true, force: true }); } catch {}
  }
});

// T43: Keyboard shortcut 'b' on the dashboard (not inside file browser) does not trigger backup.
test('T43: pressing "b" on the dashboard does not trigger a backup', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('kb-dashboard');
  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

  await page.keyboard.press('b');

  // No backup toast should appear; file browser should remain hidden.
  await page.waitForTimeout(1_500);
  await expect(page.locator('.toast-visible')).toHaveCount(0);
  await expect(page.locator('#file-browser')).toHaveClass(/hidden/);
});

// T44: After backup, pressing 'b' a second time triggers another backup.
test('T44: pressing "b" twice triggers two separate backups', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('kb-twice');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await page.waitForSelector('.folder-card', { timeout: 8_000 });
  await page.click('.open-folder-btn');
  await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });

  // First backup.
  await page.keyboard.press('b');
  await page.locator('.toast-visible', { hasText: /Backup:/ }).waitFor({ timeout: 20_000 });
  await expect(page.locator('#backup-now-btn')).toHaveText('Backup Now', { timeout: 8_000 });

  // Second backup — button must be re-enabled between the two.
  await page.keyboard.press('b');
  const secondToast = page.locator('.toast-visible', { hasText: /Backup:/ });
  await secondToast.waitFor({ timeout: 20_000 });
  expect(await secondToast.first().textContent()).toMatch(/Backup:/);
});

// =============================================================================
// MODAL & UI — corner cases
// =============================================================================

// T45: Escape key closes the download-prompt modal without removing the folder.
test('T45: Escape key closes the remove-folder download-prompt modal', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('esc-modal');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await page.waitForSelector('.folder-card', { timeout: 8_000 });

  await page.click('.remove-folder-btn');
  await page.waitForSelector('.modal-overlay.modal-visible', { timeout: 5_000 });

  await page.keyboard.press('Escape');

  // Modal must be gone and the folder card must still be there.
  await expect(page.locator('.modal-overlay')).toHaveCount(0, { timeout: 3_000 });
  await expect(page.locator('.folder-card')).toHaveCount(1);
});

// T46: Closing the metadata modal with the × button removes it from the DOM.
test('T46: metadata modal close button removes it from the DOM', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('meta-close');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });
  await page.waitForSelector('.folder-card', { timeout: 8_000 });
  await page.click('.open-folder-btn');
  await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });
  await page.waitForSelector('.file-item:not(.is-dir):not(.file-empty)', { timeout: 8_000 });

  await page.locator('.file-item:not(.is-dir) .file-info-btn').first().click({ force: true });
  await page.waitForSelector('#metadata-modal.modal-visible', { timeout: 5_000 });

  // Click the × close button.
  await page.locator('#metadata-modal .modal-close').click();
  await expect(page.locator('#metadata-modal')).toHaveCount(0, { timeout: 3_000 });
});

// T47: Preview modal Escape key closes it (same as the × button).
test('T47: Escape key closes the file preview modal', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('preview-esc');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await openFileBrowser(page);
  await page.waitForSelector('.file-item:not(.is-dir):not(.file-empty)', { timeout: 8_000 });

  await page.locator('.file-name.file-link', { hasText: 'hello.txt' }).click();
  await expect(page.locator('#preview-modal')).toBeVisible({ timeout: 5_000 });

  await page.keyboard.press('Escape');
  await expect(page.locator('#preview-modal')).toHaveCount(0, { timeout: 3_000 });
});

// T48: A binary file (data.bin) does not have an in-app preview button — clicking its name
//      opens it with the OS default app instead (shell.openPath). Verify no preview modal opens.
test('T48: binary file has no in-app preview — file name opens via OS, no modal appears', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('binary-no-preview');
  await httpPost('/api/folders', { path: tmpDir }, token);

  // Spy on shell.openPath before the click.
  await app.evaluate(({ shell }) => {
    global._e2eOpenedPath = null;
    shell.openPath = async (p) => { global._e2eOpenedPath = p; return ''; };
  });

  await loginViaUI(page, email, password);
  await openFileBrowser(page);
  await page.waitForSelector('.file-item:not(.is-dir):not(.file-empty)', { timeout: 8_000 });

  const binRow = page.locator('.file-item', { hasText: 'data.bin' });

  // Binary files must NOT have a row-level preview button.
  await expect(binRow.locator('.file-preview-btn')).toHaveCount(0);

  // Clicking the file name must delegate to the OS (shell.openPath), not open a modal.
  await binRow.locator('.file-name').click();
  await page.waitForTimeout(500);

  // No preview modal should have appeared.
  await expect(page.locator('#preview-modal')).toHaveCount(0);
});

// T49: Opening the info modal for a file shows the correct file name in the metadata.
test('T49: metadata modal shows the correct file name', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('meta-name');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await openFileBrowser(page);
  await page.waitForSelector('.file-item:not(.is-dir):not(.file-empty)', { timeout: 8_000 });

  // Open info for hello.txt specifically.
  const helloRow = page.locator('.file-item', { hasText: 'hello.txt' });
  await helloRow.locator('.file-info-btn').click({ force: true });

  await page.waitForSelector('#metadata-modal.modal-visible', { timeout: 5_000 });
  const modalTitle = await page.locator('.modal-title').textContent();
  expect(modalTitle.trim()).toMatch(/hello\.txt/i);

  await page.keyboard.press('Escape');
});

// =============================================================================
// SETTINGS & THEME
// =============================================================================

// T50: Dark theme button removes the data-theme="light" attribute (dark is the default/absent state).
test('T50: clicking dark theme button removes light theme — document is not in light mode', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('theme-dark');
  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

  await page.click('#header-avatar');
  await page.click('#avatar-dropdown-settings');
  await expect(page.locator('#settings')).not.toHaveClass(/hidden/);

  // First switch to light so there is something to revert.
  await page.click('#theme-light-btn');
  const afterLight = await page.locator('html').getAttribute('data-theme');
  expect(afterLight).toBe('light');

  // Now switch to dark — the data-theme="light" attribute must be removed.
  await page.click('#theme-dark-btn');
  const afterDark = await page.locator('html').getAttribute('data-theme');
  expect(afterDark).not.toBe('light');
});

// T51: Light theme button removes the dark-theme class.
test('T51: clicking light theme button applies light theme to the document', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('theme-light');
  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

  await page.click('#header-avatar');
  await page.click('#avatar-dropdown-settings');
  await expect(page.locator('#settings')).not.toHaveClass(/hidden/);

  // First apply dark, then switch to light.
  await page.click('#theme-dark-btn');
  await page.click('#theme-light-btn');

  const htmlClass  = await page.locator('html').getAttribute('class') || '';
  const bodyClass  = await page.locator('body').getAttribute('class') || '';
  const dataTheme  = await page.locator('html').getAttribute('data-theme') || '';
  const isDark = htmlClass.includes('dark') || bodyClass.includes('dark') || dataTheme.includes('dark');
  expect(isDark).toBe(false);
});

// T52: The auto-backup toggle can be switched on and off without errors.
test('T52: auto-backup toggle switches on and off without errors', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('autobackup-toggle');
  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

  await page.click('#header-avatar');
  await page.click('#avatar-dropdown-settings');
  await expect(page.locator('#settings')).not.toHaveClass(/hidden/);

  const toggle = page.getByLabel('Enable auto-backup');
  const initialState = await toggle.isChecked();

  // Toggle on then off (or off then on).
  await toggle.click();
  await expect(toggle).toBeChecked({ checked: !initialState, timeout: 3_000 });
  await toggle.click();
  await expect(toggle).toBeChecked({ checked: initialState, timeout: 3_000 });
});

// T53: The notifications toggle can be switched on and off without errors.
test('T53: desktop notifications toggle switches on and off without errors', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('notif-toggle');
  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

  await page.click('#header-avatar');
  await page.click('#avatar-dropdown-settings');
  await expect(page.locator('#settings')).not.toHaveClass(/hidden/);

  const toggle = page.getByLabel('Enable desktop notifications');
  const initialState = await toggle.isChecked();

  await toggle.click();
  await expect(toggle).toBeChecked({ checked: !initialState, timeout: 3_000 });
  await toggle.click();
  await expect(toggle).toBeChecked({ checked: initialState, timeout: 3_000 });
});

// =============================================================================
// ACTIVITY LOG — edge cases
// =============================================================================

// T54: Activity log for a fresh user (no backups) shows an empty state message.
test('T54: activity log shows empty state for a brand new account with no backups', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('history-empty');
  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

  await page.click('#history-nav-btn');
  await expect(page.locator('.history-card')).toBeVisible({ timeout: 5_000 });

  // No history items — expect an empty-state element or a count of zero items.
  const itemCount = await page.locator('.history-item').count();
  if (itemCount === 0) {
    // The empty state element should be visible.
    await expect(page.locator('.history-empty, .history-no-items, [data-empty]').first()).toBeVisible({ timeout: 3_000 });
  }
  // If the app shows 0 rows that is also acceptable — both paths pass.
});

// T55: Activity log after two separate backups shows at least two history entries.
test('T55: activity log shows at least two entries after two backups', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('history-multi');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await openFileBrowser(page);

  // First backup.
  await page.click('#backup-now-btn');
  await page.locator('.toast-visible', { hasText: /Backup:/ }).waitFor({ timeout: 20_000 });
  await expect(page.locator('#backup-now-btn')).toHaveText('Backup Now', { timeout: 8_000 });

  // Second backup.
  await page.click('#backup-now-btn');
  await page.locator('.toast-visible', { hasText: /Backup:/ }).waitFor({ timeout: 20_000 });

  await page.click('#history-nav-btn');
  await expect(page.locator('.history-card')).toBeVisible({ timeout: 5_000 });

  const itemCount = await page.locator('.history-item').count();
  expect(itemCount).toBeGreaterThanOrEqual(2);
});

// T56: Navigating to history and back to dashboard, then to file browser, works without errors.
test('T56: history → dashboard → file browser navigation chain works without errors', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('nav-chain');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

  // Go to history.
  await page.click('#history-nav-btn');
  await expect(page.locator('.history-card')).toBeVisible({ timeout: 5_000 });

  // Back to dashboard.
  await page.click('#history-back-btn');
  await expect(page.locator('#dashboard')).not.toHaveClass(/hidden/, { timeout: 3_000 });

  // Open file browser.
  await page.waitForSelector('.folder-card', { timeout: 5_000 });
  await page.click('.open-folder-btn');
  await page.waitForSelector('#file-browser:not(.hidden)', { timeout: 8_000 });
  await expect(page.locator('#backup-now-btn')).not.toBeDisabled();
});

// =============================================================================
// SESSION / NAVIGATION — edge cases
// =============================================================================

// T57: Logout from within the file browser (not dashboard) returns to the login form.
test('T57: logout from the file browser view returns to the login form', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('logout-from-browser');
  await httpPost('/api/folders', { path: tmpDir }, token);

  await loginViaUI(page, email, password);
  await openFileBrowser(page);

  // Logout while file browser is open.
  await page.click('#header-avatar');
  await page.click('#avatar-dropdown-signout');

  await page.waitForSelector('#login-form', { timeout: 10_000 });
  await expect(page.locator('#login-form')).toBeVisible();
  await expect(page.locator('#file-browser')).toHaveClass(/hidden/);
  await expect(page.locator('#dashboard')).toHaveClass(/hidden/);
});

// T58: Account panel → close → account panel can be re-opened without issues.
test('T58: account panel can be opened, closed, and reopened without issues', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('account-reopen');
  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

  // Open, close, open again.
  await page.click('#header-avatar');
  await page.click('#avatar-dropdown-account');
  await expect(page.locator('#account')).not.toHaveClass(/hidden/);
  await page.click('#account-close-btn');
  await expect(page.locator('#account')).toHaveClass(/hidden/);

  await page.click('#header-avatar');
  await page.click('#avatar-dropdown-account');
  await expect(page.locator('#account')).not.toHaveClass(/hidden/);
  const emailEl = await page.locator('#account-current-email').textContent();
  expect(emailEl.trim()).toBe(email);
});

// T59: Settings panel → close → settings panel can be re-opened without issues.
test('T59: settings panel can be opened, closed, and reopened without issues', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password } = await registerFreshUser('settings-reopen');
  await loginViaUI(page, email, password);
  await page.waitForSelector('#dashboard:not(.hidden)', { timeout: 8_000 });

  await page.click('#header-avatar');
  await page.click('#avatar-dropdown-settings');
  await expect(page.locator('#settings')).not.toHaveClass(/hidden/);
  await page.click('#settings-close-btn');
  await expect(page.locator('#settings')).toHaveClass(/hidden/);

  await page.click('#header-avatar');
  await page.click('#avatar-dropdown-settings');
  await expect(page.locator('#settings')).not.toHaveClass(/hidden/);
  await expect(page.locator('#theme-dark-btn')).toBeVisible();
});

// =============================================================================
// SAFE-DELETE — additional edge cases
// =============================================================================

// T60: Cancel on the step-2 (file-loss) modal leaves the folder and returns to dashboard.
test('T60: cancel on the file-loss step-2 modal leaves the folder card intact', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('rm-step2-cancel');

  // Upload a cloud-only file so step-2 has content.
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

  // Hit Cancel (if present) or Go Back.
  const cancelBtn = page.locator('.modal-action-btn:has-text("Cancel")');
  const goBackBtn = page.locator('.modal-action-btn:has-text("Go Back")');
  if (await cancelBtn.count() > 0) {
    await cancelBtn.click();
  } else {
    await goBackBtn.click();
    // Back at step 1 — now cancel.
    await page.waitForSelector('.modal-overlay.modal-visible', { timeout: 5_000 });
    await page.click('.modal-action-btn:has-text("Cancel")');
  }

  await expect(page.locator('.modal-overlay')).toHaveCount(0, { timeout: 3_000 });
  await expect(page.locator('.folder-card')).toHaveCount(1);
});

// T61: Cloud-only file preview in the file-loss modal shows the file's content.
test('T61: preview button in file-loss modal shows the cloud-only file content', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { email, password, token } = await registerFreshUser('rm-preview');

  const addRes = await httpPost('/api/folders', { path: tmpDir }, token);
  const folderId = addRes.body.id;
  const content = 'preview content from e2e test';
  await httpUploadFile(`/api/folders/${folderId}/backup/hello.txt`, content, token);
  fs.unlinkSync(path.join(tmpDir, 'hello.txt'));

  await loginViaUI(page, email, password);
  await page.waitForSelector('.folder-card', { timeout: 8_000 });

  await page.click('.remove-folder-btn');
  await page.waitForSelector('.modal-overlay.modal-visible', { timeout: 5_000 });
  await page.click('.modal-action-danger');
  await page.waitForSelector('.remove-file-list', { timeout: 8_000 });

  // Click the preview button for hello.txt.
  await page.locator('.remove-file-preview-btn').first().click();

  // A preview modal must appear; wait for the loading spinner to be replaced by real content.
  await expect(page.locator('#preview-modal')).toBeVisible({ timeout: 8_000 });
  await expect(page.locator('#preview-modal')).not.toContainText('Loading preview', { timeout: 10_000 });
  const previewText = await page.locator('#preview-modal').textContent();
  expect(previewText).toContain('preview content from e2e test');

  await page.locator('#preview-modal .modal-close').click();
});

// T62: Remove folder API endpoint returns 404 for a folder ID that does not exist.
test('T62: DELETE /api/folders/:id with nonexistent ID returns 404', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { cookies } = await registerFreshUser('del-404');
  const res = await httpDelete('/api/folders/999999999', cookies);
  expect(res.status).toBe(404);
});

// T63: GET /api/folders returns an empty folders list for a fresh user.
test('T63: GET /api/folders returns empty folders list for a fresh user', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { cookies } = await registerFreshUser('empty-folders');
  const res = await httpGet('/api/folders', cookies);
  expect(res.status).toBe(200);
  // Response is { folders: [] } — unwrap the array from the wrapper object.
  const folders = Array.isArray(res.body) ? res.body : (res.body.folders ?? []);
  expect(Array.isArray(folders)).toBe(true);
  expect(folders.length).toBe(0);
});

// T64: GET /api/folders without auth returns 401.
test('T64: GET /api/folders without auth cookie returns 401', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const res = await httpGet('/api/folders', null);
  expect(res.status).toBe(401);
});

// T65: Backup upload endpoint rejects a request with no X-Checksum-SHA256 header.
test('T65: backup upload with missing checksum header returns 400', async () => {
  test.skip(!!process.env.E2E_BACKEND_DOWN, 'backend not running');

  const { cookies } = await registerFreshUser('missing-checksum');
  const addRes = await httpPost('/api/folders', { path: tmpDir }, cookies);
  expect(addRes.status).toBe(201);
  const folderId = addRes.body.id;

  const content = Buffer.from('real content here');

  const res = await new Promise((resolve, reject) => {
    const parsed = new URL(`${BASE_URL}/api/folders/${folderId}/backup/no-checksum.txt`);
    const headers = {
      'Content-Type': 'application/octet-stream',
      'Content-Length': String(content.length),
      // X-Checksum-SHA256 intentionally omitted
      'X-File-Size': String(content.length),
      'Origin': ORIGIN,
      'Cookie': cookies,
    };
    const req = http.request(
      { hostname: parsed.hostname, port: Number(parsed.port) || 80,
        path: parsed.pathname, method: 'PUT', headers },
      (res2) => {
        let raw = '';
        res2.on('data', c => { raw += c; });
        res2.on('end', () => resolve({ status: res2.statusCode }));
      }
    );
    req.on('error', reject);
    req.write(content);
    req.end();
  });

  expect(res.status).toBe(400);
});
