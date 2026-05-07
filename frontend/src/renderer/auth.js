/**
 * Auth UI — session check, login, register, forgot/reset password, logout.
 *
 * Cookie-only: tokens live in the Electron / browser session and attach
 * automatically. There is no client-side TokenStore, no remember-me toggle,
 * and no Authorization header.
 *
 * Exposes: window.Auth.checkSession
 */

'use strict';

(function () {

  const _mod = (typeof require !== 'undefined' && typeof module !== 'undefined')
    ? require('./api-client')
    : null;
  const AuthExpiredError = _mod ? _mod.AuthExpiredError : window.AuthExpiredError;
  const escapeHtml       = _mod ? _mod.escapeHtml       : window.escapeHtml;
  const APIClient        = _mod ? _mod.APIClient        : window.APIClient;

  const _apiMod = (typeof require !== 'undefined' && typeof module !== 'undefined')
    ? require('./api')
    : null;
  const API = _apiMod ? _apiMod.API : window.API;

  // ---- Pure / testable functions ------------------------------------------

  /**
   * Pure function: derive a render descriptor from a session API response.
   */
  function renderSessionState(data) {
    if (data.logged_in && data.user) {
      return { type: 'logged-in', email: data.user.email };
    }
    return { type: 'logged-out' };
  }

  /**
   * Estimate password strength as 0–4 plus a label. Pure.
   * Cheap heuristic — length + variety. Not a security gate, just UX guidance.
   */
  function passwordStrength(p) {
    if (!p) return { score: 0, label: 'Empty' };
    let score = 0;
    if (p.length >= 6) score++;
    if (p.length >= 10) score++;
    if (/[A-Z]/.test(p) && /[a-z]/.test(p)) score++;
    if (/[0-9]/.test(p)) score++;
    if (/[^A-Za-z0-9]/.test(p)) score++;
    score = Math.min(4, score);
    const labels = ['Very weak', 'Weak', 'Okay', 'Good', 'Strong'];
    return { score, label: labels[score] };
  }

  // ---- DOM interaction ----------------------------------------------------

  if (typeof document !== 'undefined' && typeof window !== 'undefined' && !window._testMode) {

    async function checkSession({ skipRefresh = false } = {}) {
      const el = document.getElementById('session-status');
      el.className = 'card loading';
      el.innerHTML = '<p>Connecting to server…</p>';

      // Cookie auth: try a proactive refresh so a still-valid refresh cookie
      // restores the session without requiring re-login. /api/session always
      // returns 200 {logged_in:false} when not authed, so request() can't
      // auto-refresh there — we must do it proactively here.
      // Skip when we just logged in — cookies are already fresh.
      if (!skipRefresh) {
        try { await APIClient.tryRefresh(); } catch {}
      }

      try {
        const resp = await API.fetchSession();
        if (!resp.ok) throw new Error(`Server responded with HTTP ${resp.status}`);
        const data = await resp.json();

        // If still not logged in after the proactive refresh (e.g. the first
        // refresh attempt failed due to a network blip at startup), try once
        // more before giving up and showing the login form.
        if (!data.logged_in && !skipRefresh) {
          try {
            const retried = await APIClient.tryRefresh();
            if (retried) {
              const resp2 = await API.fetchSession();
              if (resp2.ok) {
                const data2 = await resp2.json();
                renderState(el, renderSessionState(data2));
                return;
              }
            }
          } catch {}
        }

        renderState(el, renderSessionState(data));
      } catch (err) {
        if (err instanceof AuthExpiredError) {
          renderState(el, { type: 'logged-out' });
        } else {
          renderConnectionError(el);
        }
      }
    }

    function renderConnectionError(el) {
      el.className = 'card error';
      el.innerHTML = `
        <h2>Connection Error</h2>
        <p>Could not reach the server. Make sure the backend is running.</p>
      `;
      const btn = document.createElement('button');
      btn.textContent = 'Retry';
      btn.addEventListener('click', checkSession);
      el.appendChild(btn);
    }

    function renderState(el, state) {
      if (state.type === 'logged-in') {
        el.className = 'card hidden';
        renderHeaderUser(state.email);
        window.Dashboard.show();
      } else {
        clearHeaderUser();
        window.Dashboard.hide();
        window.Files.hide();
        el.classList.remove('hidden');
        renderLoginForm(el);
      }
    }

    function renderHeaderUser(email) {
      const initials = email
        .split('@')[0]
        .replace(/[^a-zA-Z0-9]/g, ' ')
        .trim()
        .split(/\s+/)
        .slice(0, 2)
        .map(function (w) { return w[0] || ''; })
        .join('')
        .toUpperCase() || '?';

      const avatar = document.getElementById('header-avatar');
      if (avatar) { avatar.textContent = initials; avatar.setAttribute('data-email', email); }

      const slot = document.getElementById('header-user');
      if (slot) slot.classList.remove('hidden');
    }

    function clearHeaderUser() {
      const slot = document.getElementById('header-user');
      if (slot) slot.classList.add('hidden');
    }

    // -- Login form ----------------------------------------------------------

    function renderLoginForm(el, errorMsg) {
      el.className = 'card logged-out';
      el.innerHTML = `
        <h2>Sign In</h2>
        <p>Connect to your Cloud Backup account.</p>
        <form id="login-form">
          <label>
            Email
            <input type="email" id="email" autocomplete="email" required />
          </label>
          <label>
            Password
            <div class="password-wrapper">
              <input type="password" id="password" autocomplete="current-password" required />
              <button type="button" class="password-toggle" aria-label="Show password" data-target="password">👁</button>
            </div>
          </label>
          <div class="form-error" id="form-error">${errorMsg ? escapeHtml(errorMsg) : ''}</div>
          <button type="submit" id="login-submit-btn">Sign In</button>
          <button type="button" id="register-btn">Create Account</button>
          <button type="button" id="forgot-btn" class="link-btn">Forgot password?</button>
        </form>
      `;
      document.getElementById('login-form').addEventListener('submit', handleLogin);
      document.getElementById('register-btn').addEventListener('click', renderRegisterForm.bind(null, el));
      document.getElementById('forgot-btn').addEventListener('click', renderForgotPasswordForm.bind(null, el));
      el.querySelectorAll('.password-toggle').forEach(attachPasswordToggle);
    }

    async function handleLogin(e) {
      e.preventDefault();
      const emailInput = document.getElementById('email');
      const passInput  = document.getElementById('password');
      const email      = emailInput.value.trim();
      const password   = passInput.value;
      const errorEl    = document.getElementById('form-error');
      const submitBtn  = document.getElementById('login-submit-btn');
      errorEl.textContent = '';
      clearFieldErrors([emailInput, passInput]);

      setButtonLoading(submitBtn, true, 'Signing in…');
      try {
        const resp = await API.login(email, password);
        if (!resp.ok) {
          let msg = 'Login failed';
          try { msg = (await resp.json()).error || msg; } catch {}
          errorEl.textContent = msg;
          markFieldError(password ? passInput : emailInput);
          setButtonLoading(submitBtn, false, 'Sign In');
          return;
        }
        // Cookies are set by the server; skip proactive refresh since they're fresh.
        checkSession({ skipRefresh: true });
      } catch {
        errorEl.textContent = 'Connection error — please try again.';
        setButtonLoading(submitBtn, false, 'Sign In');
      }
    }

    // -- Register form -------------------------------------------------------

    function renderRegisterForm(el) {
      el.className = 'card logged-out';
      el.innerHTML = `
        <h2>Create Account</h2>
        <form id="register-form">
          <label>
            Email
            <input type="email" id="reg-email" required />
          </label>
          <label>
            Password
            <div class="password-wrapper">
              <input type="password" id="reg-password" required />
              <button type="button" class="password-toggle" aria-label="Show password" data-target="reg-password">👁</button>
            </div>
            <div id="reg-strength" class="password-strength" aria-live="polite"></div>
          </label>
          <div class="form-error" id="reg-error"></div>
          <button type="submit" id="reg-submit-btn">Register</button>
          <button type="button" id="back-btn">Back to Sign In</button>
        </form>
      `;
      document.getElementById('register-form').addEventListener('submit', handleRegister);
      document.getElementById('back-btn').addEventListener('click', () => renderLoginForm(el));
      el.querySelectorAll('.password-toggle').forEach(attachPasswordToggle);

      const passInput = document.getElementById('reg-password');
      const strengthEl = document.getElementById('reg-strength');
      passInput.addEventListener('input', function () {
        const s = passwordStrength(passInput.value);
        strengthEl.textContent = passInput.value ? `Strength: ${s.label}` : '';
        strengthEl.dataset.score = String(s.score);
      });
    }

    async function handleRegister(e) {
      e.preventDefault();
      const emailInput = document.getElementById('reg-email');
      const passInput  = document.getElementById('reg-password');
      const email      = emailInput.value.trim();
      const password   = passInput.value;
      const errorEl    = document.getElementById('reg-error');
      const submitBtn  = document.getElementById('reg-submit-btn');
      errorEl.textContent = '';
      clearFieldErrors([emailInput, passInput]);

      setButtonLoading(submitBtn, true, 'Creating account…');
      try {
        const resp = await API.register(email, password);
        if (!resp.ok) {
          let msg = 'Registration failed';
          try { msg = (await resp.json()).error || msg; } catch {}
          errorEl.textContent = msg;
          markFieldError(emailInput);
          setButtonLoading(submitBtn, false, 'Register');
          return;
        }
        checkSession({ skipRefresh: true });
      } catch {
        errorEl.textContent = 'Connection error — please try again.';
        setButtonLoading(submitBtn, false, 'Register');
      }
    }

    // -- Forgot password flow ------------------------------------------------
    // TODO(forgot-password): backend flow is under development; this UI stays
    // as-is until the email-delivery path lands. See backend handlers.go.

    function renderForgotPasswordForm(el) {
      el.className = 'card logged-out';
      el.innerHTML = `
        <h2>Forgot Password</h2>
        <p>Enter your email to receive a reset token.</p>
        <form id="forgot-form">
          <label>Email <input type="email" id="fp-email" required /></label>
          <div class="form-error" id="fp-error"></div>
          <button type="submit" id="fp-submit-btn">Send Reset Token</button>
          <button type="button" id="back-btn">Back to Sign In</button>
        </form>
      `;
      document.getElementById('forgot-form').addEventListener('submit', handleForgotPassword.bind(null, el));
      document.getElementById('back-btn').addEventListener('click', () => renderLoginForm(el));
    }

    async function handleForgotPassword(el, e) {
      e.preventDefault();
      const emailInput = document.getElementById('fp-email');
      const email      = emailInput.value.trim();
      const errorEl    = document.getElementById('fp-error');
      const submitBtn  = document.getElementById('fp-submit-btn');
      errorEl.textContent = '';
      clearFieldErrors([emailInput]);

      setButtonLoading(submitBtn, true, 'Sending…');
      try {
        const resp = await API.forgotPassword(email);
        if (!resp.ok) {
          let msg = 'Request failed';
          try { msg = (await resp.json()).error || msg; } catch {}
          errorEl.textContent = msg;
          markFieldError(emailInput);
          setButtonLoading(submitBtn, false, 'Send Reset Token');
          return;
        }
        const data = await resp.json();
        renderResetPasswordForm(el, data.reset_token || '');
      } catch {
        errorEl.textContent = 'Connection error — please try again.';
        setButtonLoading(submitBtn, false, 'Send Reset Token');
      }
    }

    function renderResetPasswordForm(el, prefillToken) {
      el.className = 'card logged-out';
      el.innerHTML = `
        <h2>Reset Password</h2>
        ${prefillToken
          ? `<p class="dev-note">Dev mode: reset token pre-filled below.<br>In production this would arrive by email.</p>`
          : '<p>Enter the reset token from your email and choose a new password.</p>'
        }
        <form id="reset-form">
          <label>
            Reset Token
            <input type="text" id="reset-token" value="${escapeHtml(prefillToken)}" required />
          </label>
          <label>
            New Password
            <div class="password-wrapper">
              <input type="password" id="new-password" required />
              <button type="button" class="password-toggle" aria-label="Show password" data-target="new-password">👁</button>
            </div>
          </label>
          <label>
            Confirm Password
            <div class="password-wrapper">
              <input type="password" id="confirm-password" required />
              <button type="button" class="password-toggle" aria-label="Show password" data-target="confirm-password">👁</button>
            </div>
          </label>
          <div class="form-error" id="reset-error"></div>
          <button type="submit" id="reset-submit-btn">Reset Password</button>
          <button type="button" id="back-btn">Back to Sign In</button>
        </form>
      `;
      document.getElementById('reset-form').addEventListener('submit', handleResetPassword.bind(null, el));
      document.getElementById('back-btn').addEventListener('click', () => renderLoginForm(el));
      el.querySelectorAll('.password-toggle').forEach(attachPasswordToggle);
    }

    async function handleResetPassword(el, e) {
      e.preventDefault();
      const newPassInput  = document.getElementById('new-password');
      const confPassInput = document.getElementById('confirm-password');
      const resetToken    = document.getElementById('reset-token').value.trim();
      const newPassword   = newPassInput.value;
      const confirmPass   = confPassInput.value;
      const errorEl       = document.getElementById('reset-error');
      const submitBtn     = document.getElementById('reset-submit-btn');
      errorEl.textContent = '';
      clearFieldErrors([newPassInput, confPassInput]);

      if (newPassword !== confirmPass) {
        errorEl.textContent = 'Passwords do not match';
        markFieldError(confPassInput);
        return;
      }

      setButtonLoading(submitBtn, true, 'Resetting…');
      try {
        const resp = await API.resetPassword(resetToken, newPassword);
        if (!resp.ok) {
          let msg = 'Reset failed';
          try { msg = (await resp.json()).error || msg; } catch {}
          errorEl.textContent = msg;
          markFieldError(newPassInput);
          setButtonLoading(submitBtn, false, 'Reset Password');
          return;
        }
        renderLoginForm(el, 'Password updated. Please sign in with your new password.');
      } catch {
        errorEl.textContent = 'Connection error — please try again.';
        setButtonLoading(submitBtn, false, 'Reset Password');
      }
    }

    // -- Auth form helpers ----------------------------------------------------

    function setButtonLoading(btn, loading, label) {
      if (!btn) return;
      btn.disabled = loading;
      btn.innerHTML = loading
        ? '<span class="btn-spinner" aria-hidden="true"></span>' + escapeHtml(label)
        : escapeHtml(label);
    }

    function markFieldError(input) {
      if (!input) return;
      input.classList.add('input-error');
      input.classList.remove('input-shake');
      void input.offsetWidth;
      input.classList.add('input-shake');
      input.addEventListener('animationend', function () {
        input.classList.remove('input-shake');
      }, { once: true });
    }

    function clearFieldErrors(inputs) {
      for (const input of inputs) {
        if (input) input.classList.remove('input-error', 'input-shake');
      }
    }

    function attachPasswordToggle(btn) {
      btn.addEventListener('click', function () {
        const targetId = btn.getAttribute('data-target');
        const input = document.getElementById(targetId);
        if (!input) return;
        const showing = input.type === 'text';
        input.type = showing ? 'password' : 'text';
        btn.setAttribute('aria-label', showing ? 'Show password' : 'Hide password');
        btn.textContent = showing ? '👁' : '🙈';
      });
    }

    // -- Logout --------------------------------------------------------------

    async function logout() {
      try { await API.logout(); } catch { /* cookie will expire eventually */ }
      checkSession({ skipRefresh: true });
    }

    // Wire up logo → dashboard and History.
    document.getElementById('logo-btn').addEventListener('click', function () {
      window.Dashboard.show();
    });
    document.getElementById('history-nav-btn').addEventListener('click', function () {
      window.History.show();
    });

    // Avatar dropdown toggle with aria-expanded state
    const avatarBtn = document.getElementById('header-avatar');
    const avatarDropdown = document.getElementById('avatar-dropdown');
    if (avatarBtn && avatarDropdown) {
      function setAvatarDropdownOpen(isOpen) {
        avatarDropdown.classList.toggle('hidden', !isOpen);
        avatarBtn.setAttribute('aria-expanded', isOpen ? 'true' : 'false');
      }

      avatarBtn.addEventListener('click', function (e) {
        e.stopPropagation();
        setAvatarDropdownOpen(avatarDropdown.classList.contains('hidden'));
      });
      document.getElementById('avatar-dropdown-account').addEventListener('click', function () {
        const email = avatarBtn.getAttribute('data-email');
        window.Account.show(email);
        setAvatarDropdownOpen(false);
      });
      document.getElementById('avatar-dropdown-settings').addEventListener('click', function () {
        window.Settings.show();
        setAvatarDropdownOpen(false);
      });
      document.getElementById('avatar-dropdown-signout').addEventListener('click', logout);
      document.addEventListener('click', function () {
        setAvatarDropdownOpen(false);
      });
    }

    // -- Theme toggle --------------------------------------------------------
    // Default: respect prefers-color-scheme; explicit user choice wins.

    (function initThemeToggle() {
      const saved = localStorage.getItem('theme');
      let theme;
      if (saved === 'light' || saved === 'dark') {
        theme = saved;
      } else if (window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches) {
        theme = 'light';
      } else {
        theme = 'dark';
      }
      if (theme === 'light') {
        document.documentElement.setAttribute('data-theme', 'light');
      } else {
        document.documentElement.removeAttribute('data-theme');
      }

      const btn = document.getElementById('theme-toggle-btn');
      if (btn) {
        btn.textContent = theme === 'light' ? '☀️' : '🌙';
        btn.addEventListener('click', function () {
          const isLight = document.documentElement.getAttribute('data-theme') === 'light';
          if (isLight) {
            document.documentElement.removeAttribute('data-theme');
            localStorage.setItem('theme', 'dark');
            btn.textContent = '🌙';
          } else {
            document.documentElement.setAttribute('data-theme', 'light');
            localStorage.setItem('theme', 'light');
            btn.textContent = '☀️';
          }
        });
      }
    }());

    window.Auth = { checkSession };
  }

  // ---- Exports (for Jest) -------------------------------------------------

  if (typeof module !== 'undefined') {
    module.exports = { renderSessionState, passwordStrength };
  }

})();
