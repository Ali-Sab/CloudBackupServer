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
  function toast(message, type, durationMs) {
    if (typeof document === 'undefined') return;
    type = type || 'success';
    durationMs = durationMs || 3000;

    const container = getContainer();
    const el = document.createElement('div');
    el.className = 'toast toast-' + type;
    el.setAttribute('role', 'status');
    el.setAttribute('aria-live', 'polite');

    const icon = document.createElement('span');
    icon.className = 'toast-icon';
    icon.setAttribute('aria-hidden', 'true');
    icon.textContent = type === 'error' ? '✕' : '✓';

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
