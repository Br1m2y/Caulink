/* ============================================================================
 * CAU WIFI HELPER · app.js
 * 视图层：渲染、选择、排序、筛选、弹窗、路由。数据全部来自 window.MockAPI。
 * ==========================================================================*/
(function () {
  'use strict';

  var API = window.MockAPI;
  var $ = function (s, r) { return (r || document).querySelector(s); };
  var $$ = function (s, r) { return Array.prototype.slice.call((r || document).querySelectorAll(s)); };

  /* ---------- 图标 ---------- */
  var I = {
    gear: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3.2"/><path d="M19.4 15a1.6 1.6 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.6 1.6 0 0 0-1.8-.3 1.6 1.6 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.6 1.6 0 0 0-1-1.5 1.6 1.6 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.6 1.6 0 0 0 .3-1.8 1.6 1.6 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.6 1.6 0 0 0 1.5-1 1.6 1.6 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.6 1.6 0 0 0 1.8.3H9a1.6 1.6 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.6 1.6 0 0 0 1 1.5 1.6 1.6 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.6 1.6 0 0 0-.3 1.8V9a1.6 1.6 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.6 1.6 0 0 0-1.5 1z"/></svg>',
    refresh: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-2.6-6.4"/><path d="M21 3v6h-6"/></svg>',
    search: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><circle cx="11" cy="11" r="7"/><path d="m20 20-3.6-3.6"/></svg>',
    x: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6 6 18"/></svg>',
    minus: '<svg viewBox="0 0 12 12" stroke="currentColor" stroke-width="1.2"><path d="M2 6h8"/></svg>',
    square: '<svg viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.2"><rect x="2.5" y="2.5" width="7" height="7"/></svg>',
    eye: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="2.8"/></svg>',
    eyeOff: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3l18 18"/><path d="M10.6 6.1A9.8 9.8 0 0 1 12 6c6 0 9.5 6 9.5 6a17 17 0 0 1-2.7 3.5"/><path d="M6.6 6.8A16.7 16.7 0 0 0 2.5 12S6 18 12 18a9.4 9.4 0 0 0 4.1-.9"/><path d="M9.9 9.9a3 3 0 0 0 4.2 4.2"/></svg>',
    dots: '<svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.6"/><circle cx="12" cy="12" r="1.6"/><circle cx="19" cy="12" r="1.6"/></svg>',
    plus: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>',
    trash: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16M9 7V5h6v2M6 7l1 13h10l1-13"/><path d="M10 11v5M14 11v5"/></svg>',
    pencil: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 20h4L20 8l-4-4L4 16z"/><path d="M14 6l4 4"/></svg>',
    back: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M15 5l-7 7 7 7"/></svg>',
    filter: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M3 5h18l-7 8v6l-4 2v-8z"/></svg>',
    check: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12.5l5 5L20 6.5"/></svg>',
    warn: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3.5 22 20H2z"/><path d="M12 9v5M12 17h.01"/></svg>',
    plug: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"><path d="M9 3v6M15 3v6"/><path d="M6 9h12v3a6 6 0 0 1-12 0z"/><path d="M12 18v3"/></svg>'
  };

  /* ---------- 工具 ---------- */
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }
  function hexA(hex, a) {
    var n = parseInt(hex.slice(1), 16);
    return 'rgba(' + ((n >> 16) & 255) + ',' + ((n >> 8) & 255) + ',' + (n & 255) + ',' + a + ')';
  }
  function rateParts(bps) {
    if (!bps) return { v: '—', u: '' };
    var mb = bps / 1048576;
    if (mb >= 1) return { v: mb.toFixed(1), u: 'MB/s' };
    var kb = bps / 1024;
    if (kb >= 10) return { v: kb.toFixed(0), u: 'KB/s' };
    return { v: bps.toFixed(0), u: 'B/s' };
  }
  function rateText(bps) {
    var p = rateParts(bps);
    return p.v === '—' ? '—' : p.v + ' ' + p.u;
  }
  function pctText(r) {
    if (r === null || r === undefined) return '—';
    return (r * 100).toFixed(1);
  }
  function timeAgo(ts) {
    if (!ts) return '—';
    var s = Math.max(0, Math.round((Date.now() - ts) / 1000));
    if (s < 3) return '刚刚';
    if (s < 60) return s + ' 秒前';
    var m = Math.round(s / 60);
    return m + ' 分钟前';
  }
  function statusOf(p) {
    if (p.auth_status === 'online') return { cls: 'ok', text: '正常' };
    if (p.auth_status === 'reauthenticating') return { cls: 'warn', text: '认证中' };
    if (p.errors && p.errors.length) return { cls: 'bad', text: '认证失败' };
    return { cls: 'off', text: '未连接' };
  }
  function chipHtml(tag, clickable) {
    var c = API.PALETTE[tag.color] || API.PALETTE[0];
    var style = 'color:' + c + ';background:' + hexA(c, .13) + ';border-color:' + hexA(c, .3);
    var inner = '<span class="dot" style="background:' + c + '"></span>' + esc(tag.name);
    if (clickable) {
      return '<button class="chip" data-tag="' + tag.id + '" style="' + style + '" title="按标签筛选">' + inner + '</button>';
    }
    return '<span class="chip" style="' + style + '">' + inner + '</span>';
  }

  /* ---------- 视图状态 ---------- */
  var S = {
    selection: new Set(),
    anchor: null,
    sort: { key: 'rate_down_bps', dir: -1 },
    search: '',
    tagFilter: new Set(),
    route: '#/',
    scanning: false,
    // 上一次扫描里"存在但没参与调度"的网卡（没有可路由 IPv4），用于页脚提示。
    skipped: [],
    chart: null,
    editingTag: null,
    newTagColor: 0,
    loginBusy: false
  };

  /* ---------- DOM 引用 ---------- */
  var el = {};
  function cache() {
    el.sidebar = $('#sidebar');
    el.main = $('#view-main');
    el.tagsPage = $('#view-tags');
    el.settingsPage = $('#view-settings');
    el.list = $('#port-list');
    el.search = $('#search');
    el.searchWrap = $('.search');
    el.batch = $('#batch');
    el.foot = $('#side-foot');
    el.stats = $('#stats');
    el.tableBody = $('#port-rows');
    el.tableWrap = $('#tbl-wrap');
    el.tableCard = $('#tbl-card');
    el.chart = $('#chart-svg');
    el.chartBox = $('#chart-box');
    el.chartNow = $('#chart-now');
    el.chartNote = $('#chart-note');
    el.chartTip = $('#chart-tip');
    el.pill = $('#status-pill');
    el.username = $('#username');
    el.password = $('#password');
    el.remember = $('#remember');
    el.loginBtn = $('#btn-login');
    el.clearBtn = $('#btn-clear');
    el.modalLayer = $('#modal-layer');
    el.toasts = $('#toasts');
    el.ctxRoot = $('#ctx-root');
  }

  /* ============================================================
   * 1. 渲染：侧栏
   * ==========================================================*/
  function filteredPorts() {
    var ports = API.visiblePorts();
    var q = S.search.trim().toLowerCase();
    return ports.filter(function (p) {
      if (q) {
        var tagNames = API.tagsOf(p.id).map(function (t) { return t.name.toLowerCase(); }).join(' ');
        var hay = (p.interface + ' ' + p.source_ip + ' ' + (p.model || '') + ' ' + tagNames).toLowerCase();
        if (hay.indexOf(q) === -1) return false;
      }
      if (S.tagFilter.size) {
        var ids = API.tagsOf(p.id).map(function (t) { return t.id; });
        var hit = false;
        S.tagFilter.forEach(function (tid) { if (ids.indexOf(tid) !== -1) hit = true; });
        if (!hit) return false;
      }
      return true;
    });
  }

  function renderList() {
    var all = API.visiblePorts();
    var ports = filteredPorts();
    if (S.scanning) {
      el.list.innerHTML = '<div class="skel">' +
        [0, 1, 2].map(function () {
          return '<div class="skel-card" style="background:var(--card-2);animation:shimmer 1.2s infinite"></div>';
        }).join('') + '</div>';
      return;
    }
    if (!ports.length) {
      var ignored = API.ignoredIds().length;
      var title, desc, action;
      if (S.search || S.tagFilter.size) {
        title = '没有匹配的端口';
        desc = '试试换个关键词，或清除标签筛选。';
        action = '<button class="btn ghost" data-act="clear-filter">清除筛选</button>';
      } else if (ignored) {
        title = '端口都已隐藏';
        desc = '当前 ' + ignored + ' 个端口被移出列表，网卡本身仍在工作。';
        action = '<button class="btn ghost" data-act="manage-ignored">管理已忽略</button>';
      } else {
        title = '未检测到可用端口';
        desc = '请检查网卡与网线连接，或点击重新扫描。';
        action = '<button class="btn ghost" data-act="rescan">重新扫描</button>';
      }
      el.list.innerHTML = '<div class="empty inline">' +
        '<div class="empty-icon">' + I.plug + '</div>' +
        '<div class="empty-title">' + title + '</div>' +
        '<div class="empty-desc">' + desc + '</div>' +
        '<div class="empty-actions">' + action + '</div></div>';
      return;
    }
    el.list.innerHTML = ports.map(function (p) {
      var st = statusOf(p);
      var tags = API.tagsOf(p.id).map(function (t) { return chipHtml(t, false); }).join('');
      var rate = p.auth_status === 'online' ? rateText(p.rate_down_bps) : '—';
      return '<button class="port-card' + (S.selection.has(p.id) ? ' is-selected' : '') + '" data-id="' + esc(p.id) + '">' +
        '<div class="pc-top"><span class="dot ' + st.cls + '"></span>' +
        '<span class="pc-name">' + esc(p.interface) + '</span>' +
        '<span class="pc-rate">' + rate + '</span></div>' +
        '<div class="pc-meta">' + esc(p.source_ip) + ' · ' + esc(p.model || '未知设备') + '</div>' +
        (tags ? '<div class="pc-tags">' + tags + '</div>' : '') +
        '</button>';
    }).join('');
    void all;
  }

  function renderFoot() {
    var total = API.visiblePorts().length;
    var scanAt = API.lastScanAt();
    var ignored = API.ignoredIds().length;
    var left = '共检测到 ' + total + ' 个端口' + (scanAt ? ' · ' + timeAgo(scanAt) : '');
    var right = ignored ? '<button class="foot-link" data-act="manage-ignored">已忽略 ' + ignored + ' 个 · 管理</button>' : '';
    el.foot.innerHTML = '<span>' + left + '</span>' + right;
  }

  /* ============================================================
   * 2. 渲染：统计区
   * ==========================================================*/
  function renderStats() {
    var t = API.totals();
    var ports = API.ports();
    var badPort = ports.filter(function (p) {
      return p.auth_status === 'reauthenticating' || (p.errors && p.errors.length);
    })[0];
    var badFoot = badPort
      ? (badPort.interface + ' · ' + (badPort.errors[0] || '认证中'))
      : '全部端口正常';
    var ready = t.count - t.online;

    el.stats.innerHTML = [
      stat('正常端口', t.online, '个', '共 ' + t.count + ' 个，' + ready + ' 个未就绪', 'ok'),
      stat('异常端口', t.bad, '个', badFoot, 'bad'),
      stat('当前网速', rateParts(t.rate_down_bps).v, rateParts(t.rate_down_bps).u,
        '↑ ' + rateText(t.rate_up_bps) + '  ↓ ' + rateText(t.rate_down_bps), ''),
      stat('延迟', t.rtt_ms === null ? '—' : String(Math.round(t.rtt_ms)), 'ms',
        t.rtt_ms === null ? '未连接'
          : '健康度 ' + (typeof t.health_pct === 'number' ? t.health_pct + '%' : t.health_score.toFixed(2)), ''),
      stat('丢包率', pctText(t.loss_rate), '%', '主用端口 · 近 60 秒', '')
    ].join('');
  }

  function stat(label, value, unit, foot, tone) {
    var dotCls = tone === 'ok' ? 'ok' : tone === 'bad' ? 'bad' : '';
    return '<div class="stat"' + (tone ? ' data-tone="' + tone + '"' : '') + '>' +
      '<div class="stat-label">' + (dotCls ? '<span class="dot ' + dotCls + '"></span>' : '') + label + '</div>' +
      '<div class="stat-value"><span class="v">' + esc(value) + '</span>' +
      (unit ? '<span class="stat-unit">' + esc(unit) + '</span>' : '') + '</div>' +
      '<div class="stat-foot" title="' + esc(foot) + '">' + esc(foot) + '</div>' +
      '</div>';
  }

  /* ============================================================
   * 3. 渲染：表格
   * ==========================================================*/
  function sortedPorts() {
    var ports = filteredPorts().slice();
    var key = S.sort.key;
    ports.sort(function (a, b) {
      var av = a[key], bv = b[key];
      if (av === null || av === undefined) return 1;
      if (bv === null || bv === undefined) return -1;
      return (av - bv) * S.sort.dir;
    });
    return ports;
  }

  function renderTable() {
    var ports = sortedPorts();
    $$('th.sortable', el.tableCard).forEach(function (th) {
      var arrow = th.querySelector('.sort-arrow');
      var active = th.getAttribute('data-sort') === S.sort.key;
      if (arrow) arrow.textContent = active ? (S.sort.dir === -1 ? '↓' : '↑') : '';
    });

    if (S.scanning) {
      el.tableBody.innerHTML = [0, 1, 2].map(function () {
        return '<tr><td colspan="6"><div class="skel-bar" style="height:10px;width:60%"></div></td></tr>';
      }).join('');
      return;
    }

    if (!ports.length) {
      el.tableBody.innerHTML = '';
      showTableEmpty();
      return;
    }
    hideTableEmpty();

    el.tableBody.innerHTML = ports.map(function (p) {
      var st = statusOf(p);
      var online = p.auth_status === 'online';
      var tags = API.tagsOf(p.id).map(function (t) { return chipHtml(t, true); }).join('');
      return '<tr data-id="' + esc(p.id) + '" class="' + (S.selection.has(p.id) ? 'is-selected' : '') + '">' +
        '<td><div class="cell-port"><span class="dot ' + st.cls + '"></span>' +
        '<div class="cell-port-body"><div class="cell-port-name">' + esc(p.interface) + '</div>' +
        '<div class="cell-port-ip">' + esc(p.source_ip) + '</div></div></div></td>' +
        '<td><span class="badge ' + st.cls + '">' + st.text + '</span></td>' +
        '<td class="mono-num">' + (online ? rateText(p.rate_down_bps) : '<span class="muted-dash">—</span>') + '</td>' +
        '<td class="mono-num">' + (online && p.rtt_ms !== null ? p.rtt_ms + ' ms' : '<span class="muted-dash">—</span>') + '</td>' +
        '<td class="col-tags"><div class="chip-cell">' + (tags || '<span class="muted-dash">—</span>') + '</div></td>' +
        '<td><button class="row-menu-btn" data-menu="' + esc(p.id) + '" title="更多操作">' + I.dots + '</button></td>' +
        '</tr>';
    }).join('');
  }

  function showTableEmpty() {
    var box = $('#table-empty');
    var ignored = API.ignoredIds().length;
    var title, desc, action;
    if (S.search || S.tagFilter.size) {
      title = '没有符合条件的端口';
      desc = '当前筛选条件下没有端口。';
      action = '<button class="btn ghost" data-act="clear-filter">清除筛选</button>';
    } else if (ignored) {
      title = '端口都已隐藏';
      desc = '你移除了所有端口，可以随时恢复。';
      action = '<button class="btn ghost" data-act="manage-ignored">管理已忽略</button>';
    } else {
      title = '未检测到可用端口';
      desc = '请检查网卡与网线连接，或重新扫描。';
      action = '<button class="btn ghost" data-act="rescan">重新扫描</button>';
    }
    box.innerHTML = '<div class="empty">' +
      '<div class="empty-icon">' + I.plug + '</div>' +
      '<div class="empty-title">' + title + '</div>' +
      '<div class="empty-desc">' + desc + '</div>' +
      '<div class="empty-actions">' + action + '</div></div>';
    box.hidden = false;
  }
  function hideTableEmpty() { $('#table-empty').hidden = true; }

  /* ============================================================
   * 4. 渲染：曲线
   * ==========================================================*/
  function renderChart() {
    var hist = API.history();
    var m = API.ports().filter(function (p) { return p.id === API.mainPortId(); })[0];
    el.chartNow.innerHTML = m ? rateParts(m.rate_down_bps).v + '<small>' + rateParts(m.rate_down_bps).u + '</small>' : '—';

    if (!hist.length) {
      el.chart.innerHTML = '<div style="display:grid;place-items:center;height:100%;color:var(--faint);font-size:11px">等待数据</div>';
      if (el.chartNote) el.chartNote.textContent = '';
      return;
    }
    var W = 600, H = 100;
    var vals = hist.map(function (h) { return h.v; });
    var max = Math.max.apply(null, vals) * 1.18 || 1;
    var n = vals.length;
    // 采集期（点少）不要把线拉满整宽，否则看起来像一条贴底的假平线；
    // 固定按 60 秒的时间轴铺，数据随秒数从左往右长出来。
    var denom = Math.max(n - 1, 59);
    if (el.chartNote) el.chartNote.textContent = n < 60 ? '正在采集 ' + n + '/60 秒' : '';
    var pts = vals.map(function (v, i) {
      var x = (i / denom) * W;
      var y = H - (v / max) * (H - 6) - 3;
      return [x, y];
    });
    var line = pts.map(function (p, i) { return (i ? 'L' : 'M') + p[0].toFixed(1) + ' ' + p[1].toFixed(1); }).join(' ');
    var lastX = pts[pts.length - 1][0].toFixed(1);
    var area = line + ' L' + lastX + ' ' + H + ' L0 ' + H + ' Z';
    var sparse = n < 8;
    var dots = sparse
      ? '<g fill="var(--accent)">' + pts.map(function (p) {
        return '<circle cx="' + p[0].toFixed(1) + '" cy="' + p[1].toFixed(1) + '" r="1.6" vector-effect="non-scaling-stroke"/>';
      }).join('') + '</g>'
      : '';
    var grid = [0.25, 0.5, 0.75].map(function (g) {
      var y = (H - (g * (H - 6) + 3)).toFixed(1);
      return '<line x1="0" y1="' + y + '" x2="' + W + '" y2="' + y + '" stroke="var(--line)" stroke-width="1" stroke-dasharray="3 4" vector-effect="non-scaling-stroke"/>';
    }).join('');

    el.chart.innerHTML =
      '<svg viewBox="0 0 ' + W + ' ' + H + '" preserveAspectRatio="none">' +
      '<defs><linearGradient id="gArea" x1="0" y1="0" x2="0" y2="1">' +
      '<stop offset="0%" stop-color="var(--accent)" stop-opacity=".26"/>' +
      '<stop offset="100%" stop-color="var(--accent)" stop-opacity="0"/>' +
      '</linearGradient></defs>' +
      grid +
      (sparse ? '' : '<path d="' + area + '" fill="url(#gArea)"/>') +
      '<path d="' + line + '" fill="none" stroke="var(--accent)" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke"/>' +
      dots +
      '<line id="chart-hover" x1="0" y1="0" x2="0" y2="' + H + '" stroke="var(--accent)" stroke-width="1" stroke-dasharray="2 3" opacity="0" vector-effect="non-scaling-stroke"/>' +
      '</svg>';
    S.chart = { pts: pts, hist: hist, W: W, H: H };
  }

  /* ============================================================
   * 5. 渲染：认证区
   * ==========================================================*/
  /* 认证运行时状态：决定"清除凭据"按钮是否可用、账号是否需要回填 */
  function refreshAuthRuntime() {
    if (!API.refreshAuth) return Promise.resolve();
    return API.refreshAuth().then(function () { renderAuth(); }).catch(function () {});
  }
  function renderAuth() {
    var has = API.hasCredential();
    el.clearBtn.disabled = !has || S.loginBusy;
    if (has) {
      // 注意：不能把打码后的账号（2024***）写进输入框——
      // 用户再点"登录"就会拿这个假账号去认证。
      // 用 placeholder 提示"本机已保存凭据"即可。
      el.username.placeholder = '已保存 ' + (API.credentialUsername() || '账号');
      el.password.placeholder = '已保存（留空则沿用）';
      el.remember.checked = true;
    } else {
      el.username.placeholder = '账号';
      el.password.placeholder = '密码';
    }
    // 认证状态文案：直接来自守护进程，避免界面自己编故事。
    var box = document.getElementById('auth-state');
    if (!box) return;
    var st = API.authStatus ? API.authStatus() : null;
    if (!st) { box.textContent = ''; box.removeAttribute('data-state'); return; }
    box.textContent = st.message || '';
    box.setAttribute('data-state', st.running ? (has ? 'ok' : 'on') : (st.configured ? 'warn' : 'off'));
  }

  /* ============================================================
   * 6. 渲染：总入口
   * ==========================================================*/
  function renderAll() {
    renderStats();
    renderList();
    renderTable();
    renderFoot();
    renderChart();
    renderAuth();
    renderPill();
    renderBatch();
    renderTagsPage();
    renderSettingsPage();
    el.search.value = S.search;
    el.searchWrap.classList.toggle('has-value', !!S.search);
  }

  function renderPill() {
    var m = API.ports().filter(function (p) { return p.id === API.mainPortId(); })[0];
    if (!m) {
      el.pill.setAttribute('data-state', 'off');
      el.pill.innerHTML = '<span class="dot"></span>未连接';
      el.pill.title = '没有可用端口';
      return;
    }
    if (m.auth_status === 'reauthenticating') {
      el.pill.setAttribute('data-state', 'warn');
      el.pill.innerHTML = '<span class="dot"></span>认证中';
    } else if (m.auth_status === 'online') {
      el.pill.removeAttribute('data-state');
      el.pill.innerHTML = '<span class="dot"></span>已连接';
    } else {
      el.pill.setAttribute('data-state', 'off');
      el.pill.innerHTML = '<span class="dot"></span>未连接';
    }
    el.pill.title = '主用端口 ' + m.interface + ' · 最后在线 ' + timeAgo(m.last_seen ? Date.parse(m.last_seen) : 0);
  }

  function renderBatch() {
    var n = S.selection.size;
    el.batch.classList.toggle('is-open', n > 0);
    if (n > 0) {
      $('.batch-count', el.batch).textContent = '已选 ' + n + ' 项';
    }
  }

  function updateLive() {
    renderStats();
    renderChart();
    renderPill();
    renderFoot();
    // 侧栏速率与表格数值就地更新，避免每秒重建 DOM 丢失 hover/focus
    $$('.port-card', el.list).forEach(function (card) {
      var p = API.ports().filter(function (x) { return x.id === card.getAttribute('data-id'); })[0];
      if (!p) return;
      var rate = $('.pc-rate', card);
      if (rate) rate.textContent = p.auth_status === 'online' ? rateText(p.rate_down_bps) : '—';
    });
    $$('#port-rows tr[data-id]').forEach(function (tr) {
      var p = API.ports().filter(function (x) { return x.id === tr.getAttribute('data-id'); })[0];
      if (!p) return;
      var cells = tr.children;
      var st = statusOf(p);
      var badge = $('.badge', cells[1]);
      if (badge) { badge.className = 'badge ' + st.cls; badge.textContent = st.text; }
      cells[2].innerHTML = p.auth_status === 'online' ? rateText(p.rate_down_bps) : '<span class="muted-dash">—</span>';
      cells[3].innerHTML = (p.auth_status === 'online' && p.rtt_ms !== null) ? p.rtt_ms + ' ms' : '<span class="muted-dash">—</span>';
      var dot = $('.dot', cells[0]);
      if (dot) dot.className = 'dot ' + st.cls;
    });
  }

  /* ============================================================
   * 7. 选择
   * ==========================================================*/
  function selectOnly(id) {
    S.selection.clear();
    if (id) { S.selection.add(id); S.anchor = id; }
    renderList(); renderTable(); renderBatch();
  }
  function toggleSelect(id) {
    if (S.selection.has(id)) S.selection.delete(id); else S.selection.add(id);
    S.anchor = id;
    renderList(); renderTable(); renderBatch();
  }
  function rangeSelect(id) {
    var order = sortedPorts().map(function (p) { return p.id; });
    var a = order.indexOf(S.anchor);
    var b = order.indexOf(id);
    if (a < 0 || b < 0) { selectOnly(id); return; }
    S.selection.clear();
    for (var i = Math.min(a, b); i <= Math.max(a, b); i += 1) S.selection.add(order[i]);
    S.anchor = id;
    renderList(); renderTable(); renderBatch();
  }
  function handlePick(id, ev) {
    if (ev.ctrlKey || ev.metaKey) toggleSelect(id);
    else if (ev.shiftKey) rangeSelect(id);
    else selectOnly(S.selection.size === 1 && S.selection.has(id) ? null : id);
  }

  /* ============================================================
   * 8. 弹窗
   * ==========================================================*/
  var modalResolve = null;
  var modalCollect = null;   // 关闭前抓取弹窗内表单值，避免先清空再取值

  function openModal(cfg) {
    modalCollect = cfg.collect || null;
    el.modalLayer.hidden = false;
    el.modalLayer.innerHTML =
      '<div class="modal" role="dialog" aria-modal="true" aria-labelledby="modal-title">' +
      '<div class="modal-head"><div class="modal-title" id="modal-title">' + esc(cfg.title) + '</div>' +
      '<button class="modal-close" data-mo="cancel" title="关闭">' + I.x + '</button></div>' +
      '<div class="modal-body">' + (cfg.body || '') + '</div>' +
      '<div class="modal-foot">' +
      (cfg.cancelText === null ? '' : '<button class="btn ghost" data-mo="cancel">' + esc(cfg.cancelText || '取消') + '</button>') +
      '<button class="btn ' + (cfg.danger ? 'danger' : 'primary') + '" data-mo="ok">' + esc(cfg.okText || '确定') + '</button>' +
      '</div></div>';
    var okBtn = $('[data-mo="ok"]', el.modalLayer);
    okBtn.focus();
    return new Promise(function (resolve) { modalResolve = resolve; });
  }

  function closeModal(val) {
    el.modalLayer.hidden = true;
    el.modalLayer.innerHTML = '';
    var r = modalResolve; modalResolve = null;
    modalCollect = null;
    if (r) r(val);
  }

  function confirmAction(cfg) { return openModal(cfg); }

  /* ---------- 移除端口 ---------- */
  function askRemove(ids) {
    var names = ids.map(function (id) {
      var p = API.ports().filter(function (x) { return x.id === id; })[0];
      return p ? p.interface : id;
    });
    return confirmAction({
      title: '从列表移除 ' + ids.length + ' 个端口',
      body: '<strong>' + esc(names.join('、')) + '</strong> 将从列表中隐藏。' +
        '这只是本地隐藏，网卡本身不受影响，之后可以在列表底部的「已忽略」里恢复。',
      okText: '移除',
      danger: true
    }).then(function (ok) {
      if (!ok) return false;
      API.ignorePorts(ids);
      S.selection.clear();
      renderAll();
      toast('已移除 ' + ids.length + ' 个端口', 'ok');
      return true;
    });
  }

  /* ---------- 已忽略管理 ---------- */
  function askManageIgnored() {
    var ids = API.ignoredIds();
    if (!ids.length) { toast('没有被忽略的端口'); return Promise.resolve(); }
    var ports = API.ports().filter(function (p) { return ids.indexOf(p.id) !== -1; });
    var body = '<div class="modal-list">' + ports.map(function (p) {
      return '<label class="modal-list-item"><input type="checkbox" value="' + esc(p.id) + '" checked>' +
        esc(p.interface) + ' <span style="color:var(--faint);font-size:11px">' + esc(p.source_ip) + '</span></label>';
    }).join('') + '</div>';
    return confirmAction({
      title: '已忽略的端口',
      body: body,
      okText: '恢复选中',
      cancelText: '关闭',
      collect: function () {
        return $$('input[type=checkbox]:checked', el.modalLayer).map(function (i) { return i.value; });
      }
    }).then(function (picked) {
      if (!picked) return;
      var list = picked.length ? picked : ids;
      API.restorePorts(list);
      renderAll();
      toast('已恢复 ' + list.length + ' 个端口', 'ok');
    });
  }

  /* ---------- 打标签 ---------- */
  function askAssign(ids) {
    var tags = API.tags();
    if (!tags.length) { toast('还没有标签，先到标签管理里新建', 'warn'); return; }
    var cur = {};
    ids.forEach(function (id) {
      (API.tagsOf(id) || []).forEach(function (t) { cur[t.id] = true; });
    });
    var body = '<div class="modal-list">' + tags.map(function (t) {
      var c = API.PALETTE[t.color] || API.PALETTE[0];
      return '<label class="modal-list-item"><input type="checkbox" value="' + t.id + '"' + (cur[t.id] ? ' checked' : '') + '>' +
        '<span class="dot" style="background:' + c + '"></span>' + esc(t.name) + '</label>';
    }).join('') + '</div>';
    openModal({
      title: '给 ' + ids.length + ' 个端口打标签',
      body: body,
      okText: '应用',
      collect: function () {
        return $$('input[type=checkbox]:checked', el.modalLayer).map(function (i) { return i.value; });
      }
    }).then(function (picked) {
      if (!picked) return;
      ids.forEach(function (id) {
        var has = (API.tagsOf(id) || []).map(function (t) { return t.id; });
        has.forEach(function (tid) { if (picked.indexOf(tid) === -1) API.unassignTag(id, tid); });
        picked.forEach(function (tid) { if (has.indexOf(tid) === -1) API.assignTag([id], tid); });
      });
      renderAll();
      toast('已更新 ' + ids.length + ' 个端口的标签', 'ok');
    });
  }

  /* ---------- 登录 ---------- */
  function doLogin() {
    if (S.loginBusy) return;
    var u = el.username.value.trim();
    var p = el.password.value;
    var has = API.hasCredential();
    // 留空沿用本机凭据：勾过「记住密码」之后，第二次登录不该再逼用户重输一遍。
    // 之前这里直接 return 一条提示，用户实际感受就是"记住密码没用"。
    var reuse = has && !u && !p;
    if (!u && !p && !has) {
      toast('请输入校园网账号与密码', 'bad'); el.username.focus(); return;
    }
    if (!u && p) {
      el.username.classList.add('is-invalid');
      toast('请输入校园网账号', 'bad'); el.username.focus(); return;
    }
    if (!p && !has) {
      el.password.classList.add('is-invalid');
      toast('请输入密码', 'bad'); el.password.focus(); return;
    }
    el.username.classList.remove('is-invalid');
    el.password.classList.remove('is-invalid');

    var needConfirm = !reuse && el.remember.checked && !has;
    var flow = needConfirm
      ? confirmAction({
          title: '保存账号密码？',
          body: '将把 <strong>' + esc(u) + '</strong> 的账号与密码保存在本机，仅用于自动重连。' +
            '凭据只存在这台设备上，不会上传。',
          okText: '保存并登录'
        })
      : Promise.resolve(true);

    flow.then(function (ok) {
      if (!ok) return;
      S.loginBusy = true;
      el.loginBtn.disabled = true;
      el.loginBtn.innerHTML = '<span class="spinner"></span>登录中';
      // 真实认证：守护进程会用 Dr.COM 客户端实际登录一次，
      // 失败会带回具体原因（密码错、账号格式不对、门户不可达等）。
      API.login(u, p, reuse ? true : el.remember.checked).then(function (st) {
        toast((st && st.message) || '登录成功', 'ok');
        el.password.value = '';
      }).catch(function (err) {
        toast(err.message || '登录失败', 'bad');
      }).then(function () {
        S.loginBusy = false;
        el.loginBtn.disabled = false;
        el.loginBtn.textContent = '登录';
        renderAll();
        refreshAuthRuntime();
      });
    });
  }

  function doClearCredential() {
    confirmAction({
      title: '清除本机凭据',
      body: '将删除本机保存的账号与密码，端口会回到未认证状态。之后需要重新输入才能登录。',
      okText: '清除',
      danger: true
    }).then(function (ok) {
      if (!ok) return;
      API.clearCredential();
      el.username.value = '';
      el.password.value = '';
      el.remember.checked = false;
      renderAll();
      toast('凭据已清除', 'ok');
    });
  }

  /* ---------- 右键 / 行菜单 ---------- */
  function openPortMenu(x, y, id) {
    var p = API.ports().filter(function (pp) { return pp.id === id; })[0];
    if (!p) return;
    var ids = S.selection.has(id) ? Array.from(S.selection) : [id];
    showMenu(x, y, [
      { label: '打标签…', onClick: function () { askAssign(ids); } },
      { label: '复制 IP', onClick: function () { copyText(p.source_ip); } },
      { sep: true },
      { label: '从列表移除', danger: true, onClick: function () { askRemove(ids); } }
    ]);
  }

  function showMenu(x, y, items) {
    closeMenu();
    var box = document.createElement('div');
    box.className = 'ctx-menu';
    box.id = 'ctx-menu';
    box.innerHTML = items.map(function (it, i) {
      if (it.sep) return '<div class="ctx-sep"></div>';
      return '<button class="ctx-item' + (it.danger ? ' danger' : '') + '" data-i="' + i + '">' +
        (it.checked ? '<span style="width:12px;color:var(--accent)">' + I.check + '</span>' : '') +
        esc(it.label) + '</button>';
    }).join('');
    el.ctxRoot.appendChild(box);
    var r = box.getBoundingClientRect();
    box.style.left = Math.min(x, window.innerWidth - r.width - 8) + 'px';
    box.style.top = Math.min(y, window.innerHeight - r.height - 8) + 'px';
    box.addEventListener('click', function (ev) {
      var btn = ev.target.closest('[data-i]');
      if (!btn) return;
      var it = items[Number(btn.getAttribute('data-i'))];
      closeMenu();
      if (it && it.onClick) it.onClick();
    });
  }
  function closeMenu() {
    var m = $('#ctx-menu');
    if (m) m.remove();
  }

  function copyText(t) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(t).then(function () { toast('已复制 ' + t, 'ok'); },
        function () { toast('复制失败，请手动选择', 'bad'); });
    } else { toast('当前环境不支持剪贴板', 'warn'); }
  }

  /* ---------- 标签筛选菜单 ---------- */
  function openTagFilterMenu(anchor) {
    var tags = API.tags();
    if (!tags.length) { toast('还没有标签', 'warn'); return; }
    var items = tags.map(function (t) {
      var c = API.PALETTE[t.color] || API.PALETTE[0];
      return {
        label: t.name + '  (' + API.tagUsage(t.id) + ')',
        checked: S.tagFilter.has(t.id),
        onClick: function () {
          if (S.tagFilter.has(t.id)) S.tagFilter.delete(t.id); else S.tagFilter.add(t.id);
          renderList(); renderTable(); renderFoot();
        }
      };
    });
    if (S.tagFilter.size) {
      items.push({ sep: true });
      items.push({ label: '清除标签筛选', onClick: function () { S.tagFilter.clear(); renderList(); renderTable(); } });
    }
    var r = anchor.getBoundingClientRect();
    showMenu(r.left, r.bottom + 4, items);
  }

  /* ============================================================
   * 9. 标签管理页
   * ==========================================================*/
  function renderTagsPage() {
    var tags = API.tags();
    var rows = tags.map(function (t) {
      var c = API.PALETTE[t.color] || API.PALETTE[0];
      var editing = S.editingTag === t.id;
      var nameCell = editing
        ? '<input data-rename="' + t.id + '" value="' + esc(t.name) + '" maxlength="8">'
        : '<span>' + esc(t.name) + '</span>';
      var dots = API.PALETTE.map(function (col, i) {
        return '<button class="color-dot' + (i === t.color ? ' is-on' : '') + '" style="background:' + col + ';color:' + col + '" data-color="' + i + '" data-tag="' + t.id + '" title="改为该颜色"></button>';
      }).join('');
      return '<div class="tag-row" data-row="' + t.id + '">' +
        '<span class="tag-swatch" style="background:' + c + '"></span>' +
        '<span class="tag-name">' + nameCell + '</span>' +
        '<span class="tag-count">' + API.tagUsage(t.id) + ' 个端口</span>' +
        '<span class="color-dots">' + dots + '</span>' +
        '<span class="row-actions">' +
        '<button class="icon-btn" data-edit="' + t.id + '" title="改名">' + I.pencil + '</button>' +
        '<button class="icon-btn danger" data-del="' + t.id + '" title="删除">' + I.trash + '</button>' +
        '</span></div>';
    }).join('');

    var newDots = API.PALETTE.map(function (col, i) {
      return '<button class="color-dot' + (i === S.newTagColor ? ' is-on' : '') + '" style="background:' + col + ';color:' + col + '" data-newcolor="' + i + '"></button>';
    }).join('');

    var body = $('#tags-body');
    if (!body) return;
    body.innerHTML = (tags.length
      ? '<div class="tag-card">' + rows + '</div>'
      : '<div class="tag-card"><div class="empty inline" style="padding:40px 12px">' +
        '<div class="empty-title">还没有标签</div>' +
        '<div class="empty-desc">给端口分类后，排查会快很多。比如「宿舍」「实验室」「备用」。</div></div></div>') +
      '<div class="tag-card"><div class="tag-new">' +
      '<input id="new-tag-name" placeholder="新标签名，最多 8 字" maxlength="8">' +
      '<span class="color-dots">' + newDots + '</span>' +
      '<button class="btn primary" id="btn-create-tag">新建</button>' +
      '</div></div>';
    var nameInput = $('#new-tag-name');
    if (nameInput && S.focusNewTag) { nameInput.focus(); S.focusNewTag = false; }
    var rename = $('[data-rename]');
    if (rename) { rename.focus(); rename.select(); }
  }

  /* ============================================================
   * 10. 设置页
   * ==========================================================*/
  function renderSettingsPage() {
    var wrap = $('#settings-body');
    if (!wrap) return;
    var s = API.settings();
    function sw(key) {
      return '<button class="switch" role="switch" aria-checked="' + (s[key] ? 'true' : 'false') + '" data-switch="' + key + '"></button>';
    }
    function seg(key, opts) {
      return '<div class="segment">' + opts.map(function (o) {
        return '<button class="seg" aria-pressed="' + (String(s[key]) === String(o.v) ? 'true' : 'false') + '" data-seg="' + key + '" data-val="' + o.v + '">' + o.t + '</button>';
      }).join('') + '</div>';
    }
    var themeCard = function (key, cls, name, desc) {
      return '<button class="theme-card" aria-pressed="' + (s.theme === key ? 'true' : 'false') + '" data-theme-pick="' + key + '">' +
        '<span class="theme-preview ' + cls + '"><span class="tp-side"></span><span class="tp-body">' +
        '<span class="tp-line short"></span><span class="tp-card"></span></span></span>' +
        '<span class="theme-name">' + name + '</span><div class="theme-desc">' + desc + '</div></button>';
    };

    wrap.innerHTML =
      '<div class="set-group"><div class="group-title">通用行为</div>' +
      row('开机自启', '登录系统后自动运行（桌面端生效）', sw('autoStart')) +
      row('关闭窗口时最小化到托盘', '不退出程序，继续在后台保持连接', sw('minimizeToTray')) +
      row('启动后自动认证', '使用已保存的凭据自动登录校园网', sw('autoLogin')) +
      '</div>' +
      '<div class="set-group"><div class="group-title">采集与刷新</div>' +
      row('轮询间隔', '每隔多久拉取一次端口状态', seg('pollInterval', [
        { v: 1000, t: '1 秒' }, { v: 2000, t: '2 秒' }, { v: 5000, t: '5 秒' }
      ])) +
      row('请求超时', '单次请求超过该时长即视为失败', '<input class="num-input" type="number" min="500" max="10000" step="100" value="' + s.timeout + '" id="set-timeout"><span style="font-size:11px;color:var(--faint)">ms</span>') +
      '</div>' +
      '<div class="set-group"><div class="group-title">外观</div>' +
      '<div class="set-row"><div><div class="set-label">主题</div><div class="set-desc">切换后立即生效，并记住你的选择</div></div></div>' +
      '<div class="theme-cards">' +
      themeCard('dark', 'tp-dark', '深色 · 仪表盘', '长时间挂后台，数字更醒目') +
      themeCard('light', 'tp-light', '浅色 · 系统风', '白天或投屏时更接近系统观感') +
      '</div></div>' +
      '<div class="set-group"><div class="group-title">关于与诊断</div>' +
      row('版本', 'CAU WIFI HELPER 1.0.0（原型）', '<button class="mini-btn" id="btn-copy-version">复制版本号</button>') +
      row('日志目录', '桌面端可打开本机日志文件夹', '<button class="mini-btn" disabled title="桌面端可用">打开</button>') +
      row('配置文件', '桌面端可打开 caulinkd 配置文件', '<button class="mini-btn" disabled title="桌面端可用">打开</button>') +
      row('检查更新', '桌面端联网检查新版本', '<button class="mini-btn" disabled title="桌面端可用">检查更新</button>') +
      row('恢复默认设置', '重置全部偏好，不影响标签与凭据', '<button class="btn danger-ghost" id="btn-reset">恢复默认</button>') +
      '</div>';

    function row(label, desc, control) {
      return '<div class="set-row"><div><div class="set-label">' + label + '</div>' +
        '<div class="set-desc">' + desc + '</div></div>' +
        '<div class="set-control">' + control + '</div></div>';
    }
  }

  /* ============================================================
   * 11. Toast
   * ==========================================================*/
  function toast(msg, tone) {
    var d = document.createElement('div');
    d.className = 'toast';
    var color = tone === 'bad' ? 'var(--bad)' : tone === 'warn' ? 'var(--warn)' : 'var(--ok)';
    d.innerHTML = '<span class="dot" style="background:' + color + '"></span><span>' + esc(msg) + '</span>';
    el.toasts.appendChild(d);
    setTimeout(function () {
      d.classList.add('is-out');
      setTimeout(function () { d.remove(); }, 200);
    }, 2600);
  }

  /* ============================================================
   * 12. 路由
   * ==========================================================*/
  /* 主界面刷新：逐个渲染并各自容错。
   *
   * 背景：之前这里是 renderList(), renderTable(), renderStats(), ... 串联调用。
   * 只要其中一个抛异常，后面的就全被跳过，而这些异常又被 notify 的
   * try/catch 静默吞掉——结果就是"数据在变、界面不动，必须手动点一下才更新"。
   * 逐个 try 之后，单个模块出错不会拖垮整屏刷新。 */
  function renderMainSafely() {
    [renderStats, renderList, renderTable, renderFoot, renderBatch, renderPill].forEach(function (fn) {
      try {
        fn();
      } catch (e) {
        if (window.console) console.error('[caulink] 渲染失败:', fn && fn.name, e);
      }
    });
  }

  /* 当前是否在主界面（hash 为空、'#'、'#/' 都算） */
  function onMainRoute() {
    return S.route === '#/' || S.route === '#' || S.route === '';
  }

  function route() {
    var h = location.hash || '#/';
    S.route = h;
    var isMain = h === '#/' || h === '#';
    el.main.hidden = !isMain;
    el.sidebar.hidden = !isMain;
    el.tagsPage.hidden = h !== '#/tags';
    el.settingsPage.hidden = h !== '#/settings';
    if (h === '#/tags') renderTagsPage();
    if (h === '#/settings') renderSettingsPage();
  }

  /* ============================================================
   * 13. 事件绑定
   * ==========================================================*/

  function bind() {
    /* 全局点击：关菜单 */
    document.addEventListener('click', function (ev) {
      if (!ev.target.closest('.ctx-menu')) closeMenu();
    });
    document.addEventListener('keydown', function (ev) {
      if (ev.key === 'Escape') {
        if (!el.modalLayer.hidden) { closeModal(false); return; }
        closeMenu();
      }
    });

    /* 标题栏 */
    $('#btn-settings').addEventListener('click', function () { location.hash = '#/settings'; });
    $$('.win-btn').forEach(function (b) {
      b.addEventListener('click', function () { toast('窗口控制由桌面端接管，原型中不可用', 'warn'); });
    });

    /* 侧栏 */
    $('#btn-rescan').addEventListener('click', function () {
      if (S.scanning) return;
      S.scanning = true;
      renderList(); renderTable();
      // 让守护进程按网卡现状校正源列表：
      // DHCP 换了地址、插拔过网线时，不重启程序也能跟上。
      var task = API.rescanSources
        ? API.rescanSources()
        : Promise.resolve(null);
      task.then(function (res) {
        S.scanning = false;
        renderAll();
        refreshAuthRuntime();
        // 后端会带回"新增 1，移除 1"这类摘要；没有就退回通用文案。
        var msg = (res && res.message) || ('扫描完成，共 ' + API.ports().length + ' 个端口');
        // 插了网卡却没出现时，把"为什么没出现"直接说出来。
        // 没有可路由 IPv4 的网卡不会进池（认证也绑不上），但用户需要知道它们被看见了。
        // 扫描会先替用户把"无线但没连上"的网卡连上校园网，成功的要报出来，
        // 否则用户会以为它本来就连着。
        var auto = (res && res.auto_connected) || [];
        if (auto.length) {
          msg += '；已自动连接校园网：' + auto.join('、');
        }
        var skipped = (res && res.skipped) || [];
        if (skipped.length) {
          msg += '；另有 ' + skipped.length + ' 个网卡无可用 IPv4（' +
            skipped.slice(0, 3).map(function (s) { return s.interface; }).join('、') + '）';
        }
        toast(msg, (skipped.length && !auto.length) ? 'warn' : 'ok');
      }).catch(function (err) {
        S.scanning = false;
        renderAll();
        toast(err.message || '扫描失败', 'bad');
      });
    });
    el.search.addEventListener('input', function () {
      S.search = el.search.value;
      el.searchWrap.classList.toggle('has-value', !!S.search);
      renderList(); renderTable(); renderFoot();
    });
    $('.search-clear').addEventListener('click', function () {
      S.search = '';
      el.search.value = '';
      el.searchWrap.classList.remove('has-value');
      renderList(); renderTable(); renderFoot();
      el.search.focus();
    });
    el.list.addEventListener('click', function (ev) {
      var card = ev.target.closest('.port-card');
      if (card) { handlePick(card.getAttribute('data-id'), ev); return; }
      var act = ev.target.closest('[data-act]');
      if (act) doAct(act.getAttribute('data-act'));
    });
    el.list.addEventListener('contextmenu', function (ev) {
      var card = ev.target.closest('.port-card');
      if (!card) return;
      ev.preventDefault();
      var id = card.getAttribute('data-id');
      if (!S.selection.has(id)) selectOnly(id);
      openPortMenu(ev.clientX, ev.clientY, id);
    });
    el.foot.addEventListener('click', function (ev) {
      var act = ev.target.closest('[data-act]');
      if (act) doAct(act.getAttribute('data-act'));
    });
    state_batch_bind();
    function state_batch_bind() {
      $('#batch-tag').addEventListener('click', function () {
        if (S.selection.size) askAssign(Array.from(S.selection));
      });
      $('#batch-remove').addEventListener('click', function () {
        if (S.selection.size) askRemove(Array.from(S.selection));
      });
    }

    /* 账号区 */
    $('#pw-toggle').addEventListener('click', function () {
      var isPw = el.password.type === 'password';
      el.password.type = isPw ? 'text' : 'password';
      this.innerHTML = isPw ? I.eyeOff : I.eye;
      this.title = isPw ? '隐藏密码' : '显示密码';
    });
    el.loginBtn.addEventListener('click', doLogin);
    el.clearBtn.addEventListener('click', doClearCredential);
    el.password.addEventListener('keydown', function (ev) { if (ev.key === 'Enter') doLogin(); });
    el.username.addEventListener('keydown', function (ev) { if (ev.key === 'Enter') el.password.focus(); });

    /* 表格 */
    $$('th.sortable', el.tableCard).forEach(function (th) {
      th.addEventListener('click', function () {
        var key = th.getAttribute('data-sort') === 'rate' ? 'rate_down_bps' : 'rtt_ms';
        if (S.sort.key === key) S.sort.dir *= -1; else { S.sort.key = key; S.sort.dir = key === 'rtt_ms' ? 1 : -1; }
        renderTable();
      });
    });
    el.tableBody.addEventListener('click', function (ev) {
      var chip = ev.target.closest('[data-tag]');
      if (chip) {
        var tid = chip.getAttribute('data-tag');
        if (S.tagFilter.has(tid)) S.tagFilter.delete(tid); else { S.tagFilter.clear(); S.tagFilter.add(tid); }
        renderList(); renderTable(); renderFoot();
        return;
      }
      var menu = ev.target.closest('[data-menu]');
      if (menu) {
        var r = menu.getBoundingClientRect();
        openPortMenu(r.left - 130, r.bottom + 4, menu.getAttribute('data-menu'));
        return;
      }
      var tr = ev.target.closest('tr[data-id]');
      if (tr) handlePick(tr.getAttribute('data-id'), ev);
    });
    el.tableBody.addEventListener('contextmenu', function (ev) {
      var tr = ev.target.closest('tr[data-id]');
      if (!tr) return;
      ev.preventDefault();
      var id = tr.getAttribute('data-id');
      if (!S.selection.has(id)) selectOnly(id);
      openPortMenu(ev.clientX, ev.clientY, id);
    });
    $('#btn-tag-filter').addEventListener('click', function () { openTagFilterMenu(this); });

    /* 弹窗 */
    el.modalLayer.addEventListener('click', function (ev) {
      if (ev.target === el.modalLayer) { closeModal(false); return; }
      var btn = ev.target.closest('[data-mo]');
      if (!btn) return;
      if (btn.getAttribute('data-mo') === 'ok') {
        var picked = modalCollect ? modalCollect() : true;
        closeModal(picked === undefined ? true : picked);
      } else {
        closeModal(false);
      }
    });

    /* 图表 hover */
    el.chartBox.addEventListener('mousemove', function (ev) {
      if (!S.chart) return;
      var rect = el.chartBox.getBoundingClientRect();
      var rel = Math.min(1, Math.max(0, (ev.clientX - rect.left) / rect.width));
      var idx = Math.round(rel * (S.chart.pts.length - 1));
      var pt = S.chart.pts[idx];
      var item = S.chart.hist[idx];
      if (!pt || !item) return;
      var line = $('#chart-hover', el.chart);
      if (line) { line.setAttribute('x1', pt[0]); line.setAttribute('x2', pt[0]); line.setAttribute('opacity', '1'); }
      var d = new Date(item.t);
      var hh = String(d.getHours()).padStart(2, '0');
      var mm = String(d.getMinutes()).padStart(2, '0');
      var ss = String(d.getSeconds()).padStart(2, '0');
      el.chartTip.innerHTML = '<em>' + hh + ':' + mm + ':' + ss + '</em>' + rateText(item.v);
      el.chartTip.classList.add('is-on');
      el.chartTip.style.left = (rel * rect.width) + 'px';
    });
    el.chart.addEventListener('mouseleave', function () {
      el.chartTip.classList.remove('is-on');
      var line = $('#chart-hover', el.chart);
      if (line) line.setAttribute('opacity', '0');
    });

    /* 页面切换 */
    window.addEventListener('hashchange', route);
    $('#back-tags').addEventListener('click', function () { location.hash = '#/'; });
    $('#back-settings').addEventListener('click', function () { location.hash = '#/'; });
    $('#btn-new-tag').addEventListener('click', function () { S.focusNewTag = true; renderTagsPage(); });
    $('#tags-body').addEventListener('click', onTagsClick);
    $('#tags-body').addEventListener('keydown', onTagsKey);
    $('#settings-body').addEventListener('click', onSettingsClick);
    $('#settings-body').addEventListener('change', onSettingsChange);

    function onTagsClick(ev) {
      var del = ev.target.closest('[data-del]');
      if (del) {
        var id = del.getAttribute('data-del');
        var tag = API.tags().filter(function (t) { return t.id === id; })[0];
        if (!tag) return;
        var usage = API.tagUsage(id);
        confirmAction({
          title: '删除标签「' + tag.name + '」',
          body: '该标签会从 ' + usage + ' 个端口上移除，端口本身不受影响。',
          okText: '删除',
          danger: true
        }).then(function (ok) {
          if (!ok) return;
          API.deleteTag(id);
          renderTagsPage(); renderAll();
          toast('标签已删除', 'ok');
        });
        return;
      }
      var edit = ev.target.closest('[data-edit]');
      if (edit) { S.editingTag = edit.getAttribute('data-edit'); renderTagsPage(); return; }
      var color = ev.target.closest('[data-color]');
      if (color) {
        API.setTagColor(color.getAttribute('data-tag'), Number(color.getAttribute('data-color')));
        renderTagsPage(); renderAll();
        return;
      }
      var nc = ev.target.closest('[data-newcolor]');
      if (nc) { S.newTagColor = Number(nc.getAttribute('data-newcolor')); renderTagsPage(); return; }
      if (ev.target.closest('#btn-create-tag')) {
        var input = $('#new-tag-name');
        var res = API.createTag(input.value, S.newTagColor);
        if (!res.ok) { toast(res.message, 'bad'); input.focus(); return; }
        S.newTagColor = 0;
        renderTagsPage(); renderAll();
        toast('已新建标签「' + res.tag.name + '」', 'ok');
        var again = $('#new-tag-name');
        if (again) again.focus();
      }
    }

    function onTagsKey(ev) {
      var rename = ev.target.closest('[data-rename]');
      if (rename) {
        if (ev.key === 'Enter') {
          var res = API.renameTag(rename.getAttribute('data-rename'), rename.value);
          if (!res.ok) { toast(res.message, 'bad'); return; }
          S.editingTag = null;
          renderTagsPage(); renderAll();
          toast('标签已改名', 'ok');
        } else if (ev.key === 'Escape') {
          S.editingTag = null; renderTagsPage();
        }
        return;
      }
      if (ev.target.id === 'new-tag-name' && ev.key === 'Enter') {
        $('#btn-create-tag').click();
      }
    }

    /* ============================================================
     * Steam 多 IP 开关
     * ------------------------------------------------------------
     * 打开 → POST /api/steam-mode：把系统代理按在 8080，重启 Steam，
     *        并持续按住 90 秒，让 Steam 启动时把代理缓存下来。
     * 关闭 → POST /api/steam-mode/cancel：停止按住。
     * 每 1.5 秒轮询一次进度，显示在开关下方。
     * ==========================================================*/
    var steamTimer = null;

    function setSteamUI(on, detail) {
      var sw = document.getElementById('steam-mode-switch');
      var d = document.getElementById('steam-mode-detail');
      if (sw) sw.setAttribute('aria-checked', on ? 'true' : 'false');
      if (d && detail) d.textContent = detail;
    }

    function steamModeFetch(path, method) {
      return fetch(path, {
        method: method,
        headers: { 'X-Caulink-Action': '1' }
      }).then(function (r) {
        return r.json().catch(function () { return {}; }).then(function (body) {
          if (!r.ok) throw new Error(body.error || ('HTTP ' + r.status));
          return body;
        });
      });
    }

    function steamModePoll() {
      steamModeFetch('/api/steam-mode', 'GET').then(function (st) {
        var text = st.detail || st.phase;
        if (st.remain_seconds > 0) text += '（还剩 ' + st.remain_seconds + ' 秒）';
        else if (st.remain_seconds < 0) text += '（直到手动关闭）';
        renderSteamStatus(st, text);
        if (!st.running) {
          clearInterval(steamTimer);
          steamTimer = null;
        }
      }).catch(function () {
        clearInterval(steamTimer);
        steamTimer = null;
      });
    }

    // 卡片上的点击（与设置页共用同一套逻辑）
    document.addEventListener('click', function (ev) {
      var sw = ev.target.closest('[data-steam-mode]');
      if (!sw) return;
      var turningOn = sw.getAttribute('aria-checked') !== 'true';
      if (turningOn) {
        steamModeRequest(true);
      } else {
        confirmAction({
          title: '关闭 Steam 多 IP 模式',
          body: '此操作会<strong>关闭 Steam</strong>。' +
            '关闭后 Steam 的下载将恢复为单链路（走 VPN 或直连），不再多 IP 并行。',
          okText: '确认关闭 Steam',
          danger: true
        }).then(function (ok) {
          if (ok) steamModeRequest(false);
        });
      }
    });
    /* 把状态渲染到开关下方；manual 阶段额外给"复制脚本路径"按钮 */
    function renderSteamStatus(st, text) {
      // script 阶段：已交给辅助脚本，开关应显示为"开"；
      // failed / idle 才是关。
      // done / running / script 视为开启；manual（本机受限）与 failed 视为未开启
      var on = st.running || st.phase === 'done' || st.phase === 'script';
      setSteamUI(on, text);
      var box = document.getElementById('steam-mode-detail');
      if (!box) return;
      if (st.phase === 'manual' && st.script) {
        box.innerHTML = esc(text) +
          ' <button class="mini-btn" id="btn-copy-steam-cmd">复制脚本路径</button>';
      }
    }

    function steamModeRequest(on) {
      if (!steamTimer) steamTimer = setInterval(steamModePoll, 1500);
      var path = on ? '/api/steam-mode' : '/api/steam-mode/cancel';
      steamModeFetch(path, 'POST').then(function (st) {
        setSteamUI(on, st.detail || (on ? '正在重启 Steam…' : '已取消'));
        if (on) toast('正在重启 Steam 并按住代理，约 90 秒完成', 'ok');
        else toast('已取消', 'ok');
        steamModePoll();
      }).catch(function (e) {
        setSteamUI(false, '执行失败：' + e.message + '（未连接守护进程时无此接口）');
        toast('执行失败：' + e.message, 'bad');
        clearInterval(steamTimer);
        steamTimer = null;
      });
    }
    function onSettingsClick(ev) {
      var sm = ev.target.closest('[data-steam-mode]');
      if (sm) {
        var turningOn = sm.getAttribute('aria-checked') !== 'true';
        if (turningOn) {
          steamModeRequest(true);
        } else {
          confirmAction({
            title: '关闭 Steam 多 IP 模式',
            body: '此操作会<strong>关闭 Steam</strong>。' +
              '关闭后 Steam 的下载将恢复为单链路（走 VPN 或直连），不再多 IP 并行。',
            okText: '确认关闭 Steam',
            danger: true
          }).then(function (ok) {
            if (ok) steamModeRequest(false);
          });
        }
        return;
      }
      var sw = ev.target.closest('[data-switch]');
      if (sw) {
        var key = sw.getAttribute('data-switch');
        var cur = API.settings()[key];
        var patch = {}; patch[key] = !cur;
        API.updateSettings(patch);
        renderSettingsPage();
        toast('已' + (patch[key] ? '开启' : '关闭') + labelOf(key), 'ok');
        return;
      }
      var seg = ev.target.closest('[data-seg]');
      if (seg) {
        var k = seg.getAttribute('data-seg');
        var v = seg.getAttribute('data-val');
        var p2 = {}; p2[k] = k === 'pollInterval' ? Number(v) : v;
        API.updateSettings(p2);
        if (k === 'pollInterval') startTicker();
        renderSettingsPage();
        return;
      }
      var tc = ev.target.closest('[data-theme-pick]');
      if (tc) {
        API.updateSettings({ theme: tc.getAttribute('data-theme-pick') });
        renderSettingsPage();
        toast('已切换为' + (tc.getAttribute('data-theme-pick') === 'dark' ? '深色' : '浅色') + '主题', 'ok');
        return;
      }
      if (ev.target.closest('#btn-copy-version')) {
        copyText('CAU WIFI HELPER 1.0.0');
        return;
      }
      if (ev.target.closest('#btn-copy-steam-cmd')) {
        steamModeFetch('/api/steam-mode', 'GET').then(function (st) {
          if (st.script) {
            copyText(st.script);
            toast('脚本路径已复制，可在资源管理器地址栏直接粘贴', 'ok');
          }
        }).catch(function () { toast('读取失败', 'bad'); });
        return;
      }
      if (ev.target.closest('#btn-reset')) {
        confirmAction({
          title: '恢复默认设置',
          body: '所有偏好将回到初始值（深色主题、1 秒轮询、超时 3000ms）。标签与凭据不受影响。',
          okText: '恢复默认',
          danger: true
        }).then(function (ok) {
          if (!ok) return;
          API.resetSettings();
          startTicker();
          document.documentElement.setAttribute('data-theme', API.settings().theme);
          renderSettingsPage();
          toast('已恢复默认设置', 'ok');
        });
      }
    }

    function onSettingsChange(ev) {
      if (ev.target.id === 'set-timeout') {
        var v = Math.min(10000, Math.max(500, Number(ev.target.value) || 3000));
        ev.target.value = v;
        API.updateSettings({ timeout: v });
        toast('超时已设为 ' + v + ' ms', 'ok');
      }
    }

    function labelOf(key) {
      return ({ autoStart: '开机自启', minimizeToTray: '最小化到托盘', autoLogin: '自动认证' })[key] || '设置';
    }

    function doAct(act) {
      if (act === 'rescan') { $('#btn-rescan').click(); }
      else if (act === 'clear-filter') {
        S.search = ''; S.tagFilter.clear();
        el.search.value = ''; el.searchWrap.classList.remove('has-value');
        renderList(); renderTable(); renderFoot();
      } else if (act === 'manage-ignored') { askManageIgnored(); }
    }
  }

  /* ============================================================
   * 14. 定时器
   * ==========================================================*/
  var ticker = null;
  function startTicker() {
    if (ticker) clearInterval(ticker);
    var iv = API.settings().pollInterval || 1000;
    ticker = setInterval(function () {
      if (!onMainRoute()) return;
      API.tick();
      updateLive();
      // 双保险：即使 notify 链路断了，界面也保持每秒刷新。
      renderMainSafely();
    }, iv);
  }

  /* ============================================================
   * 15. 演示参数（便于截图与自测）
   *   ?theme=light   ?view=tags|settings   ?empty=1
   *   ?search=zzz    ?dialog=remove        ?select=1
   * ==========================================================*/
  function applyDemo() {
    var q = new URLSearchParams(location.search);
    if (q.get('theme')) API.updateSettings({ theme: q.get('theme') });
    if (q.get('empty') === '1') {
      API.ignorePorts(API.ports().map(function (p) { return p.id; }));
    }
    if (q.get('select') === '1') {
      var ids = API.visiblePorts().map(function (p) { return p.id; });
      ids.slice(0, 2).forEach(function (id) { S.selection.add(id); });
      S.anchor = ids[0];
    }
    if (q.get('search')) { S.search = q.get('search'); }
    if (q.get('view')) location.hash = '#/' + q.get('view');
    if (q.get('dialog') === 'remove') {
      setTimeout(function () { askRemove(API.visiblePorts().slice(0, 2).map(function (p) { return p.id; })); }, 60);
    }
  }

  /* ============================================================
   * 16. 启动
   * ==========================================================*/
  function boot() {
    API.init();
    cache();
    bind();
    applyDemo();
    renderAll();
    // 问一次守护进程：当前是不是已经登录过（决定"清除凭据"是否可用）
    refreshAuthRuntime();
    startTicker();
    route();
    API.onChange(function () {
      if (onMainRoute()) renderMainSafely();
    });
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();
})();
