/* ============================================================================
 * CAU WIFI HELPER · 桌面壳层（shell.js）
 * ---------------------------------------------------------------------------
 * 自绘标题栏需要自己接这三件事：
 *   1. 最小化 / 最大化还原 / 关闭 → window.wails.Window.*
 *   2. 双击标题栏空白处切换最大化
 *   3. 拖动交给 CSS：.titlebar 上 --wails-draggable: drag
 *
 * 注意：Wails 运行时的注入时机晚于 DOMContentLoaded 是可能的，
 * 所以这里「先绑定、后判断」——点击时再看 window.wails 在不在。
 * 在浏览器里直接打开时它不存在，静默降级，其它功能不受影响。
 * ==========================================================================*/
(function () {
  'use strict';

  function win() {
    return (window.wails && window.wails.Window) || null;
  }

  function call(name) {
    var w = win();
    if (!w || typeof w[name] !== 'function') return false;
    try {
      w[name]();
      return true;
    } catch (e) {
      return false;
    }
  }

  function bind(id, name) {
    var el = document.getElementById(id);
    if (!el || el.dataset.shellBound) return;
    el.dataset.shellBound = '1';
    el.addEventListener('click', function (ev) {
      ev.preventDefault();
      ev.stopPropagation();
      call(name);
    });
  }

  function boot() {
    bind('win-min', 'Minimise');
    bind('win-max', 'ToggleMaximise');
    bind('win-close', 'Close');

    var bar = document.querySelector('.titlebar');
    if (bar && !bar.dataset.shellBound) {
      bar.dataset.shellBound = '1';
      bar.addEventListener('dblclick', function (ev) {
        if (ev.target.closest('button, input, a')) return;
        call('ToggleMaximise');
      });
    }
  }

  boot();
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  window.addEventListener('load', boot);
})();
