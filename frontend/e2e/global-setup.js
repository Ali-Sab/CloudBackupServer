/**
 * Playwright globalSetup — runs once before all E2E tests.
 *
 * Registers a fresh test user via the HTTP API so tests can log in without
 * going through the registration UI. Uses a timestamp suffix for a unique
 * email on every run — no database wipe needed between runs.
 *
 * If the backend is not reachable, sets E2E_BACKEND_DOWN=1 so tests can
 * skip gracefully rather than failing with a cryptic network error.
 */

'use strict';

const http = require('http');

const BASE_URL = process.env.API_BASE_URL || 'http://localhost:8080';

function httpPost(url, body) {
  return new Promise((resolve, reject) => {
    const data = JSON.stringify(body);
    const parsed = new URL(url);
    const req = http.request({
      hostname: parsed.hostname,
      port: Number(parsed.port) || 80,
      path: parsed.pathname,
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Content-Length': Buffer.byteLength(data),
      },
    }, (res) => {
      let raw = '';
      res.on('data', c => { raw += c; });
      res.on('end', () => {
        try {
          resolve({ status: res.statusCode, body: JSON.parse(raw) });
        } catch {
          resolve({ status: res.statusCode, body: {} });
        }
      });
    });
    req.on('error', reject);
    req.write(data);
    req.end();
  });
}

function isBackendAlive() {
  return new Promise((resolve) => {
    const req = http.get(`${BASE_URL}/api/health`, (res) => {
      resolve(res.statusCode === 200);
    });
    req.on('error', () => resolve(false));
    req.setTimeout(3000, () => { req.destroy(); resolve(false); });
  });
}

module.exports = async function globalSetup() {
  const alive = await isBackendAlive();
  if (!alive) {
    process.env.E2E_BACKEND_DOWN = '1';
    console.warn('\n[E2E] Backend not reachable at', BASE_URL, '— tests will be skipped.\n');
    return;
  }

  const ts = Date.now();
  const email    = `e2e_${ts}@test.example`;
  const password = `E2ePass_${ts}!`;

  const res = await httpPost(`${BASE_URL}/api/auth/register`, { email, password });
  if (res.status !== 201) {
    throw new Error(`[E2E] globalSetup: registration failed — HTTP ${res.status}: ${JSON.stringify(res.body)}`);
  }

  process.env.E2E_TEST_EMAIL    = email;
  process.env.E2E_TEST_PASSWORD = password;

  console.log(`[E2E] Registered test user: ${email}`);
};
