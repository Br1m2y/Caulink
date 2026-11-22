/* ============================================================================
 * CAU WIFI HELPER · mock.js
 * 数据层：端口、调度、标签、凭据、偏好、60 秒速率历史。
 * 对外只暴露 window.MockAPI，UI 不直接碰 localStorage，也不直接改状态。
 * 换成真接口时，只需把 scanPorts() / fetchStatus() / login() 三处改成 fetch。
 * ==========================================================================*/
(function () {
  'use strict';

  var STORE_KEY = 'cau-helper:v1';

  /* ---------- 标签色板（8 色，深浅主题通用） ---------- */
  var PALETTE = [
    '#4a8cff', '#35d39a', '#8c96a7', '#f2b544',
    '#ff6b6b', '#b18cff', '#4ecfd9', '#ff9f6b'
  ];

  /* ---------- 端口初始数据：照本机真实网卡 ---------- */
  var PORT_DEFS = [
    {
      id: 'WLAN@10.6.50.103',
      interface: 'WLAN',
      source_ip: '10.6.50.103',
      model: 'Intel Wi-Fi 6 AX203',
      auth_status: 'online',
      health_score: 0.96,
      rtt_ms: 23,
      loss_rate: 0.002,
      schedulable: true,
      weight: 80,
      active_conns: 34,
      total_conns: 412,
      bytes_up: 734003200,
      bytes_down: 4294967296,
      rate_up_bps: 1258291,
      rate_down_bps: 13000000,
      last_seen: null,
      errors: []
    },
    {
      id: '以太网@169.254.174.133',
      interface: '以太网',
      source_ip: '169.254.174.133',
      model: 'Realtek PCIe GbE Family Controller',
      auth_status: 'offline',
      health_score: 0,
      rtt_ms: null,
      loss_rate: null,
      schedulable: false,
      weight: 0,
      active_conns: 0,
      total_conns: 0,
      bytes_up: 0,
      bytes_down: 0,
      rate_up_bps: 0,
      rate_down_bps: 0,
      last_seen: null,
      errors: []
    },
    {
      id: '以太网 2@169.254.178.208',
      interface: '以太网 2',
      source_ip: '169.254.178.208',
      model: 'Sangfor SSL VPN CS Support System VNIC',
      auth_status: 'offline',
      health_score: 0,
      rtt_ms: null,
      loss_rate: null,
      schedulable: false,
      weight: 0,
      active_conns: 0,
      total_conns: 0,
      bytes_up: 0,
      bytes_down: 0,
      rate_up_bps: 0,
      rate_down_bps: 0,
      last_seen: null,
      errors: ['Portal 认证失败：该账号已在其他设备登录']
    }
  ];

  var DEFAULT_SETTINGS = {
    autoStart: false,
    minimizeToTray: true,
    autoLogin: false,
    pollInterval: 1000,
    timeout: 3000,
    theme: 'dark'
  };

  /* ---------- 内部状态 ---------- */
  var state = {
    ports: [],
    tags: [],
    assign: {},
    ignored: [],
    settings: {},
    scheduler: { strategy: 'sticky-weight', sticky_id: null, sticky_ip: null, candidates: 1 },
    history: [],
    lastScanAt: 0,
    scanning: false
  };

  /* ---------- 与守护进程的写接口 ----------
   * 控制面默认只读；写操作要求 X-Caulink-Action 头（防止浏览器里
   * 任意页面误触"退出登录"或"杀 Steam"）。 */
  var CONTROL_BASE = 'http://127.0.0.1:7788';
  var authRuntime = {};

  function controlFetch(path, opts) {
    opts = opts || {};
    var headers = opts.headers || {};
    headers['X-Caulink-Action'] = '1';
    if (opts.body) headers['Content-Type'] = 'application/json';
    opts.headers = headers;
    return fetch(CONTROL_BASE + path, opts).then(function (r) {
      return r.text().then(function (txt) {
        var body = {};
        try { body = txt ? JSON.parse(txt) : {}; } catch (e) { body = {}; }
        if (!r.ok) throw new Error(body.error || ('请求失败（HTTP ' + r.status + '）'));
        return body;
      });
    });
  }

  var listeners = [];
  var uid = 0;

  function nowIso() { return new Date().toISOString(); }
  function clone(o) { return JSON.parse(JSON.stringify(o)); }
  function rand(a, b) { return a + Math.random() * (b - a); }
  function clamp(v, a, b) { return Math.min(b, Math.max(a, v)); }
  function nextId(prefix) { uid += 1; return prefix + '-' + Date.now().toString(36) + uid.toString(36); }

  /* ---------- 持久化 ---------- */
  function persist() {
    try {
      localStorage.setItem(STORE_KEY, JSON.stringify({
        tags: state.tags,
        assign: state.assign,
        ignored: state.ignored,
        settings: state.settings,
          }));
    } catch (e) { /* 隐私模式下静默失败，UI 仍可用 */ }
  }

  function restore() {
    var raw = null;
    try { raw = localStorage.getItem(STORE_KEY); } catch (e) { raw = null; }
    var saved = null;
    if (raw) { try { saved = JSON.parse(raw); } catch (e) { saved = null; } }

    state.tags = (saved && saved.tags) || [
      { id: 't-dorm', name: '宿舍', color: 0 },
      { id: 't-main', name: '主用', color: 1 },
      { id: 't-backup', name: '备用', color: 2 },
      { id: 't-vpn', name: 'VPN', color: 6 }
    ];
    state.assign = (saved && saved.assign) || {
      'WLAN@10.6.50.103': ['t-dorm', 't-main'],
      '以太网@169.254.174.133': ['t-backup'],
      '以太网 2@169.254.178.208': ['t-vpn']
    };
    state.ignored = (saved && saved.ignored) || [];
    state.settings = Object.assign({}, DEFAULT_SETTINGS, (saved && saved.settings) || {});
    }

  /* ---------- 端口与调度 ---------- */
  function buildPorts() {
    state.ports = clone(PORT_DEFS).map(function (p) {
      p.last_seen = p.auth_status === 'online' ? nowIso() : null;
      return p;
    });
    pickSticky();
  }

  function pickSticky() {
    var online = state.ports.filter(function (p) {
      return p.schedulable && p.auth_status === 'online';
    });
    if (!online.length) {
      state.scheduler.sticky_id = null;
      state.scheduler.sticky_ip = null;
      state.scheduler.candidates = 0;
      return;
    }
    var current = state.scheduler.sticky_id;
    var keep = online.filter(function (p) { return p.id === current; })[0];
    var chosen = keep || online[0];
    state.scheduler.sticky_id = chosen.id;
    state.scheduler.sticky_ip = chosen.source_ip;
    state.scheduler.candidates = online.length;
  }

  function mainPort() {
    if (!state.scheduler.sticky_id) return null;
    return state.ports.filter(function (p) { return p.id === state.scheduler.sticky_id; })[0] || null;
  }

  function visiblePorts() {
    return state.ports.filter(function (p) { return state.ignored.indexOf(p.id) === -1; });
  }

  /* ---------- 速率历史（60 点） ---------- */
  function seedHistory() {
    state.history = [];
    var v = 8.2e6;
    for (var i = 0; i < 60; i += 1) {
      v = clamp(v * rand(0.94, 1.12) + rand(-2e5, 5e5), 3e6, 1.5e7);
      state.history.push({ t: Date.now() - (60 - i) * 1000, v: v });
    }
  }

  function pushHistory(v) {
    state.history.push({ t: Date.now(), v: v });
    if (state.history.length > 60) state.history.shift();
  }

  /* ---------- 每秒推进 ---------- */
  function tick() {
    state.ports.forEach(function (p) {
      if (p.auth_status !== 'online') return;
      p.rate_down_bps = clamp(p.rate_down_bps * rand(0.9, 1.1), 6e6, 1.9e7);
      p.rate_up_bps = clamp(p.rate_up_bps * rand(0.88, 1.12), 5e5, 2.4e6);
      p.rtt_ms = Math.round(clamp(p.rtt_ms + rand(-3, 3), 14, 46));
      p.loss_rate = clamp(p.loss_rate + rand(-0.0008, 0.0009), 0.0002, 0.008);
      p.health_score = clamp(1 - (p.rtt_ms - 12) / 120 - p.loss_rate * 4, 0.6, 0.99);
      p.bytes_down += Math.round(p.rate_down_bps / 4);
      p.bytes_up += Math.round(p.rate_up_bps / 4);
      p.active_conns = Math.round(clamp(p.active_conns + rand(-2, 2), 12, 60));
      p.last_seen = nowIso();
    });
    var m = mainPort();
    if (m) pushHistory(m.rate_down_bps);
    notify();
  }

  function notify() {
    listeners.forEach(function (fn) { try { fn(); } catch (e) { /* 渲染异常不应中断数据流 */ } });
  }

  /* ---------- 对外 API ---------- */
  var api = {
    PALETTE: PALETTE,

    init: function () {
      restore();
      buildPorts();
      seedHistory();
      applySettingsSideEffects();
    },

    onChange: function (fn) { listeners.push(fn); return function () { listeners = listeners.filter(function (x) { return x !== fn; }); }; },

    /* 扫描：800ms 模拟系统枚举网卡 */
    scanPorts: function () {
      state.scanning = true;
      notify();
      return new Promise(function (resolve) {
        setTimeout(function () {
          buildPorts();
          state.scanning = false;
          state.lastScanAt = Date.now();
          notify();
          resolve(clone(state.ports));
        }, 800);
      });
    },

    isScanning: function () { return state.scanning; },
    lastScanAt: function () { return state.lastScanAt; },

    snapshot: function () {
      return {
        ips: clone(state.ports),
        scheduler: clone(state.scheduler),
        totals: api.totals()
      };
    },

    ports: function () { return clone(state.ports); },
    visiblePorts: function () { return clone(visiblePorts()); },
    ignoredIds: function () { return state.ignored.slice(); },
    mainPortId: function () { return state.scheduler.sticky_id; },
    scheduler: function () { return clone(state.scheduler); },

    totals: function () {
      var online = 0, bad = 0;
      state.ports.forEach(function (p) {
        if (p.auth_status === 'online') online += 1;
        if (p.auth_status === 'reauthenticating' || (p.errors && p.errors.length)) bad += 1;
      });
      var m = mainPort();
      return {
        count: state.ports.length,
        online: online,
        bad: bad,
        idle: state.ports.length - online - bad,
        rate_down_bps: m ? m.rate_down_bps : 0,
        rate_up_bps: m ? m.rate_up_bps : 0,
        rtt_ms: m ? m.rtt_ms : null,
        loss_rate: m ? m.loss_rate : null,
        health_score: m ? m.health_score : null
      };
    },

    history: function () { return state.history.slice(); },

    /* ---------- 认证 ---------- */
    /* 登录：直接调用守护进程的真实认证接口。
     *
     * 之前这里是纯 mock（只校验账号是不是 6 位数字），
     * 用户填了真密码也不会真的去认证——这正是"面板显示正常但上不了网"的来源。
     *
     * 现在：POST /api/v1/auth/login → 守护进程先用 Dr.COM 客户端实际登录一次，
     * 通过后启动认证子进程，并按 remember 决定是否保存到本机。 */
    login: function (username, password, remember) {
      return controlFetch('/api/v1/auth/login', {
        method: 'POST',
        body: JSON.stringify({
          username: username,
          password: password,
          remember: !!remember
        })
      }).then(function (st) {
        authRuntime = st || {};
        notify();
        return st;
      });
    },
    /* 拉一次认证运行时状态：页面打开时据此决定"清除凭据"是否可用、
       以及账号栏要不要回填。 */
    refreshAuth: function () {
      return controlFetch('/api/v1/auth').then(function (st) {
        authRuntime = st || {};
        notify();
        return authRuntime;
      }).catch(function () {
        authRuntime = {};
        return authRuntime;
      });
    },    authStatus: function () { return authRuntime; },
    /* 按住系统代理：让只认系统代理的程序（迅雷等）也走多 IP */
    startProxyHold: function (seconds) {
      return controlFetch('/api/v1/proxyhold/start', {
        method: 'POST',
        body: JSON.stringify({ seconds: seconds || 600 })
      });
    },
    stopProxyHold: function () {
      return controlFetch('/api/v1/proxyhold/stop', { method: 'POST' });
    },
    proxyHoldStatus: function () {
      return controlFetch('/api/v1/proxyhold');
    },

    /* 重新扫描：让守护进程按网卡现状校正源列表（DHCP 换地址后不用重启） */
    rescanSources: function () {
      return controlFetch('/api/v1/sources/rescan', { method: 'POST' });
    },
    hasCredential: function () { return !!authRuntime.remember; },
    credentialUsername: function () { return authRuntime.account || ''; },
    /* 兼容旧调用：登录时已经带 remember，这里不再单独保存 */
    saveCredential: function () {},
    /* 清除凭据 = 退出认证：停止子进程、删除本机文件、端口标记离线 */
    clearCredential: function () {
      return controlFetch('/api/v1/auth/logout', { method: 'POST' }).then(function (st) {
        authRuntime = st || {};
        notify();
      }).catch(function () {
        authRuntime = {};
        notify();
      });
    },

    /* ---------- 标签 ---------- */
    tags: function () { return clone(state.tags); },
    tagsOf: function (portId) {
      var ids = state.assign[portId] || [];
      return ids.map(function (id) {
        return state.tags.filter(function (t) { return t.id === id; })[0];
      }).filter(Boolean);
    },
    createTag: function (name, color) {
      name = (name || '').trim();
      if (!name) return { ok: false, message: '标签名不能为空' };
      if (name.length > 8) return { ok: false, message: '标签名最多 8 个字' };
      if (state.tags.some(function (t) { return t.name === name; })) return { ok: false, message: '已有同名标签' };
      var tag = { id: nextId('t'), name: name, color: color || 0 };
      state.tags.push(tag);
      persist();
      return { ok: true, tag: tag };
    },
    renameTag: function (id, name) {
      name = (name || '').trim();
      if (!name) return { ok: false, message: '标签名不能为空' };
      if (name.length > 8) return { ok: false, message: '标签名最多 8 个字' };
      if (state.tags.some(function (t) { return t.name === name && t.id !== id; })) return { ok: false, message: '已有同名标签' };
      state.tags.forEach(function (t) { if (t.id === id) t.name = name; });
      persist();
      return { ok: true };
    },
    setTagColor: function (id, color) {
      state.tags.forEach(function (t) { if (t.id === id) t.color = color; });
      persist();
    },
    deleteTag: function (id) {
      state.tags = state.tags.filter(function (t) { return t.id !== id; });
      Object.keys(state.assign).forEach(function (pid) {
        state.assign[pid] = state.assign[pid].filter(function (tid) { return tid !== id; });
      });
      persist();
      notify();
    },
    assignTag: function (portIds, tagId) {
      portIds.forEach(function (pid) {
        var list = state.assign[pid] || [];
        if (list.indexOf(tagId) === -1) list.push(tagId);
        state.assign[pid] = list;
      });
      persist();
      notify();
    },
    unassignTag: function (portId, tagId) {
      state.assign[portId] = (state.assign[portId] || []).filter(function (id) { return id !== tagId; });
      persist();
      notify();
    },
    tagUsage: function (tagId) {
      return Object.keys(state.assign).filter(function (pid) {
        return state.assign[pid].indexOf(tagId) !== -1 && state.ignored.indexOf(pid) === -1;
      }).length;
    },
    portIdsWithTag: function (tagId) {
      return Object.keys(state.assign).filter(function (pid) {
        return state.assign[pid].indexOf(tagId) !== -1 && state.ignored.indexOf(pid) === -1;
      });
    },

    /* ---------- 忽略 ---------- */
    ignorePorts: function (ids) {
      ids.forEach(function (id) {
        if (state.ignored.indexOf(id) === -1) state.ignored.push(id);
      });
      persist();
      notify();
    },
    restorePorts: function (ids) {
      state.ignored = state.ignored.filter(function (id) { return ids.indexOf(id) === -1; });
      persist();
      notify();
    },

    /* ---------- 偏好 ---------- */
    settings: function () { return clone(state.settings); },
    updateSettings: function (patch) {
      state.settings = Object.assign({}, state.settings, patch);
      persist();
      applySettingsSideEffects();
      notify();
    },
    resetSettings: function () {
      state.settings = Object.assign({}, DEFAULT_SETTINGS);
      persist();
      applySettingsSideEffects();
      notify();
    },

    tick: tick,
    notify: notify
  };

  function applySettingsSideEffects() {
    document.documentElement.setAttribute('data-theme', state.settings.theme || 'dark');
    document.body.setAttribute('data-theme', state.settings.theme || 'dark');
  }

  window.MockAPI = api;
})();
