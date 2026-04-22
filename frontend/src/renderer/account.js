/**
 * Account management panel — change email, change password, delete account.
 *
 * Depends on: api.js (window.API), ui.js (window.UI), auth.js (window.Auth)
 *
 * Exposes: window.Account = { show, hide }
 */

'use strict';

(function () {

  if (typeof document === 'undefined' || typeof window === 'undefined') return;

  const escapeHtml = window.escapeHtml;

  // ---- Helpers ---------------------------------------------------------------

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
    for (const inp of inputs) {
      if (inp) inp.classList.remove('input-error', 'input-shake');
    }
  }

  function attachPasswordToggle(btn) {
    btn.addEventListener('click', function () {
      const input = document.getElementById(btn.getAttribute('data-target'));
      if (!input) return;
      const showing = input.type === 'text';
      input.type = showing ? 'password' : 'text';
      btn.setAttribute('aria-label', showing ? 'Show password' : 'Hide password');
      btn.textContent = showing ? '👁' : '🙈';
    });
  }

  // ---- Render ----------------------------------------------------------------

  function render(currentEmail) {
    const el = document.getElementById('account');
    el.innerHTML = `
      <div class="account-panel">
        <div class="view-header account-panel-header">
          <button id="account-close-btn" class="view-back-btn" aria-label="Close account panel">← Back</button>
          <h2 class="view-title">Account</h2>
        </div>

        <section class="account-section">
          <h3 class="account-section-title">Account Info</h3>
          <div class="account-info-row">
            <span class="account-info-label">Email</span>
            <span class="account-info-value" id="account-current-email">${escapeHtml(currentEmail)}</span>
          </div>
        </section>

        <section class="account-section">
          <h3 class="account-section-title">Change Email</h3>
          <form id="change-email-form">
            <label>New email
              <input type="email" id="ce-new-email" required autocomplete="email" />
            </label>
            <label>Current password
              <div class="password-wrapper">
                <input type="password" id="ce-password" required autocomplete="current-password" />
                <button type="button" class="password-toggle" aria-label="Show password" data-target="ce-password">👁</button>
              </div>
            </label>
            <div class="form-error" id="ce-error"></div>
            <button type="submit" id="ce-submit-btn">Update Email</button>
          </form>
        </section>

        <section class="account-section">
          <h3 class="account-section-title">Change Password</h3>
          <form id="change-password-form">
            <label>Current password
              <div class="password-wrapper">
                <input type="password" id="cp-current" required autocomplete="current-password" />
                <button type="button" class="password-toggle" aria-label="Show password" data-target="cp-current">👁</button>
              </div>
            </label>
            <label>New password
              <div class="password-wrapper">
                <input type="password" id="cp-new" required autocomplete="new-password" />
                <button type="button" class="password-toggle" aria-label="Show password" data-target="cp-new">👁</button>
              </div>
            </label>
            <label>Confirm new password
              <div class="password-wrapper">
                <input type="password" id="cp-confirm" required autocomplete="new-password" />
                <button type="button" class="password-toggle" aria-label="Show password" data-target="cp-confirm">👁</button>
              </div>
            </label>
            <div class="form-error" id="cp-error"></div>
            <button type="submit" id="cp-submit-btn">Update Password</button>
          </form>
        </section>

        <section class="account-section account-section--danger">
          <h3 class="account-section-title account-section-title--danger">Danger Zone</h3>
          <p class="account-danger-desc">Permanently deletes your account and all backed-up data. This cannot be undone.</p>
          <button id="delete-account-btn" class="btn-danger">Delete Account</button>
        </section>
      </div>
    `;

    document.getElementById('account-close-btn').addEventListener('click', hide);
    el.querySelectorAll('.password-toggle').forEach(attachPasswordToggle);

    document.getElementById('change-email-form').addEventListener('submit', handleChangeEmail);
    document.getElementById('change-password-form').addEventListener('submit', handleChangePassword);
    document.getElementById('delete-account-btn').addEventListener('click', handleDeleteAccount);
  }

  // ---- Handlers --------------------------------------------------------------

  async function handleChangeEmail(e) {
    e.preventDefault();
    const newEmailInput = document.getElementById('ce-new-email');
    const passInput     = document.getElementById('ce-password');
    const errorEl       = document.getElementById('ce-error');
    const submitBtn     = document.getElementById('ce-submit-btn');
    errorEl.textContent = '';
    clearFieldErrors([newEmailInput, passInput]);

    setButtonLoading(submitBtn, true, 'Updating…');
    try {
      const resp = await window.API.changeEmail(newEmailInput.value.trim(), passInput.value);
      if (!resp.ok) {
        let msg = 'Failed to update email';
        try { msg = (await resp.json()).error || msg; } catch {}
        errorEl.textContent = msg;
        markFieldError(resp.status === 401 ? passInput : newEmailInput);
        setButtonLoading(submitBtn, false, 'Update Email');
        return;
      }
      const data = await resp.json();
      document.getElementById('account-current-email').textContent = data.email;
      passInput.value = '';
      newEmailInput.value = '';
      window.UI.toast('Email updated successfully', 'success');
      setButtonLoading(submitBtn, false, 'Update Email');
    } catch {
      errorEl.textContent = 'Connection error — please try again.';
      setButtonLoading(submitBtn, false, 'Update Email');
    }
  }

  async function handleChangePassword(e) {
    e.preventDefault();
    const currentInput = document.getElementById('cp-current');
    const newInput     = document.getElementById('cp-new');
    const confirmInput = document.getElementById('cp-confirm');
    const errorEl      = document.getElementById('cp-error');
    const submitBtn    = document.getElementById('cp-submit-btn');
    errorEl.textContent = '';
    clearFieldErrors([currentInput, newInput, confirmInput]);

    if (newInput.value !== confirmInput.value) {
      errorEl.textContent = 'Passwords do not match';
      markFieldError(confirmInput);
      return;
    }

    setButtonLoading(submitBtn, true, 'Updating…');
    try {
      const resp = await window.API.changePassword(currentInput.value, newInput.value);
      if (!resp.ok) {
        let msg = 'Failed to update password';
        try { msg = (await resp.json()).error || msg; } catch {}
        errorEl.textContent = msg;
        markFieldError(resp.status === 401 ? currentInput : newInput);
        setButtonLoading(submitBtn, false, 'Update Password');
        return;
      }
      currentInput.value = '';
      newInput.value = '';
      confirmInput.value = '';
      window.UI.toast('Password updated — you have been signed out of other devices', 'success');
      setButtonLoading(submitBtn, false, 'Update Password');
    } catch {
      errorEl.textContent = 'Connection error — please try again.';
      setButtonLoading(submitBtn, false, 'Update Password');
    }
  }

  function handleDeleteAccount() {
    showDeleteConfirmModal(async function (password) {
      try {
        const resp = await window.API.deleteAccount(password);
        if (!resp.ok) {
          let msg = 'Failed to delete account';
          try { msg = (await resp.json()).error || msg; } catch {}
          window.UI.toast(msg, 'error');
          return;
        }
        window.UI.toast('Account deleted', 'success');
        window.TokenStore.clear();
        setTimeout(function () { window.Auth.checkSession(); }, 800);
      } catch {
        window.UI.toast('Connection error — please try again.', 'error');
      }
    });
  }

  function showDeleteConfirmModal(onConfirm) {
    const overlay = document.createElement('div');
    overlay.className = 'modal-overlay modal-overlay--confirm';

    const card = document.createElement('div');
    card.className = 'modal-card';
    card.setAttribute('role', 'dialog');
    card.setAttribute('aria-modal', 'true');
    card.setAttribute('aria-labelledby', 'del-acct-modal-title');

    card.innerHTML = `
      <div class="modal-header">
        <h3 class="modal-title" id="del-acct-modal-title">Delete Account</h3>
      </div>
      <div class="modal-body">
        <p>This will permanently delete your account and all backed-up files. Enter your password to confirm.</p>
        <label class="modal-password-label">Password
          <div class="password-wrapper">
            <input type="password" id="del-acct-password" class="modal-password-input" autocomplete="current-password" />
            <button type="button" class="password-toggle" aria-label="Show password" data-target="del-acct-password">👁</button>
          </div>
        </label>
        <div class="form-error" id="del-acct-error"></div>
      </div>
      <div class="modal-actions">
        <button class="modal-action-btn" id="del-acct-cancel-btn">Cancel</button>
        <button class="modal-action-btn modal-action-danger" id="del-acct-confirm-btn">Delete Account</button>
      </div>
    `;

    overlay.appendChild(card);
    document.body.appendChild(overlay);
    requestAnimationFrame(function () { overlay.classList.add('modal-visible'); });

    card.querySelectorAll('.password-toggle').forEach(attachPasswordToggle);

    const passInput  = card.querySelector('#del-acct-password');
    const cancelBtn  = card.querySelector('#del-acct-cancel-btn');
    const confirmBtn = card.querySelector('#del-acct-confirm-btn');
    cancelBtn.focus();

    function close() {
      overlay.classList.remove('modal-visible');
      setTimeout(function () { overlay.remove(); }, 200);
    }

    cancelBtn.addEventListener('click', close);
    overlay.addEventListener('keydown', function (e) { if (e.key === 'Escape') close(); });

    confirmBtn.addEventListener('click', function () {
      const pw = passInput.value;
      if (!pw) {
        card.querySelector('#del-acct-error').textContent = 'Password is required';
        markFieldError(passInput);
        return;
      }
      close();
      onConfirm(pw);
    });
  }

  // ---- Public interface ------------------------------------------------------

  let _currentEmail = '';
  let _returnToFiles = false;

  function show(email) {
    _currentEmail = email || _currentEmail;
    _returnToFiles = !document.getElementById('file-browser').classList.contains('hidden');
    const el = document.getElementById('account');
    render(_currentEmail);
    el.classList.remove('hidden');
    document.getElementById('dashboard').classList.add('hidden');
    document.getElementById('file-browser').classList.add('hidden');
  }

  function hide() {
    document.getElementById('account').classList.add('hidden');
    if (_returnToFiles) {
      window.FileBrowser.show();
    } else {
      window.Dashboard.show();
    }
  }

  window.Account = { show, hide };

}());
