/**
 * UI utilities — toast notifications.
 *
 * Usage:
 *   window.UI.toast('Path saved')           // green success toast
 *   window.UI.toast('Something failed', 'error')  // red error toast
 *
 * Toasts are appended to #toast-container, which this module creates if absent.
 * #4 — Dismissable by click on ×
 * #5 — Icon prefix (✓ / ✕)
 */

'use strict';

(function () {

  function getContainer() {
    let el = document.getElementById('toast-container');
    if (!el) {
      el = document.createElement('div');
      el.id = 'toast-container';
      document.body.appendChild(el);
    }
    return el;
  }

  /**
   * Show a brief toast notification.
   * @param {string} message
   * @param {'success'|'error'} [type='success']
   * @param {number} [durationMs=3000]
   */
  // Dedupe rapid duplicate toasts. Same (message, type) within 1.5s = no-op.
  let _lastKey = '';
  let _lastTs = 0;

  function toast(message, type, durationMs) {
    if (typeof document === 'undefined') return;
    type = type || 'success';
    durationMs = durationMs || 3000;

    const key = type + '\x00' + message;
    const now = Date.now();
    if (key === _lastKey && now - _lastTs < 1500) return;
    _lastKey = key;
    _lastTs = now;

    const container = getContainer();
    const el = document.createElement('div');
    el.className = 'toast toast-' + type;
    el.setAttribute('role', 'status');
    el.setAttribute('aria-live', 'polite');

    const icon = document.createElement('span');
    icon.className = 'toast-icon';
    icon.setAttribute('aria-hidden', 'true');
    icon.textContent = type === 'error' ? '✕' : type === 'info' ? 'ℹ' : '✓';

    const text = document.createElement('span');
    text.className = 'toast-text';
    text.textContent = message;

    const closeBtn = document.createElement('button');
    closeBtn.className = 'toast-close';
    closeBtn.setAttribute('aria-label', 'Dismiss');
    closeBtn.textContent = '×';

    el.appendChild(icon);
    el.appendChild(text);
    el.appendChild(closeBtn);

    container.appendChild(el);

    function dismiss() {
      el.classList.remove('toast-visible');
      el.addEventListener('transitionend', function () { el.remove(); }, { once: true });
    }

    closeBtn.addEventListener('click', dismiss);

    // Trigger enter animation on next frame
    requestAnimationFrame(function () {
      el.classList.add('toast-visible');
    });

    setTimeout(dismiss, durationMs);
  }

  const UI = { toast };

  if (typeof module !== 'undefined') {
    module.exports = { UI };
  } else if (typeof window !== 'undefined') {
    window.UI = UI;
  }

})();
