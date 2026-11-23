/* ============================================================================
 * CAU WIFI HELPER · 双通道加速下载面板（accel.js）
 * ---------------------------------------------------------------------------
 * 为什么单独一个文件：app.js 是 1470 行的自绘仪表盘，往里面塞新模块的回归
 * 风险比收益大。这里自带样式与 DOM 注入，只跟 caulinkd 的接口打交道。
 *
 * 后端契约（internal/control/accel.go）：
 *   GET  /api/v1/accel/tasks         → { defaults:{dir,connections,max_connections}, tasks:[...] }
 *   POST /api/v1/accel/tasks         → {url, dir?, connections?}   需 X-Caulink-Action: 1
 *   POST /api/v1/accel/tasks/cancel  → {id}                        需 X-Caulink-Action: 1
 *
 * 一条铁律写死在这里的文案里：**加速只在有 2 个以上已认证源 IP 时才有意义**，
 * 单 IP 下分片只会互相抢同一份额度（实测 0.98×）。所以面板会把当前几条链路
 * 明明白白列出来，不让用户对着一个数字猜。
 * ==========================================================================*/
(function () {
  'use strict';

  var BASE = 'http://127.0.0.1:7788';
  var POLL_MS = 900;

  var state = {
    defaults: { dir: '', connections: 8, max_connections: 16 },
    tasks: [],
    loaded: false,
    err: '',
    busy: false,
    /* 用户是否正在编辑目录：正在编辑时不能被轮询刷掉输入 */
    dirTouched: false,
    /* 目录相关的临时提示（保存成功/失败），一次有效 */
    dirMsg: ''
  };

  var $ = function (s, r) { return (r || document).querySelector(s); };

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function mb(n) {
    if (!n) return '0';
    var v = n / 1048576;
    if (v >= 1024) return (v / 1024).toFixed(2) + ' GB';
    if (v >= 1) return v.toFixed(1) + ' MB';
    return (n / 1024).toFixed(0) + ' KB';
  }

  function speed(bps) {
    if (!bps) return '—';
    var v = bps / 1048576;
    if (v >= 1) return v.toFixed(1) + ' MB/s';
    return (bps / 1024).toFixed(0) + ' KB/s';
  }

  var STATUS_TEXT = {
    queued: '排队中',
    running: '下载中',
    done: '已完成',
    failed: '失败',
    canceled: '已取消'
  };

  /* ---------------- 样式（自带，不侵入 styles.css） ---------------- */
  var CSS = [
    '.acc-card{flex:none;background:var(--card);border:1px solid var(--line);',
    'border-radius:var(--radius-card);padding:10px 14px 12px;display:flex;flex-direction:column;gap:8px}',
    '.acc-head{display:flex;align-items:center;gap:8px}',
    '.acc-title{font-size:13px;font-weight:600;color:var(--text)}',
    '.acc-hint{font-size:11px;color:var(--faint);margin-left:auto;white-space:nowrap}',
    '.acc-form{display:flex;align-items:center;gap:8px}',
    '.acc-input{flex:1 1 auto;min-width:0;height:32px;padding:0 10px;background:var(--card-2);',
    'border:1px solid var(--line);border-radius:var(--radius-btn);color:var(--text);font-size:12.5px;outline:none}',
    '.acc-input:focus{border-color:var(--accent);box-shadow:0 0 0 3px var(--accent-weak)}',
    '.acc-sel{height:32px;padding:0 8px;background:var(--card-2);border:1px solid var(--line);',
    'border-radius:var(--radius-btn);color:var(--text);font-size:12px;outline:none}',
    '.acc-btn{height:32px;padding:0 14px;border:1px solid var(--line);border-radius:var(--radius-btn);',
    'background:var(--card-2);color:var(--text);font-size:12.5px;cursor:pointer;white-space:nowrap}',
    '.acc-btn:hover{border-color:var(--accent);color:var(--accent)}',
    '.acc-btn.primary{background:var(--accent);border-color:var(--accent);color:#fff}',
    '.acc-btn.primary:hover{filter:brightness(1.08);color:#fff}',
    '.acc-btn[disabled]{opacity:.5;cursor:default;border-color:var(--line);color:var(--muted)}',
    '.acc-btn.danger{color:var(--bad);border-color:var(--line)}',
    '.acc-btn.danger:hover{border-color:var(--bad)}',
    '.acc-meta{display:flex;align-items:center;gap:10px;font-size:11.5px;color:var(--muted);flex-wrap:wrap}',
    '.acc-meta b{color:var(--text);font-weight:600}',
    '.acc-bar{height:8px;border-radius:99px;background:var(--card-2);overflow:hidden;position:relative;',
    'border:1px solid var(--line)}',
    '.acc-fill{height:100%;width:0;background:var(--accent);transition:width .3s ease}',
    '.acc-fill.done{background:var(--ok)}',
    '.acc-fill.failed{background:var(--bad)}',
    '.acc-ch{display:flex;align-items:center;gap:8px;font-size:11px;color:var(--muted)}',
    '.acc-ch-ip{width:132px;flex:none;color:var(--text);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}',
    '.acc-ch-bar{flex:1 1 auto;height:6px;border-radius:99px;background:var(--card-2);',
    'border:1px solid var(--line);overflow:hidden;min-width:40px}',
    '.acc-ch-fill{height:100%;background:var(--accent)}',
    '.acc-ch-b{width:64px;flex:none;text-align:right;color:var(--muted)}',
    '.acc-dirrow{display:flex;align-items:center;gap:8px}',
    '.acc-dirlabel{font-size:11.5px;color:var(--muted);flex:none}',
    '.acc-dirinput{height:28px;font-size:11.5px;flex:1 1 auto;min-width:0}',
    '.acc-dirrow .acc-btn{height:28px;padding:0 10px;font-size:11.5px}',
    '.acc-msg{font-size:11.5px;color:var(--muted);word-break:break-all}',
    '.acc-msg.err{color:var(--bad)}',
    '.acc-msg.ok{color:var(--ok)}'
  ].join('');

  function injectStyle() {
    var st = document.createElement('style');
    st.id = 'acc-style';
    st.textContent = CSS;
    document.head.appendChild(st);
  }

  /* ---------------- DOM ---------------- */
  function buildCard() {
    var sec = document.createElement('section');
    sec.className = 'acc-card';
    sec.id = 'acc-card';
    sec.innerHTML = [
      '<div class="acc-head">',
      '  <span class="acc-title">双通道加速下载</span>',
      '  <span class="acc-hint" id="acc-hint">把文件拆成多段，分别走不同线路</span>',
      '</div>',
      '<div class="acc-form">',
      '  <input class="acc-input" id="acc-url" spellcheck="false" autocomplete="off"',
      '         placeholder="粘贴文件直链（http/https，例如 .iso / .zip 下载地址）">',
      '  <select class="acc-sel" id="acc-conns" title="并发分片数">',
      '    <option value="4">4 片</option>',
      '    <option value="8" selected>8 片</option>',
      '    <option value="16">16 片</option>',
      '  </select>',
      '  <button class="acc-btn primary" id="acc-start">开始加速</button>',
      '</div>',
      '<div class="acc-dirrow">',
      '  <span class="acc-dirlabel">保存到</span>',
      '  <input class="acc-input acc-dirinput" id="acc-dir" spellcheck="false" autocomplete="off"',
      '         placeholder="选择或粘贴文件夹路径（绝对路径）">',
      '  <button class="acc-btn" id="acc-browse" title="选择文件夹">浏览…</button>',
      '</div>',
      '<div class="acc-msg" id="acc-msg"></div>',
      '<div id="acc-progress" hidden>',
      '  <div class="acc-meta" style="margin-bottom:6px">',
      '    <span id="acc-file" style="color:var(--text)"></span>',
      '    <span id="acc-stat"></span>',
      '    <button class="acc-btn danger" id="acc-cancel" style="margin-left:auto;height:24px;padding:0 10px;font-size:11.5px">取消</button>',
      '  </div>',
      '  <div class="acc-bar"><div class="acc-fill" id="acc-fill"></div></div>',
      '  <div id="acc-channels" style="display:flex;flex-direction:column;gap:5px;margin-top:8px"></div>',
      '</div>'
    ].join('');
    return sec;
  }

  function mount() {
    var view = document.getElementById('view-main');
    if (!view || document.getElementById('acc-card')) return;
    var anchor = document.getElementById('tbl-card');
    var card = buildCard();
    if (anchor && anchor.parentNode === view) view.insertBefore(card, anchor);
    else view.appendChild(card);
    bind();
  }

  /* ---------------- 接口 ---------------- */
  function request(method, path, body) {
    var opt = {
      method: method,
      cache: 'no-store',
      headers: { 'Accept': 'application/json' }
    };
    if (body !== undefined) {
      opt.headers['Content-Type'] = 'application/json';
      // 写操作必须带这个头：守护进程靠它挡住本机其他网页的偷偷调用。
      opt.headers['X-Caulink-Action'] = '1';
      opt.body = JSON.stringify(body);
    }
    return fetch(BASE + path, opt).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (d) {
        if (!r.ok) throw new Error(d.error || ('HTTP ' + r.status));
        return d;
      });
    });
  }

  function poll() {
    return request('GET', '/api/v1/accel/tasks').then(function (d) {
      state.defaults = d.defaults || state.defaults;
      state.tasks = d.tasks || [];
      state.loaded = true;
      state.err = '';
      render();
    }).catch(function (e) {
      state.err = e.message || '连接失败';
      render();
    });
  }

  /* ---------------- 保存目录 ---------------- */

  /* 调 Wails 运行时的目录选择框。
   *
   * 面板是零构建的原生 JS，没有打包 @wailsio/runtime，所以用动态 import 拿
   * 同一份 /wails/runtime.js 的模块命名空间 —— 它由 Wails 自己注入，页面
   * 里已经加载过一次，这里拿到的是同一个实例。
   *
   * 拿不到（比如在普通浏览器里开发调试）就返回 null，界面提示手动粘贴路径，
   * 不能因为这个功能把整个面板卡住。
   */
  function pickFolder(current) {
    return import('/wails/runtime.js').then(function (rt) {
      var dlg = rt && rt.Dialogs;
      if (!dlg || typeof dlg.OpenFile !== 'function') return null;
      return dlg.OpenFile({
        CanChooseDirectories: true,
        CanChooseFiles: false,
        CanCreateDirectories: true,
        Title: '选择加速下载的保存目录',
        ButtonText: '选这个文件夹',
        Directory: current || undefined
      });
    }).then(function (r) {
      var p = Array.isArray(r) ? r[0] : r;
      if (!p) return null;
      // 有的后端会回 file:///D:/xxx 形式的 URI，转成普通路径再交给守护进程。
      if (p.indexOf('file:///') === 0) p = decodeURIComponent(p.slice(8));
      return p;
    }).catch(function () { return null; });
  }

  function setDirMsg(text, isErr) {
    state.dirMsg = text;
    var msg = $('#acc-msg');
    if (!msg) return;
    msg.className = 'acc-msg' + (isErr ? ' err' : '');
    msg.textContent = text;

    // 成功提示 4 秒后自动退回常规说明，免得一句话永久占着位置。
    clearTimeout(state.dirMsgTimer);
    if (!isErr) {
      state.dirMsgTimer = setTimeout(function () {
        state.dirMsg = '';
        render();
      }, 4000);
    }
  }

  /* 保存目录：立刻校验（守护进程会顺手把它建出来）并记住。
   *
   * 单独一个动作，而不是等下次提交任务才生效 —— 用户改完路径就该看到
   * "能不能用"的答案。 */
  function saveDir(raw, quiet) {
    var v = (raw || '').trim();
    if (!v) {
      if (!quiet) setDirMsg('保存目录不能为空', true);
      return Promise.resolve(false);
    }
    return request('POST', '/api/v1/accel/settings', { dir: v }).then(function (d) {
      state.defaults.dir = d.dir;
      state.dirTouched = false;
      state.dirMsg = '';
      var input = $('#acc-dir');
      if (input) input.value = d.dir;
      if (!quiet) setDirMsg('保存目录已更新：' + d.dir, false);
      return true;
    }).catch(function (e) {
      state.dirMsg = e.message || '保存目录不可用';
      setDirMsg('保存目录不可用：' + (e.message || ''), true);
      return false;
    });
  }

  /* ---------------- 渲染 ---------------- */
  function currentTask() {
    return state.tasks.length ? state.tasks[0] : null;
  }

  function render() {
    var dirInput = $('#acc-dir');
    // 用户正在编辑时不覆盖他的输入，否则每 900ms 一次的轮询会把光标下的字刷掉。
    if (dirInput && !state.dirTouched && state.defaults.dir) {
      dirInput.value = state.defaults.dir;
    }

    var conns = $('#acc-conns');
    if (conns && !conns.dataset.touched) conns.value = String(state.defaults.connections || 8);

    var msg = $('#acc-msg');
    var t = currentTask();
    var prog = $('#acc-progress');
    var start = $('#acc-start');

    if (!state.loaded) {
      if (msg) { msg.className = 'acc-msg'; msg.textContent = '正在连接守护进程…'; }
      if (start) start.disabled = true;
      if (prog) prog.hidden = true;
      return;
    }

    if (state.err) {
      if (msg) {
        msg.className = 'acc-msg err';
        msg.textContent = '连不上守护进程：' + state.err + '（加速下载需要 caulinkd 在运行）';
      }
      if (start) start.disabled = true;
      if (prog) prog.hidden = true;
      return;
    }

    if (start) start.disabled = state.busy;
    if (msg && !t && !state.dirMsg) {
      msg.className = 'acc-msg';
      msg.textContent = '多个分片分别走不同线路，落盘位置就是上面这个目录。';
    }

    if (!t) {
      if (prog) prog.hidden = true;
      return;
    }

    prog.hidden = false;
    var pct = t.total_bytes > 0 ? Math.min(100, t.done_bytes / t.total_bytes * 100) : 0;

    var file = $('#acc-file');
    if (file) file.textContent = t.file || '（探测中）';

    var stat = $('#acc-stat');
    if (stat) {
      var bits = [mb(t.done_bytes) + ' / ' + mb(t.total_bytes)];
      if (t.speed_bps > 0) bits.push('<b>' + speed(t.speed_bps) + '</b>');
      bits.push(pct.toFixed(0) + '%');
      bits.push(STATUS_TEXT[t.status] || t.status);
      stat.innerHTML = bits.join(' · ');
    }

    var fill = $('#acc-fill');
    if (fill) {
      fill.style.width = pct.toFixed(1) + '%';
      fill.className = 'acc-fill' + (t.status === 'done' ? ' done' : t.status === 'failed' ? ' failed' : '');
    }

    var cancel = $('#acc-cancel');
    if (cancel) {
      var active = t.status === 'running' || t.status === 'queued';
      cancel.hidden = !active;
    }

    if (msg) {
      if (t.status === 'failed' && t.error) {
        msg.className = 'acc-msg err';
        msg.textContent = '下载失败：' + t.error;
      } else if (t.status === 'done') {
        msg.className = 'acc-msg ok';
        msg.textContent = '已保存到 ' + (t.save_path || '') +
          (t.elapsed_seconds ? '（用时 ' + t.elapsed_seconds.toFixed(1) + ' 秒）' : '');
      } else if (t.status === 'canceled') {
        msg.className = 'acc-msg';
        msg.textContent = '已取消，未完成的文件已删除';
      } else if (t.status === 'running') {
        msg.className = 'acc-msg';
        var eff = t.effective_connections || (t.channels ? t.channels.length : 0);
        msg.textContent = '正在下载：' + eff + ' 个分片';
        // 选的片数和实际不一样时，说明原因，别让用户以为设置没生效。
        if (t.connections && eff && eff < t.connections) {
          msg.textContent += '（选了 ' + t.connections + ' 片，受在线 IP 与文件大小限制实际 ' + eff + ' 片）';
        }
      }
    }

    renderChannels(t);
  }

  /* 每 IP 各扛了多少，是这块面板最有说服力的一屏 —— 答辩时可以直接指给人看。 */
  function renderChannels(t) {
    var box = $('#acc-channels');
    if (!box) return;
    var chs = t.channels || [];
    if (!chs.length) { box.innerHTML = ''; return; }

    var byIP = {};
    var order = [];
    chs.forEach(function (c) {
      var k = c.ip || c.id || '?';
      if (!byIP[k]) { byIP[k] = { bytes: 0, n: 0, err: '' }; order.push(k); }
      byIP[k].bytes += c.bytes || 0;
      byIP[k].n += 1;
      if (c.error && !byIP[k].err) byIP[k].err = c.error;
    });

    var max = 1;
    order.forEach(function (k) { if (byIP[k].bytes > max) max = byIP[k].bytes; });

    box.innerHTML = order.map(function (k) {
      var v = byIP[k];
      var w = Math.max(2, v.bytes / max * 100);
      var cls = v.err ? ' style="background:var(--bad)"' : '';
      return '<div class="acc-ch" title="' + esc(k) + '">' +
        '<span class="acc-ch-ip">' + esc(k) + '</span>' +
        '<span class="acc-ch-bar"><span class="acc-ch-fill" style="width:' + w.toFixed(1) + '%"' + cls + '></span></span>' +
        '<span class="acc-ch-b">' + esc(mb(v.bytes)) + '</span>' +
        '</div>';
    }).join('');
  }

  /* ---------------- 交互 ---------------- */
  function bind() {
    var start = $('#acc-start');
    var url = $('#acc-url');
    var cancel = $('#acc-cancel');
    var conns = $('#acc-conns');

    if (conns) {
      conns.addEventListener('change', function () { conns.dataset.touched = '1'; });
    }

    function begin() {
      if (state.busy) return;
      var v = (url.value || '').trim();
      if (!v) { url.focus(); return; }

      state.busy = true;
      start.disabled = true;
      start.textContent = '提交中…';
      var dirInput = $('#acc-dir');
      var body = {
        url: v,
        connections: parseInt(conns.value, 10) || 8
      };
      // 带了目录就以这次为准：守护进程会校验、记住，并用于本次下载。
      if (dirInput && dirInput.value.trim()) body.dir = dirInput.value.trim();

      request('POST', '/api/v1/accel/tasks', body).then(function () {
        url.value = '';
        return poll();
      }).catch(function (e) {
        var msg = $('#acc-msg');
        if (msg) {
          msg.className = 'acc-msg err';
          msg.textContent = e.message || '提交失败';
        }
      }).then(function () {
        state.busy = false;
        start.disabled = false;
        start.textContent = '开始加速';
      });
    }

    var dirInput = $('#acc-dir');
    var browse = $('#acc-browse');

    if (dirInput) {
      dirInput.addEventListener('input', function () { state.dirTouched = true; });
      dirInput.addEventListener('blur', function () {
        // 只有真的改了才提交，避免点一下输入框就发一次请求。
        if (state.dirTouched) saveDir(dirInput.value, false);
      });
      dirInput.addEventListener('keydown', function (e) {
        if (e.key === 'Enter') { e.preventDefault(); saveDir(dirInput.value, false); }
      });
    }

    if (browse) {
      browse.addEventListener('click', function () {
        browse.disabled = true;
        pickFolder(state.defaults.dir || (dirInput && dirInput.value) || '').then(function (p) {
          if (!p) {
            // 拿不到系统对话框时不要装死，直接告诉用户还有手动这条路。
            setDirMsg('打不开系统选择框，可以直接在输入框里粘贴文件夹路径', true);
            return false;
          }
          var input = $('#acc-dir');
          if (input) input.value = p;
          state.dirTouched = false;
          return saveDir(p, false);
        }).then(function () { browse.disabled = false; });
      });
    }

    if (start) start.addEventListener('click', begin);
    if (url) {
      url.addEventListener('keydown', function (e) {
        if (e.key === 'Enter') begin();
      });
    }

    if (cancel) {
      cancel.addEventListener('click', function () {
        var t = currentTask();
        if (!t) return;
        cancel.disabled = true;
        request('POST', '/api/v1/accel/tasks/cancel', { id: t.id })
          .catch(function () { /* 轮询会把真实状态刷回来 */ })
          .then(function () { cancel.disabled = false; poll(); });
      });
    }
  }

  /* ---------------- 启动 ---------------- */
  function boot() {
    injectStyle();
    mount();
    poll();
    setInterval(function () {
      // 页面切走时不必轮询：省一次请求，也避免后台任务把日志刷满。
      if (document.hidden) return;
      poll();
    }, POLL_MS);
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();
})();
