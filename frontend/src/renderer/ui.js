/**
 * UI utilities — toast notifications.
 *
 * Usage:
 *   window.UI.toast('Path saved')           // green success toast
 *   window.UI.toast('Something failed', 'error')  // red error toast
 *
 * Toasts are appended to #toast-container, which this module creates if absent.
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
    el.textContent = message;

    container.appendChild(el);

    // Trigger enter animation on next frame
    requestAnimationFrame(function () {
      el.classList.add('toast-visible');
    });

    setTimeout(function () {
      el.classList.remove('toast-visible');
      el.addEventListener('transitionend', function () { el.remove(); }, { once: true });
    }, durationMs);
  }

  const UI = { toast };

  if (typeof module !== 'undefined') {
    module.exports = { UI };
  } else if (typeof window !== 'undefined') {
    window.UI = UI;
  }

})();
