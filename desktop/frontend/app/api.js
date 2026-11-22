/* ============================================================================
 * CAU WIFI HELPER · 桌面端数据适配层（api.js）
 * ---------------------------------------------------------------------------
 * 职责：把本机 caulinkd 的只读接口包装成 UI 期望的 window.MockAPI。
 *
 *   有守护进程 → 显示真实状态，标题栏出现「实时」小标
 *   探不到     → 顶部红条提示连接失败，界面不再展示任何内置模拟数据
 *
 * 接口契约见仓库 docs/status-api.md：
 *   GET http://127.0.0.1:7788/api/v1/status
 *   { ips: [...], scheduler: {...}, totals: {...} }
 *   轮询 1s，单次超时 3s；rate_* 单位是字节/秒。
 *
 * 本文件必须排在 mock.js 之后、app.js 之前加载（app.js 启动时取
 * window.MockAPI 的引用）。
 * ==========================================================================*/
(function () {
  'use strict';

  var M = window.MockAPI;
  if (!M) return;

  var BASE = 'http://127.0.0.1:7788';
  var POLL_MS = 1000;   // 与 status-api.md 建议的 1s 对齐
  var TIMEOUT_MS = 3000; // 单次请求超时

  /* 真实数据的一层薄缓存。
   *
   * 连不上守护进程时 on=false 且 err=true：界面显示一条红色告警条，
   * 数据一律为空——保留模拟数据会让人分不清"程序在跑"和"连不上"，
   * 排查时被误导的代价远大于界面好看。 */
  var live = { on: false, at: 0, ports: [], sched: null, hist: [], miss: 0, err: false };

  /* 记录 UI 注册的刷新回调（app.js 通过 MockAPI.onChange 注册） */
  var subs = [];
  var innerOnChange = M.onChange;
  M.onChange = function (fn) {
    subs.push(fn);
    return innerOnChange(fn);
  };
  function notify() {
    subs.forEach(function (fn) { try { fn(); } catch (e) { /* 渲染异常不影响轮询 */ } });
  }

  /* ---------------- 契约字段 → UI 字段 ---------------- */
  function num(v, d) { return typeof v === 'number' && isFinite(v) ? v : d; }

  function mapPort(p) {
    return {
      id: p.id,
      interface: p.interface || '',
      source_ip: p.source_ip || '',
      model: p.model || '',
      auth_status: p.auth_status || 'offline',
      health_score: num(p.health_score, 0),
      rtt_ms: p.rtt_ms == null ? null : Math.round(p.rtt_ms),
      loss_rate: p.loss_rate == null ? null : p.loss_rate,
      schedulable: !!p.schedulable,
      weight: num(p.weight, 0),
      active_conns: num(p.active_conns, 0),
      total_conns: num(p.total_conns, 0),
      bytes_up: num(p.bytes_up, 0),
      bytes_down: num(p.bytes_down, 0),
      rate_up_bps: num(p.rate_up_bps, 0),
      rate_down_bps: num(p.rate_down_bps, 0),
      last_seen: p.last_seen || null,
      errors: p.errors || []
    };
  }

  function mainOf() {
    var id = live.sched && live.sched.sticky_id;
    if (!id) return null;
    for (var i = 0; i < live.ports.length; i += 1) {
      if (live.ports[i].id === id) return live.ports[i];
    }
    return null;
  }

  /* 代表端口：统计卡片上"延迟/丢包率/健康度"取自它。
   *
   * 优先用 sticky_id（敏感流量绑定的那条），但它只在跑游戏/会议时才有值。
   * 没有时不能返回 null——否则三张卡片会一直显示"未连接/—"。退回到
   * "连接数最多的端口"，再退到健康度最好的，最后退到第一个可用端口。 */
  function primaryOf() {
    var m = mainOf();
    if (m) return m;

    var best = null;
    live.ports.forEach(function (p) {
      if (!best) { best = p; return; }
      var pa = p.active_conns || 0, ba = best.active_conns || 0;
      if (pa > ba) { best = p; return; }
      if (pa === ba && (p.health_score || 0) > (best.health_score || 0)) best = p;
    });
    return best;
  }

  function calcTotals() {
    var online = 0, bad = 0;
    var down = 0, up = 0;
    live.ports.forEach(function (p) {
      if (p.auth_status === 'online') online += 1;
      if (p.auth_status === 'reauthenticating' || (p.errors && p.errors.length)) bad += 1;
      // 当前网速是"这台机器现在总共跑多快"，必须合计所有端口，
      // 而不是只取主用端口——多 IP 并行下载的收益正是体现在这里。
      down += p.rate_down_bps || 0;
      up += p.rate_up_bps || 0;
    });
    var m = primaryOf();
    return {
      count: live.ports.length,
      online: online,
      bad: bad,
      idle: live.ports.length - online - bad,
      rate_down_bps: down,
      rate_up_bps: up,
      rtt_ms: m ? m.rtt_ms : null,
      loss_rate: m ? m.loss_rate : null,
      health_score: m ? m.health_score : null,
      health_pct: qualityPct(m),
      primary_id: m ? m.id : null,
      primary_interface: m ? m.interface : null
    };
  }

  /* ---------------- 延迟 → 健康度百分比（纯展示口径） ----------------
   *
   * 后端给的 health_score 是 0–1 的复合分（RTT + 丢包经 EWMA 平滑），
   * 直接乘 100 显示成 "38%" 会让一条完全正常的链路看起来像故障。
   * 这里换算成用户能理解的百分比，锚点取自农大实测：
   *
   *   ≤ 120 ms            100%   —— 优秀
   *   120 → 300 ms        100→95 —— 正常区间（实测 215–330 ms）
   *   300 → 1500 ms       95→0   —— 明显变差直到不可用
   *   ≥ 1500 ms / 无数据   0%    —— 等同于断连
   *
   * 断连时根本没有 RTT（探测失败或认证掉线），所以直接给 0%。
   * 丢包按比例扣分：10% 丢包 → 打九折。
   */
  var QUALITY = { bestRtt: 120, normRtt: 300, deadRtt: 1500, normPct: 95 };

  function qualityPct(p) {
    if (!p || p.auth_status !== 'online' || p.rtt_ms == null) return 0;

    var r = p.rtt_ms;
    var pct;
    if (r <= QUALITY.bestRtt) {
      pct = 100;
    } else if (r <= QUALITY.normRtt) {
      pct = 100 - (r - QUALITY.bestRtt) / (QUALITY.normRtt - QUALITY.bestRtt) *
        (100 - QUALITY.normPct);
    } else if (r < QUALITY.deadRtt) {
      pct = QUALITY.normPct * (1 - (r - QUALITY.normRtt) / (QUALITY.deadRtt - QUALITY.normRtt));
    } else {
      pct = 0;
    }

    var loss = typeof p.loss_rate === 'number' ? p.loss_rate : 0;
    if (loss > 0) pct *= (1 - Math.min(loss, 1));

    return Math.round(Math.max(0, Math.min(100, pct)));
  }

  function clone(x) { return JSON.parse(JSON.stringify(x)); }

  /* ---------------- 收到一份状态快照 ---------------- */
  function apply(d) {
    live.on = true;
    live.miss = 0;
    live.err = false;
    live.at = Date.now();
    live.ports = (d.ips || []).map(mapPort);
    live.sched = d.scheduler || { strategy: '', sticky_id: null, sticky_ip: null, candidates: 0 };
    // 曲线画的是"总下载速率"，与统计卡的「当前网速」口径一致。
    var sumDown = 0;
    live.ports.forEach(function (p) { sumDown += p.rate_down_bps || 0; });
    if (live.ports.length) {
      live.hist.push({ t: live.at, v: sumDown });
      if (live.hist.length > 60) live.hist.shift();
    }
    badge();
    hideAlert();
    notify();
  }

  /* ---------------- 连接失败：一条红条，不许给假数据 ---------------- */
  function dropToDisconnected() {
    if (!live.on && live.err) return;
    live.on = false;
    live.err = true;
    live.ports = [];
    live.sched = null;
    live.hist = [];
    badge();
    showAlert();
    notify();
  }

  // showAlert 在标题栏下方插一条红色告警条。只在连接失败时出现，
  // 连上后自动移除——它盖在内容区顶部，不遮住操作按钮。
  function showAlert() {
    if (document.getElementById('conn-alert')) return;
    var bar = document.createElement('div');
    bar.id = 'conn-alert';
    bar.setAttribute('role', 'alert');
    bar.style.cssText = [
      'position:fixed', 'left:0', 'right:0', 'top:56px', 'z-index:99',
      'display:flex', 'align-items:center', 'gap:12px',
      'padding:10px 16px',
      'background:#c62828', 'color:#fff',
      'font-size:13px', 'line-height:1.5',
      'box-shadow:0 2px 8px rgba(0,0,0,.35)'
    ].join(';');
    bar.innerHTML =
      '<span style="flex:0 0 auto;font-weight:600">连接失败</span>' +
      '<span style="flex:1 1 auto;opacity:.92">' +
      '无法连接本机守护进程（' + BASE + '）。程序没有在运行，或状态接口被占用。' +
      '日志见 %LOCALAPPDATA%\\caulink-desktop\\caulinkd.log' +
      '</span>';
    var btn = document.createElement('button');
    btn.textContent = '重试';
    btn.style.cssText = [
      'flex:0 0 auto', 'cursor:pointer', 'border:0', 'border-radius:4px',
      'padding:5px 14px', 'background:rgba(255,255,255,.92)', 'color:#8e1414',
      'font-size:13px', 'font-weight:600'
    ].join(';');
    btn.onclick = function () { poll(); };
    bar.appendChild(btn);
    document.body.appendChild(bar);
  }

  function hideAlert() {
    var el = document.getElementById('conn-alert');
    if (el) el.remove();
  }

  /* ---------------- 轮询 ---------------- */
  var inflight = false;
  function poll() {
    if (inflight) return Promise.resolve(false);
    inflight = true;
    var ctl = (typeof AbortController !== 'undefined') ? new AbortController() : null;
    var timer = setTimeout(function () { if (ctl) ctl.abort(); }, TIMEOUT_MS);

    return fetch(BASE + '/api/v1/status', {
      cache: 'no-store',
      headers: { 'Accept': 'application/json' },
      signal: ctl ? ctl.signal : undefined
    }).then(function (r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return r.json();
    }).then(function (d) {
      clearTimeout(timer);
      inflight = false;
      apply(d);
      return true;
    }).catch(function () {
      clearTimeout(timer);
      inflight = false;
      live.miss += 1;
      // 连续两次失败才报错：单次抖动只值得重试，不值得把界面打成红的。
      if (live.miss >= 2) dropToDisconnected();
      return false;
    });
  }

  /* ---------------- 覆写 MockAPI 的读接口 ----------------
   *
   * 拿不到真实数据时一律返回空结构，不回落到 mock.js 的模拟数据。
   * 字段形状必须和真实快照一致，否则 app.js 的渲染会拿到 undefined。
   */
  function emptySched() {
    return { strategy: '', sticky_id: null, sticky_ip: null, candidates: 0, schedulable_count: 0 };
  }

  function emptyTotals() {
    return {
      count: 0, online: 0, bad: 0, idle: 0,
      rate_down_bps: 0, rate_up_bps: 0,
      rtt_ms: null, loss_rate: null, health_score: null, health_pct: 0,
      primary_id: null, primary_interface: null
    };
  }

  M.ports = function () { return live.on ? clone(live.ports) : []; };

  M.visiblePorts = function () {
    if (!live.on) return [];
    var ignored = M.ignoredIds();
    return M.ports().filter(function (p) { return ignored.indexOf(p.id) === -1; });
  };

  M.mainPortId = function () {
    return live.on && live.sched ? live.sched.sticky_id : null;
  };

  M.scheduler = function () {
    return live.on && live.sched ? clone(live.sched) : emptySched();
  };

  M.totals = function () { return live.on ? calcTotals() : emptyTotals(); };

  M.history = function () { return live.on ? live.hist.slice() : []; };

  M.snapshot = function () {
    return { ips: M.ports(), scheduler: M.scheduler(), totals: M.totals() };
  };

  M.lastScanAt = function () { return live.on ? live.at : 0; };

  /* 重新扫描：先拉一次状态，再返回端口列表 */
  M.scanPorts = function () {
    return poll().then(function () { return M.ports(); });
  };

  /* 不推进任何本地模拟数据 */
  M.tick = function () {};

  /* 供 UI / 自测查询当前数据来源 */
  M.mode = function () { return live.on ? 'live' : (live.err ? 'error' : 'connecting'); };
  M.liveInfo = function () { return { on: live.on, err: live.err, at: live.at, miss: live.miss }; };

  /* ---------------- 标题栏数据来源小标 ---------------- */
  function badge() {
    var right = document.querySelector('.titlebar-right');
    if (!right) return;
    var el = document.getElementById('mode-badge');
    if (!el) {
      el = document.createElement('span');
      el.id = 'mode-badge';
      el.className = 'mode-badge';
      right.insertBefore(el, right.firstChild);
    }
    if (live.on) {
      el.textContent = '实时';
      el.className = 'mode-badge live';
      el.title = '已连接本机 caulinkd · ' + BASE;
    } else if (live.err) {
      el.textContent = '未连接';
      el.className = 'mode-badge demo';
      el.title = '连不上本机 caulinkd（' + BASE + '），界面不显示模拟数据';
    } else {
      el.textContent = '连接中';
      el.className = 'mode-badge';
      el.title = '正在连接 ' + BASE;
    }
  }

  /* ---------------- 启动 ---------------- */
  var pollTimer = null;

  function boot() {
    badge();
    poll();
    pollTimer = setInterval(poll, POLL_MS);
    /* 从设置页改轮询间隔后跟着变 */
    var innerUpdate = M.updateSettings;
    if (typeof innerUpdate === 'function') {
      M.updateSettings = function (patch) {
        var r = innerUpdate.call(M, patch);
        if (patch && patch.pollInterval) {
          clearInterval(pollTimer);
          pollTimer = setInterval(poll, patch.pollInterval);
        }
        return r;
      };
    }
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();
})();
