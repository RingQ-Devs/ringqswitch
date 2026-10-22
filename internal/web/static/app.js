'use strict';
// RingQswitch web interface. Plain JavaScript, no build step. Every value
// from the server is inserted as text (never as HTML).

// ---------------------------------------------------------------------------
// Helpers

function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v === null || v === undefined || v === false) continue;
      if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
      else if (k === 'class') el.className = v;
      else if (k === 'value') el.value = v;
      else if (k === 'checked') el.checked = !!v;
      else if (k === 'selected') el.selected = !!v;
      else if (k === 'title' || k === 'placeholder' || k === 'aria-label') el.setAttribute(k, I18N.tr(String(v)));
      else el.setAttribute(k, v === true ? '' : v);
    }
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid === null || kid === undefined || kid === false) continue;
    el.append(kid instanceof Node ? kid : I18N.tr(String(kid))); // UI words in the chosen language
  }
  return el;
}
const $ = (sel, root) => (root || document).querySelector(sel);
function fill(el, ...kids) {
  el.replaceChildren(...kids.flat(Infinity).filter((k) => k !== null && k !== undefined && k !== false));
  return el;
}
const $$ = (sel, root) => Array.from((root || document).querySelectorAll(sel));

async function api(method, path, body, raw) {
  const opt = { method, headers: {}, credentials: 'same-origin' };
  if (method !== 'GET') opt.headers['X-RQ'] = '1';
  if (body !== undefined) {
    if (raw) { opt.body = body; opt.headers['Content-Type'] = 'application/octet-stream'; }
    else { opt.body = JSON.stringify(body); opt.headers['Content-Type'] = 'application/json'; }
  }
  const r = await fetch('/api/' + path, opt);
  if (r.status === 401 && path !== 'login') { showLogin(); throw new Error('Please sign in again'); }
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data.error || r.statusText);
  return data;
}
const dpath = (p) => 'd/' + encodeURIComponent(S.domain) + '/' + p;

let toastTimer;
function toast(msg, err) {
  const el = $('#toast');
  el.textContent = I18N.tr(msg);
  el.className = 'toast' + (err ? ' err' : '');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.add('hidden'), err ? 6000 : 2500);
}
function fail(e) { toast(e.message || String(e), true); }

// Notes: the notifications at the top right (chat messages, calls, missed
// calls, voicemail, people asking to join a meeting). Each has an icon, a
// title, a line of text, buttons, and closes by itself after a few seconds
// (7 s; not while the mouse is over it) unless it waits for an answer (a
// ringing call, someone knocking at a meeting). With the tab in the
// background the system shows one too.
const Notes = (() => {
  const live = new Map(); // key -> {el, timer}
  let box = null;
  function stack() {
    if (!box || !box.isConnected) { box = h('div', { class: 'notes', 'aria-live': 'polite' }); document.body.append(box); }
    return box;
  }
  function close(key) {
    const n = live.get(key);
    if (!n) return;
    live.delete(key);
    clearTimeout(n.timer);
    n.el.classList.add('leaving');
    setTimeout(() => n.el.remove(), 180);
  }
  // show({key, icon, tone: green|red|blue|amber|violet, title, body, actions: [{label, kind, fn}], click, sticky, ms, os})
  function show(o) {
    const key = o.key || String(Math.random());
    if (live.has(key)) { const old = live.get(key); clearTimeout(old.timer); old.el.remove(); live.delete(key); }
    const el = h('div', { class: 'note ' + (o.tone || 'blue'), role: o.sticky ? 'alertdialog' : 'status' },
      h('span', { class: 'note-ic' }, icon(o.icon || 'bell', 18)),
      h('div', { class: 'note-main' },
        h('div', { class: 'note-head' }, h('b', null, o.title || ''), h('span', { class: 'note-time' }, new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }))),
        o.body ? h('div', { class: 'note-body' }, o.body) : null,
        o.actions && o.actions.length ? h('div', { class: 'note-actions' }, o.actions.map((a) => h('button', { type: 'button', class: 'btn small ' + (a.kind || ''),
          onclick: (e) => { e.stopPropagation(); close(key); a.fn(); } }, a.label))) : null),
      h('button', { type: 'button', class: 'note-x', title: 'Close', 'aria-label': 'Close', onclick: (e) => { e.stopPropagation(); close(key); } }, icon('x', 14)));
    if (o.click) { el.classList.add('click'); el.addEventListener('click', () => { close(key); o.click(); }); }
    const n = { el, timer: 0 };
    const arm = () => { if (!o.sticky) n.timer = setTimeout(() => close(key), o.ms || 7000); };
    el.addEventListener('mouseenter', () => clearTimeout(n.timer));
    el.addEventListener('mouseleave', arm);
    arm();
    live.set(key, n);
    stack().prepend(el);
    while (live.size > 5) close(live.keys().next().value);
    if (o.os !== false && document.hidden && window.Notification && Notification.permission === 'granted') {
      try {
        const sys = new Notification(o.title || '', { body: o.body || '', tag: 'rq-' + key });
        sys.onclick = () => { window.focus(); sys.close(); if (o.click) { close(key); o.click(); } };
      } catch (e) { /* not supported */ }
    }
    return key;
  }
  return { show, close };
})();

function truthy(v) { return ['true', 't', '1', 'yes', 'on'].includes(String(v).toLowerCase()); }
function pad(n) { return String(n).padStart(2, '0'); }
function fmtDur(s) {
  s = Math.max(0, Math.round(Number(s) || 0));
  const hh = Math.floor(s / 3600), mm = Math.floor((s % 3600) / 60), ss = s % 60;
  return hh ? `${hh}:${pad(mm)}:${pad(ss)}` : `${mm}:${pad(ss)}`;
}
function toDate(v) {
  if (v === null || v === undefined || v === '' || v === '0') return null;
  // Epoch seconds or milliseconds, also as 1.79e+09 or with decimals.
  const n = Number(v);
  const d = String(v).trim() !== '' && Number.isFinite(n) ? new Date(n * (n > 1e11 ? 1 : 1000)) : new Date(v);
  return isNaN(d) ? null : d;
}
function fmtTime(v) {
  const d = toDate(v);
  return d ? d.toLocaleString() : (v ? String(v) : '');
}
// party names someone: "Jay Bayron (1000)"; a name that already holds the
// number ("Ext 1000") is shown alone.
function party(name, number) {
  name = (name || '').trim(); number = (number || '').trim();
  if (!name || name === number) return number;
  if (!number || name.includes(number)) return name;
  return name + ' (' + number + ')';
}
// fmtShort is a compact date and time for wide tables: "14 Oct, 03:45 PM"
// (the year when it is not this one).
function fmtShort(v) {
  const d = toDate(v);
  if (!d) return v ? String(v) : '';
  const o = { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit' };
  if (d.getFullYear() !== new Date().getFullYear()) o.year = 'numeric';
  return d.toLocaleString([], o);
}
function today() { const d = new Date(); return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`; }
function daysAgo(n) { const d = new Date(Date.now() - n * 86400000); return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`; }
function randomPassword(n) {
  const chars = 'abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789';
  const a = new Uint32Array(n); crypto.getRandomValues(a);
  return Array.from(a, (x) => chars[x % chars.length]).join('');
}
// eyeToggle wraps a password input with a show/hide button (all password
// fields are hidden until the eye is clicked).
function eyeToggle(inp) {
  if (inp.parentNode && inp.parentNode.classList && inp.parentNode.classList.contains('pw-wrap')) return inp.parentNode;
  inp.dataset.eye = '1';
  const btn = h('button', { type: 'button', class: 'pw-eye', tabindex: '-1' });
  const wrap = h('span', { class: 'pw-wrap' });
  const set = (show) => {
    inp.type = show ? 'text' : 'password';
    fill(btn, icon(show ? 'eyeoff' : 'eye', 16));
    btn.title = show ? 'Hide' : 'Show';
    btn.setAttribute('aria-label', btn.title);
  };
  btn.addEventListener('mousedown', (e) => e.preventDefault()); // keep the focus in the input
  btn.addEventListener('click', () => set(inp.type === 'password'));
  if (inp.parentNode) inp.parentNode.insertBefore(wrap, inp);
  wrap.append(inp, btn);
  wrap.reveal = set;
  set(false);
  return wrap;
}
// Every password field the pages draw gets the eye.
new MutationObserver(() => {
  $$('input[type=password]:not([data-eye])').forEach((inp) => { inp.dataset.eye = '1'; eyeToggle(inp); });
}).observe(document.documentElement, { childList: true, subtree: true });

// secretText shows a stored password as dots with an eye to reveal it.
function secretText(v) {
  if (!v) return h('span', { class: 'muted' }, '—');
  const txt = h('span', { class: 'mono' }, '••••••••');
  const btn = h('button', { type: 'button', class: 'pw-eye inline', title: 'Show' }, icon('eye', 15));
  btn.addEventListener('click', () => {
    const show = txt.textContent !== v;
    txt.textContent = show ? v : '••••••••';
    fill(btn, icon(show ? 'eyeoff' : 'eye', 15));
    btn.title = show ? 'Hide' : 'Show';
  });
  return h('span', { class: 'secret' }, txt, btn);
}

function downloadCSV(name, header, rows) {
  const q = (v) => { v = v === null || v === undefined ? '' : String(v); return /[",\n]/.test(v) ? '"' + v.replace(/"/g, '""') + '"' : v; };
  const text = [header.map(q).join(',')].concat(rows.map((r) => r.map(q).join(','))).join('\r\n');
  const a = h('a', { href: URL.createObjectURL(new Blob([text], { type: 'text/csv' })), download: name });
  document.body.append(a); a.click(); a.remove();
}

function table(cols, rows, opts) {
  opts = opts || {};
  if (!rows.length) return h('div', { class: 'table-wrap' }, h('div', { class: 'empty' }, opts.empty || 'Nothing here yet.'));
  return h('div', { class: 'table-wrap' }, h('table', null,
    h('thead', null, h('tr', null, cols.map((c) => h('th', { class: c.num ? 'num-cell' : null }, c.label)))),
    h('tbody', null, rows.map((r) => h('tr', { class: opts.click ? 'click' : null, onclick: opts.click ? (ev) => opts.click(r, ev.currentTarget, ev) : null },
      cols.map((c) => h('td', { class: (c.num ? 'num-cell ' : '') + (c.wrap ? 'wrap' : '') + (c.actions ? ' actions' : '') },
        c.render ? c.render(r) : (r[c.key] === undefined ? '' : r[c.key]))))))));
}
const badge = (text, color) => h('span', { class: 'badge ' + (color || '') }, text);
const yesNo = (v) => truthy(v) ? badge('Yes', 'green') : badge('No');

function statusBadge(status, aux) {
  const s = status || '';
  if (s.startsWith('Available')) return badge(s, 'green');
  if (s === 'On Break') return badge('Break' + (aux ? ': ' + aux : ''), 'amber');
  if (s === 'Logged Out') return badge('Logged out');
  return badge(s);
}

// ---------------------------------------------------------------------------
// State, login, navigation

const S = { me: null, domains: [], domain: '', meta: null, timers: [], clockSkew: 0, unread: 0 };

function clearTimers() {
  S.timers.forEach(clearInterval);
  S.timers = [];
  liveOff();
}
function every(ms, fn) { S.timers.push(setInterval(fn, ms)); }

// ---------------------------------------------------------------------------
// Realtime: one WebSocket per tab (live data, chat, dialer, presence)

const WS = { sock: null, handlers: {}, queue: [], ref: 0, pending: {}, retry: 1000, liveFn: null, open: false };

function on(type, fn) { (WS.handlers[type] = WS.handlers[type] || []).push(fn); }
function off(type, fn) { WS.handlers[type] = (WS.handlers[type] || []).filter((f) => f !== fn); }

function wsConnect() {
  if (WS.sock) return;
  const sock = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/api/ws');
  WS.sock = sock;
  sock.onopen = () => {
    WS.open = true;
    WS.retry = 1000;
    const q = WS.queue;
    WS.queue = [];
    q.forEach((m) => sock.send(JSON.stringify(m)));
    if (WS.liveFn) sock.send(JSON.stringify({ t: 'live', on: true, domain: S.domain }));
    (WS.handlers['ws.open'] || []).forEach((f) => f());
  };
  sock.onmessage = (ev) => {
    let m;
    try { m = JSON.parse(ev.data); } catch (e) { return; }
    if (m.ref && WS.pending[m.ref]) {
      const p = WS.pending[m.ref];
      delete WS.pending[m.ref];
      if (m.t === 'error') p.reject(new Error(m.error)); else p.resolve(m);
    } else if (m.t === 'error') {
      fail(new Error(m.error));
    }
    (WS.handlers[m.t] || []).forEach((f) => { try { f(m); } catch (e) { console.error(e); } });
  };
  sock.onclose = () => {
    WS.sock = null;
    WS.open = false;
    (WS.handlers['ws.close'] || []).forEach((f) => f());
    if (!S.me) return;
    setTimeout(wsConnect, WS.retry);
    WS.retry = Math.min(WS.retry * 2, 15000);
  };
}

function wsSend(msg, wait) {
  if (wait) {
    msg.ref = String(++WS.ref);
    const p = new Promise((resolve, reject) => {
      WS.pending[msg.ref] = { resolve, reject };
      setTimeout(() => { if (WS.pending[msg.ref]) { delete WS.pending[msg.ref]; reject(new Error('no answer from the server')); } }, 15000);
    });
    if (WS.open) WS.sock.send(JSON.stringify(msg)); else WS.queue.push(msg);
    return p;
  }
  if (WS.open) WS.sock.send(JSON.stringify(msg)); else WS.queue.push(msg);
  return null;
}

// liveOn calls fn with live data now and on every change (until the page changes).
function liveOn(fn) {
  liveOff();
  WS.liveFn = (m) => { S.clockSkew = Date.now() / 1000 - m.data.time; fn(m.data); };
  on('live', WS.liveFn);
  wsSend({ t: 'live', on: true, domain: S.domain });
}
function liveOff() {
  if (!WS.liveFn) return;
  off('live', WS.liveFn);
  WS.liveFn = null;
  wsSend({ t: 'live', on: false });
}

function showLogin() {
  clearTimers();
  S.me = null;
  if (WS.sock) WS.sock.close();
  if (window.Phone) Phone.stop();
  if (window.LiveTr) LiveTr.reset();
  $('#app').classList.add('hidden');
  $('#login').classList.remove('hidden');
  $('#login-user').focus();
  if (window.SSOLogin) {
    const err = SSOLogin.takeError();
    if (err) $('#login-error').textContent = err;
    SSOLogin.render();
  }
}

$('#login-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  $('#login-error').textContent = '';
  try {
    await api('POST', 'login', { user: $('#login-user').value, password: $('#login-pass').value });
    $('#login-pass').value = '';
    boot();
  } catch (e) { $('#login-error').textContent = e.message; }
});
const signOut = async () => { window.dispatchEvent(new Event('rq-logout')); await api('POST', 'logout').catch(() => {}); showLogin(); };
$('#logout-btn').addEventListener('click', signOut);
$('#menu-btn').addEventListener('click', () => $('#app').classList.toggle('nav-open'));
$('#domain-select').addEventListener('change', async (ev) => {
  S.domain = ev.target.value;
  try { localStorage.setItem('rq-domain', S.domain); } catch (e) { /* private mode */ }
  await loadMeta();
  route();
});

async function loadMeta() {
  S.meta = null;
  if (!S.me.database || !S.domain) return;
  try { S.meta = await api('GET', dpath('meta')); } catch (e) { fail(e); }
}

async function loadDomains() {
  if (!S.me.database) { S.domains = []; return; }
  S.domains = await api('GET', 'domains');
  const sel = $('#domain-select');
  sel.replaceChildren(...S.domains.map((d) => h('option', { value: d.domain_name }, d.domain_name)));
  let saved = '';
  try { saved = localStorage.getItem('rq-domain') || ''; } catch (e) { /* private mode */ }
  if (S.me.domain) S.domain = S.me.domain;
  if (!S.domains.some((d) => d.domain_name === S.domain)) {
    S.domain = S.domains.some((d) => d.domain_name === saved) ? saved : (S.domains[0] ? S.domains[0].domain_name : '');
  }
  sel.value = S.domain;
  // Only the administrator works across domains.
  sel.parentElement.classList.toggle('hidden', !S.domains.length || S.me.role !== 'admin');
}

const isOwner = () => S.me && (S.me.role === 'admin' || S.me.role === 'owner');
// Supervisors (owners and managers) monitor calls, set any agent's status and see all reports.
const isSupervisor = () => isOwner() || (S.me && S.me.role === 'manager');
const isExt = () => S.me && S.me.kind === 'extension';

// drawWho shows the signed-in user's name and role (again in another language).
function drawWho() {
  const display = isExt() ? (S.me.name && S.me.name !== S.me.extension ? S.me.name : t('Ext') + ' ' + S.me.extension) : t('Administrator');
  $('#user-name').textContent = display;
  $('#top-name').textContent = display;
  $('#user-role').textContent = t({ admin: 'Administrator', owner: 'Owner', manager: 'Manager', user: 'User' }[S.me.role] || '') + (isExt() ? ' · ' + S.me.extension : '');
  const ini = isExt() ? initials(S.me.name && S.me.name !== S.me.extension ? S.me.name : S.me.extension) : 'AD';
  $('#side-avatar').textContent = ini;
  $('#top-avatar').replaceChildren(ini, $('#top-presence'));
  $('#top-presence').className = 'presence';
}

async function boot() {
  try { S.me = await api('GET', 'me'); } catch (e) { showLogin(); return; }
  document.title = S.me.brand;
  $$('.brand-name').forEach((el) => { el.textContent = S.me.brand; });
  drawWho();
  $('#login').classList.add('hidden');
  $('#app').classList.remove('hidden');
  try { await loadDomains(); } catch (e) { fail(e); }
  await loadMeta();
  renderNav();
  wsConnect();
  $('#phone-btn').classList.toggle('hidden', !isExt() || !S.me.webrtc);
  $('#chat-btn').classList.toggle('hidden', !isExt() || !S.me.database);
  $('#meet-btn').classList.toggle('hidden', !isExt() || !S.me.database || !S.me.webrtc);
  if (window.Topbar) Topbar.update();
  if (isExt()) {
    if (window.Chat) Chat.init();
    if (window.Phone && S.me.webrtc) Phone.init();
    if (window.PhoneSide && S.me.webrtc) PhoneSide.init(); // BLF and parked calls beside the dialer
    if (window.LiveTr) LiveTr.init();
  }
  if (window.IdleLogout) IdleLogout.start(); // web auto logout (Security settings)
  route();
}

// Navigation. Top-level entries are pages or groups of pages:
// { id, label, icon, who } or { label, icon, kids: [[page, label, icon, who]...] }.
// who: all, owner, server (Security, System...), ext (needs an extension).
const NAV = [
  { id: 'dashboard', label: 'Dashboard', icon: 'dashboard', who: 'all' },
  { id: 'directory', label: 'Team directory', icon: 'team', who: 'all' },
  { label: 'Accounts', icon: 'idcard', kids: [
    ['extensions', 'Extensions', 'user', 'owner'], ['roles', 'User roles', 'roles', 'owner'], ['ringgroups', 'Ring groups', 'users', 'owner'], ['ivrs', 'IVR menus', 'ivr', 'owner'],
    ['timeconditions', 'Time conditions', 'clock', 'owner'], ['conferences', 'Conference rooms', 'team', 'owner']] },
  { label: 'Contact center', icon: 'headset', kids: [
    ['contacts', 'Contacts', 'contacts', 'all'], ['callstatus', 'Call status', 'tower', 'sup'], ['callcentre', 'Call centre (live)', 'activity', 'all'], ['queues', 'Queues', 'list', 'owner'],
    ['auxcodes', 'AUX / break codes', 'coffee', 'owner']] },
  { id: 'wallboard', label: 'Wallboard', icon: 'monitor', who: 'all' },
  { label: 'Communicate', icon: 'chat', kids: [
    ['chat', 'Chat', 'chat', 'ext'], ['meetings', 'Meetings', 'video', 'ext'], ['voicemail', 'Voicemail', 'voicemail', 'all']] },
  { id: 'callflow', label: 'Call Flow', icon: 'route', who: 'owner' },
  { id: 'aiagents', label: 'AI agents', icon: 'bot', who: 'owner' },
  { id: 'hotel', label: 'Hotel front desk', icon: 'building', who: 'sup' },
  { id: 'efax', label: 'eFax', icon: 'fax', who: 'sup' },
  { label: 'Reports', icon: 'chart', kids: [
    ['cdr', 'Call history', 'history', 'all'], ['rep-agent-status', 'Agent status times', 'clock', 'all'],
    ['rep-agent-log', 'Agent status log', 'log', 'all'], ['rep-queues', 'Queue report', 'chart', 'all'],
    ['rep-agents', 'Agent performance', 'users', 'all'], ['rep-queue-calls', 'Queue calls', 'list', 'all']] },
];
const NAV_BOTTOM = [
  { label: 'Settings', icon: 'settings', kids: [
    ['global', 'Global', 'globe', 'owner'], ['officehours', 'Language & office hours', 'clock', 'owner'], ['routes', 'Outbound rules', 'outgoing', 'owner'], ['integrations', 'Integrations', 'plug', 'owner'], ['sounds', 'Sounds & music', 'music', 'owner'], ['security', 'Security', 'shield', 'owner'], ['domains', 'Domains', 'globe', 'server'],
    ['transcription', 'Call transcription', 'captions', 'owner'], ['ai-settings', 'AI agent service', 'bot', 'owner'], ['phonekeys', 'Phone keys', 'keypad', 'owner'],
    ['dialplans', 'Dialplans (advanced)', 'code', 'owner'], ['system', 'System', 'cpu', 'server'], ['logs', 'Event log', 'log', 'server'],
    ['profile', 'My account', 'user', 'all']] },
];
const DB_ONLY = new Set(['extensions', 'ringgroups', 'ivrs', 'timeconditions', 'officehours', 'conferences', 'voicemail', 'queues', 'agents', 'auxcodes',
  'gateways', 'dids', 'callflow', 'hotel', 'efax', 'faxes', 'integrations', 'routes', 'cdr', 'rep-agent-status', 'rep-agent-log', 'rep-queues', 'rep-agents', 'rep-queue-calls', 'domains',
  'dialplans', 'sounds', 'chat', 'meetings', 'security', 'directory', 'roles', 'contacts', 'transcription', 'aiagents', 'ai-settings', 'phonekeys']);

function allowed(who) {
  switch (who) {
    case 'owner': return isOwner();
    case 'sup': return isSupervisor();
    case 'server': return S.me.server_access;
    case 'ext': return isExt();
  }
  return true;
}

// navPages lists every page of the menu as [id, label, icon, who, group].
function navPages() {
  const out = [];
  for (const e of NAV.concat(NAV_BOTTOM)) {
    if (e.kids) e.kids.forEach((k) => out.push([k[0], k[1], k[2], k[3], e.label]));
    else out.push([e.id, e.label, e.icon, e.who, '']);
  }
  return out;
}

function pageAllowed(page) {
  const p = navPages().find((x) => x[0] === page);
  return p ? allowed(p[3]) : isOwner(); // pages outside the menu (agents...)
}

const showPage = (id, who) => (S.me.database || !DB_ONLY.has(id)) && allowed(who);
const currentPage = () => location.hash.replace(/^#\/?/, '').split('/')[0] || 'dashboard';

function navLink(id, label, ic, cls) {
  return h('a', { class: 'nav-item ' + (cls || ''), href: '#/' + id, 'data-page': id, title: label }, icon(ic, cls ? 15 : 18),
    h('span', { class: 'nav-label' }, label),
    id === 'chat' ? h('span', { class: 'nav-badge' + (S.unread ? '' : ' hidden'), id: 'nav-chat-badge' }, String(S.unread || '')) : null);
}

function renderNavList(el, entries) {
  const page = currentPage();
  el.replaceChildren();
  for (const e of entries) {
    if (!e.kids) {
      if (showPage(e.id, e.who)) el.append(navLink(e.id, e.label, e.icon));
      continue;
    }
    const kids = e.kids.filter(([id, , , who]) => showPage(id, who));
    if (!kids.length) continue;
    const open = S.navGroup === e.label;
    const group = h('div', { class: 'nav-group' + (open ? ' open' : ''), 'data-group': e.label });
    group.append(h('button', { class: 'nav-item', type: 'button', title: e.label, 'aria-expanded': String(open), onclick: () => {
      if ($('#app').classList.contains('collapsed')) { location.hash = '#/' + kids[0][0]; return; }
      // One menu open at a time: opening this one closes the others.
      openNavGroup(group.classList.contains('open') ? '' : e.label);
    } }, icon(e.icon, 20), h('span', { class: 'nav-label' }, e.label), icon('down', 16, 'chev')),
    h('div', { class: 'nav-kids' }, kids.map(([id, label, ic]) => navLink(id, label, ic, 'nav-sub'))));
    el.append(group);
  }
  markNav();
}

function renderNav() {
  const q = $('#nav-search').value.trim().toLowerCase();
  if (q) {
    // Search: a flat list of the matching pages.
    const hits = navPages().filter(([id, label, , who, group]) => showPage(id, who) && (label + ' ' + group).toLowerCase().includes(q));
    fill($('#nav-main'), hits.length ? hits.map(([id, label, ic]) => navLink(id, label, ic)) : h('div', { class: 'nav-empty' }, 'Nothing found.'));
    $('#nav-bottom').classList.add('hidden');
    markNav();
    return;
  }
  $('#nav-bottom').classList.remove('hidden');
  renderNavList($('#nav-main'), NAV);
  renderNavList($('#nav-bottom'), NAV_BOTTOM);
}

function openNavGroup(label) {
  S.navGroup = label;
  $$('#nav .nav-group').forEach((g) => {
    const on = g.dataset.group === label;
    g.classList.toggle('open', on);
    g.querySelector('button.nav-item').setAttribute('aria-expanded', String(on));
  });
  try { localStorage.setItem('rq-nav-group', label); } catch (err) { /* private mode */ }
}

function markNav() {
  const page = currentPage();
  $$('#nav a.nav-item').forEach((a) => a.classList.toggle('active', a.dataset.page === page));
  // Going to a page opens its menu (and closes the one open before).
  const g = $$('#nav .nav-group').find((x) => x.querySelector('a.active'));
  if (g && S.navPage !== page) { S.navPage = page; openNavGroup(g.dataset.group); }
}

// initials: "Thilak Manager" → TM, "Alice" → A, "Ext 1003" and "1003" → 03.
function initials(name) {
  const words = String(name || '').trim().split(/\s+/).filter(Boolean);
  if (!words.length) return '?';
  const last = words[words.length - 1];
  if (/^\+?\d+$/.test(last)) return last.slice(-2);
  if (words.length === 1) return words[0][0].toUpperCase();
  return (words[0][0] + last[0]).toUpperCase();
}

function setTheme(theme) {
  if (theme) document.documentElement.dataset.theme = theme; else delete document.documentElement.dataset.theme;
  const dark = theme ? theme === 'dark' : window.matchMedia('(prefers-color-scheme: dark)').matches;
  fill($('#theme-btn'), icon(dark ? 'sun' : 'moon', 20));
  $('#theme-btn').title = t(dark ? 'Light mode' : 'Dark mode');
  $('#theme-btn').setAttribute('aria-label', $('#theme-btn').title);
}

function setCollapsed(on) {
  $('#app').classList.toggle('collapsed', on);
  $('#collapse-btn').title = t(on ? 'Expand the menu' : 'Collapse the menu');
  try { localStorage.setItem('rq-nav-collapsed', on ? '1' : ''); } catch (e) { /* private mode */ }
}

// The shell's static icons and controls (set up once).
(function shell() {
  fill($('#collapse-btn'), icon('left', 14));
  fill($('#search-icon'), icon('search', 16));
  fill($('#logout-btn'), icon('logout', 18));
  fill($('#menu-btn'), icon('menu', 22));
  $('#meet-btn').prepend(icon('video', 20));
  $('#meet-btn').addEventListener('click', () => Meet.openWindow());
  $('#chat-btn').prepend(icon('chat', 20));
  $('#wall-btn').prepend(icon('monitor', 20));
  $('#phone-btn').prepend(icon('phone', 24));
  S.navGroup = '';
  try {
    S.navGroup = localStorage.getItem('rq-nav-group') || '';
    if (localStorage.getItem('rq-nav-collapsed')) $('#app').classList.add('collapsed');
    setTheme(localStorage.getItem('rq-theme') || '');
  } catch (e) { setTheme(''); }
  $('#theme-btn').addEventListener('click', () => {
    const dark = document.documentElement.dataset.theme ? document.documentElement.dataset.theme === 'dark'
      : window.matchMedia('(prefers-color-scheme: dark)').matches;
    const t = dark ? 'light' : 'dark';
    setTheme(t);
    try { localStorage.setItem('rq-theme', t); } catch (e) { /* private mode */ }
  });
  $('#collapse-btn').addEventListener('click', () => setCollapsed(!$('#app').classList.contains('collapsed')));
  $('#nav-search').addEventListener('input', renderNav);
  $('#nav-search').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      const a = $('#nav-main a.nav-item');
      if (a) { location.hash = a.getAttribute('href'); e.target.value = ''; renderNav(); e.target.blur(); }
    } else if (e.key === 'Escape') { e.target.value = ''; renderNav(); e.target.blur(); }
  });
  $('.side-search').addEventListener('click', () => { if ($('#app').classList.contains('collapsed')) setCollapsed(false); });
  document.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k' && S.me) {
      e.preventDefault();
      if ($('#app').classList.contains('collapsed')) setCollapsed(false);
      if (window.innerWidth <= 900) $('#app').classList.add('nav-open');
      $('#nav-search').focus();
    }
  });
  $('#nav-shade').addEventListener('click', () => $('#app').classList.remove('nav-open'));
})();

window.addEventListener('hashchange', route);

// Another language: the fixed texts change at once, the menu and the page
// are drawn again.
I18N.applyStatic();
window.addEventListener('rq-lang', () => {
  setTheme(document.documentElement.dataset.theme || '');
  setCollapsed($('#app').classList.contains('collapsed'));
  if (!S.me) return;
  drawWho();
  renderNav();
  if (window.Topbar) Topbar.update();
  route();
});

function route() {
  if (!S.me) return;
  clearTimers();
  $('#app').classList.remove('nav-open');
  document.body.classList.remove('tv');
  const parts = location.hash.replace(/^#\/?/, '').split('/').map(decodeURIComponent);
  const page = parts[0] || 'dashboard';
  markNav();
  $('#chat-btn').classList.toggle('on', page === 'chat');
  $('#wall-btn').classList.toggle('on', page === 'wallboard');
  if (window.Topbar) Topbar.mark(page);
  const main = $('#main');
  fill(main, h('div', { class: 'muted' }, 'Loading…'));
  main.scrollTop = 0;
  if (!pageAllowed(page)) {
    fill(main, h('div', { class: 'panel' }, h('h2', null, 'Not available'), h('p', null, 'Your account cannot open this page.')));
    return;
  }
  if (DB_ONLY.has(page) && page !== 'domains' && S.me.database && !S.domain) {
    fill(main, h('div', { class: 'panel' }, h('h2', null, 'No domain yet'),
      h('p', null, 'Create a domain first (Settings → Domains). A domain is your company\'s phone system; phones register to it.'),
      h('a', { class: 'btn primary', href: '#/domains' }, 'Go to Domains')));
    return;
  }
  const fn = PAGES[page] || (R[page] ? () => resourcePage(page, parts[1]) : null);
  if (!fn) { fill(main, h('div', { class: 'panel' }, 'Page not found.')); return; }
  Promise.resolve(fn(parts.slice(1))).catch((e) => {
    fill(main, h('div', { class: 'panel' }, h('h2', null, 'Something went wrong'), h('p', { class: 'error-text' }, e.message)));
  });
}

function pageHead(title, ...right) {
  return h('div', { class: 'page-head' }, h('h1', null, title), h('div', { class: 'spacer' }), right);
}

// ---------------------------------------------------------------------------
// Destinations and form field widgets

// destOptions: the choices of a "goes to" field. eFax (a number answering
// as a fax machine for an extension) is only for DID numbers.
function destOptions(value, extra, efax) {
  const groups = {};
  const list = (S.meta ? S.meta.destinations : []).filter((d) => efax || d.group !== 'eFax').concat(extra || []);
  for (const d of list) (groups[d.group] = groups[d.group] || []).push(d);
  const sel = h('select', null, h('option', { value: '' }, '— choose —'));
  let found = !value;
  for (const [g, items] of Object.entries(groups)) {
    sel.append(h('optgroup', { label: g }, items.map((d) => {
      if (d.value === value) found = true;
      return h('option', { value: d.value, selected: d.value === value }, d.label);
    })));
  }
  if (!found) sel.append(h('option', { value, selected: true }, 'Current: ' + value));
  return sel;
}

const ivrExtra = [{ group: 'IVR', label: 'Play the menu again', value: 'menu-top:' }, { group: 'IVR', label: 'Leave the menu', value: 'menu-exit:' }];

function selectOf(options, value, blank) {
  const sel = h('select');
  if (blank !== undefined) sel.append(h('option', { value: '' }, blank));
  let found = !value;
  for (const o of options) {
    const [v, l] = Array.isArray(o) ? o : [o, o];
    if (String(v) === String(value)) found = true;
    sel.append(h('option', { value: v, selected: String(v) === String(value) }, l));
  }
  if (!found) sel.append(h('option', { value, selected: true }, value));
  return sel;
}

function gatewayOptions() { return (S.meta ? S.meta.gateways : []).map((g) => [g.gateway_uuid, g.gateway + (truthy(g.enabled) ? '' : ' (disabled)')]); }
function extOptions() { return (S.meta ? S.meta.extensions : []).map((e) => [e.extension, e.extension + (e.name && e.name !== e.extension ? ' ' + e.name : '')]); }
function agentOptions() { return (S.meta ? S.meta.agents : []).map((a) => [a.uuid, a.name + (a.extension ? ' (' + a.extension + ')' : '')]); }

let listId = 0;
function datalistInput(value, options) {
  const id = 'dl' + (++listId);
  return [h('input', { value: value || '', list: id }), h('datalist', { id }, options.map((o) => h('option', { value: o })))];
}

// A field widget: returns { el, get() }.
function widget(f, value) {
  value = value === undefined || value === null ? '' : String(value);
  let el, get;
  switch (f.t) {
    case 'bool': {
      const cb = h('input', { type: 'checkbox', checked: truthy(value) });
      el = h('label', { class: 'switch' }, cb, f.on || 'Yes');
      get = () => String(cb.checked);
      break;
    }
    case 'select': el = selectOf(typeof f.o === 'function' ? f.o() : f.o, value, f.blank); get = () => el.value; break;
    case 'dest': el = destOptions(value, f.ivr ? ivrExtra : null, f.efax); get = () => el.value; break;
    case 'gateway': el = selectOf(gatewayOptions(), value, f.blank || 'Any gateway'); get = () => el.value; break;
    case 'ext': el = selectOf(extOptions(), value, '— choose —'); get = () => el.value; break;
    case 'agent': el = selectOf(agentOptions(), value, '— choose —'); get = () => el.value; break;
    case 'moh': el = selectOf((S.meta ? S.meta.moh : []).map((m) => [m, m.replace('local_stream://', 'Music folder: ')]), value, 'Default music'); get = () => el.value; break;
    case 'sound': {
      const [inp, dl] = datalistInput(value, S.meta ? S.meta.prompts : []);
      inp.placeholder = 'prompts/welcome.wav';
      el = h('div', null, inp, dl);
      get = () => inp.value.trim();
      break;
    }
    case 'gateways': {
      const box = h('div', { class: 'multi' });
      const addRow = (v) => {
        const sel = selectOf(gatewayOptions(), v, '— gateway —');
        const row = h('div', { class: 'multi-row' }, sel, h('button', { type: 'button', class: 'btn small danger', onclick: () => row.remove() }, 'Remove'));
        box.insertBefore(row, addBtn);
      };
      const addBtn = h('button', { type: 'button', class: 'btn small', onclick: () => addRow('') }, '+ Add a fallback gateway');
      box.append(addBtn);
      const vals = value.split(',').map((s) => s.trim()).filter(Boolean);
      (vals.length ? vals : ['']).forEach(addRow);
      el = box;
      get = () => $$('select', box).map((s) => s.value).filter(Boolean).join(',');
      break;
    }
    case 'days': {
      const days = [['mon', 'Mon'], ['tue', 'Tue'], ['wed', 'Wed'], ['thu', 'Thu'], ['fri', 'Fri'], ['sat', 'Sat'], ['sun', 'Sun']];
      const on = new Set(value.split(',').map((s) => s.trim()));
      el = h('div', { class: 'days' }, days.map(([v, l]) => h('label', null, h('input', { type: 'checkbox', value: v, checked: on.has(v) }), l)));
      get = () => $$('input:checked', el).map((c) => c.value).join(',');
      break;
    }
    case 'textarea': el = h('textarea', { value }); el.value = value; get = () => el.value; break;
    case 'password': {
      const inp = h('input', { type: 'password', value, autocomplete: 'new-password', spellcheck: 'false', placeholder: f.ph || null });
      inp.value = value;
      const wrap = eyeToggle(inp);
      el = h('div', { class: 'multi-row' }, wrap, h('button', { type: 'button', class: 'btn small', title: 'Make a strong new password (shown so you can copy it)',
        onclick: () => { inp.value = randomPassword(f.len || 12); wrap.reveal(true); } }, 'New'));
      get = () => inp.value;
      break;
    }
    case 'secret': { // a PIN: hidden, with the eye, no generator
      const inp = h('input', { type: 'password', value, autocomplete: 'new-password', inputmode: 'numeric', spellcheck: 'false' });
      inp.value = value;
      el = eyeToggle(inp);
      get = () => inp.value.trim();
      break;
    }
    default:
      el = h('input', { type: f.t === 'number' ? 'number' : (f.t === 'time' ? 'time' : 'text'), value, placeholder: f.ph || null, min: f.t === 'number' ? '0' : null });
      get = () => el.value.trim();
  }
  return { el, get };
}

// ---------------------------------------------------------------------------
// Resources (lists and forms)

const STRATEGY_HELP = {
  'ring-all': 'Ring every free agent at once; the first to answer gets the call.',
  'longest-idle-agent': 'The agent who has waited longest since their last call (fair, the usual choice).',
  'round-robin': 'Agents take turns, carrying on from the last agent who got a call.',
  'top-down': 'Always start with the first agent in the list, then the next.',
  'agent-with-least-talk-time': 'The agent with the least talk time today.',
  'agent-with-fewest-calls': 'The agent who answered the fewest calls today.',
  'sequentially-by-agent-order': 'One agent at a time in list order (level, then position).',
  'random': 'A random free agent.',
  'ring-progressively': 'Ring the first agent, then add one more every few seconds while the others keep ringing.',
};

const R = {
  extensions: {
    title: 'Extensions', one: 'extension', key: 'extension_uuid', name: 'extension',
    intro: 'An extension is one phone (or app) user. The phone signs in with the extension number, the domain and the SIP password.',
    cols: [
      { label: 'Extension', key: 'extension' },
      { label: 'Status', render: (r) => (S.live && S.live.regs[r.extension]) ? h('span', null, h('span', { class: 'dot green' }), 'Registered') : h('span', null, h('span', { class: 'dot red' }), 'Unregistered') },
      { label: 'Name', render: (r) => {
        const name = (r.directory_first_name || r.directory_last_name) ? ((r.directory_first_name || '') + ' ' + (r.directory_last_name || '')).trim() : (r.effective_caller_id_name || '');
        return h('span', { class: 'cell-person' }, h('span', { class: 'avatar sm' }, initials(name || r.extension), h('i', { class: 'presence ' + ((S.live && S.live.regs[r.extension]) ? 'green' : 'red') })), name);
      } },
      { label: 'Web access', render: (r) => r.web_role === 'owner' ? badge('Owner', 'blue') : r.web_role === 'manager' ? badge('Manager', 'green') : r.web_role === 'user' ? badge('User') : '' },
      { label: 'Agent', render: (r) => truthy(r.agent_enabled) ? badge('Agent', 'amber') : '' },
      { label: 'Voicemail', render: (r) => yesNo(r.voicemail_enabled) },
      { label: 'Forwarding', render: (r) => truthy(r.do_not_disturb) ? badge('Do not disturb', 'red') : truthy(r.forward_all_enabled) ? badge('All → ' + r.forward_all_destination, 'amber') : '' },
      { label: 'Enabled', render: (r) => yesNo(r.enabled === '' ? 'true' : r.enabled) },
    ],
    defaults: () => ({ password: randomPassword(12), call_timeout: '30', enabled: 'true', voicemail_enabled: 'true', max_registrations: '3' }),
    fields: [
      { k: 'extension', l: 'Extension number', req: 1, help: 'Digits only, e.g. 1005.' },
      { k: 'password', l: 'SIP password', t: 'password', req: 1, help: 'The phone uses this to sign in. A strong one is filled in for you.' },
      { k: 'effective_caller_id_name', l: 'Name shown on internal calls', ph: 'e.g. Priya Sales' },
      { k: 'effective_caller_id_number', l: 'Number shown on internal calls', help: 'Usually the extension number.' },
      { k: 'outbound_caller_id_name', l: 'Name shown on outside calls' },
      { k: 'outbound_caller_id_number', l: 'Number shown on outside calls', help: 'Usually one of your DIDs.' },
      { k: 'call_timeout', l: 'Ring for (seconds)', t: 'number' },
      { k: 'call_group', l: 'Pickup group', help: 'Phones in the same group can answer each other\'s ringing calls with *8.' },
      { k: 'user_record', l: 'Record calls', t: 'select', o: [['', 'Never'], ['all', 'All calls'], ['inbound', 'Incoming only'], ['outbound', 'Outgoing only'], ['local', 'Internal only']] },
      { k: 'hold_music', l: 'Music on hold', t: 'moh' },
      { k: 'do_not_disturb', l: 'Do not disturb', t: 'bool', on: 'On (callers hear busy / go to voicemail)' },
      { sec: 'Call forwarding' },
      { k: 'forward_all_enabled', l: 'Forward all calls', t: 'bool', on: 'On' },
      { k: 'forward_all_destination', l: '…to number' },
      { k: 'forward_busy_enabled', l: 'Forward when busy', t: 'bool', on: 'On' },
      { k: 'forward_busy_destination', l: '…to number' },
      { k: 'forward_no_answer_enabled', l: 'Forward when not answered', t: 'bool', on: 'On' },
      { k: 'forward_no_answer_destination', l: '…to number' },
      { k: 'forward_user_not_registered_enabled', l: 'Forward when the phone is offline', t: 'bool', on: 'On' },
      { k: 'forward_user_not_registered_destination', l: '…to number' },
      { sec: 'Voicemail' },
      { k: 'voicemail_enabled', l: 'Voicemail box', t: 'bool', on: 'On (unanswered calls go to voicemail)' },
      { k: 'voicemail_password', l: 'Voicemail PIN', t: 'secret', help: 'Digits. Defaults to the extension number.' },
      { k: 'voicemail_mail_to', l: 'Email address (for your records)' },
      { sec: 'Web portal access (sign in with the extension number)' },
      { k: 'web_role', l: 'Access', t: 'select', o: [['', 'No web access'], ['user', 'User: dialer, chat, meetings, monitor, own reports'],
        ['manager', 'Manager: also listen / barge in, every agent and all reports'], ['owner', 'Owner: everything']] },
      { k: 'web_password', l: 'Web password', t: 'password', help: 'Separate from the SIP password. At least 8 characters. Click the eye to see it.', nogen: 0, len: 10,
        ph: (r) => truthy(r.web_password_set) && !r.web_password ? 'Set (shown here after this user signs in once more)' : null },
      { k: 'sso_email', l: 'Sign-in email (single sign-on)', ph: 'name@company.com',
        help: 'The Microsoft, Google or LinkedIn account that signs in as this extension (Settings → Integrations). Empty: their RingQ user email or the email address above.' },
      { sec: 'Call-centre agent' },
      { k: 'agent_enabled', l: 'Takes queue calls', t: 'bool', on: 'Yes, this extension is an agent', help: 'Also switched on automatically when you add the extension to a queue.' },
      { k: 'agent_call_timeout', l: 'Ring for (seconds)', t: 'number' },
      { k: 'agent_wrap_up_time', l: 'Wrap-up after a call (seconds)', t: 'number' },
      { k: 'agent_max_no_answer', l: 'Missed calls before automatic break', t: 'number', help: '0 = never' },
      { k: 'agent_no_answer_delay_time', l: 'Pause after a missed call (seconds)', t: 'number' },
      { sec: 'Advanced' },
      { k: 'directory_first_name', l: 'First name' },
      { k: 'directory_last_name', l: 'Last name' },
      { k: 'max_registrations', l: 'Devices allowed at once', t: 'number' },
      { k: 'limit_max', l: 'Simultaneous calls allowed', t: 'number' },
      { k: 'accountcode', l: 'Account code' },
      { k: 'toll_allow', l: 'Toll allow', help: 'e.g. domestic,international (for outbound route rules).' },
      { k: 'enabled', l: 'Enabled', t: 'bool', on: 'Enabled' },
      { k: 'description', l: 'Description', full: 1 },
    ],
  },
  gateways: {
    title: 'Gateways (SIP trunks)', one: 'gateway', key: 'gateway_uuid', name: 'gateway',
    intro: 'A gateway is your connection to a phone company (SIP trunk). Calls to the outside go out through it, and your DIDs come in through it.',
    cols: [
      { label: 'Name', key: 'gateway' },
      { label: 'Provider address', key: 'proxy' },
      { label: 'Status', render: (r) => gatewayState(r.gateway_uuid, r) },
      { label: 'Registers', render: (r) => yesNo(r.register) },
      { label: 'Enabled', render: (r) => yesNo(r.enabled === '' ? 'true' : r.enabled) },
    ],
    defaults: () => ({ register: 'true', register_transport: 'udp', expire_seconds: '800', retry_seconds: '30', enabled: 'true' }),
    fields: [
      { k: 'gateway', l: 'Name', req: 1, ph: 'e.g. Twilio, MyCarrier' },
      { k: 'proxy', l: 'Provider address', req: 1, ph: 'sip.provider.com or 203.0.113.10:5060', help: 'The SIP server your provider gave you.' },
      { k: 'register', l: 'Sign in to the provider', t: 'bool', on: 'Yes (use username and password)', help: 'Turn off for trunks that trust your server\'s IP address instead.' },
      { k: 'username', l: 'Username' },
      { k: 'password', l: 'Password', t: 'password' },
      { k: 'auth_username', l: 'Authentication username', help: 'Only if the provider gave a different one.' },
      { k: 'realm', l: 'Realm', help: 'Usually leave empty.' },
      { k: 'from_user', l: 'From user', help: 'Usually leave empty.' },
      { k: 'from_domain', l: 'From domain', help: 'Usually leave empty.' },
      { k: 'register_transport', l: 'Transport', t: 'select', o: [['udp', 'UDP'], ['tcp', 'TCP']] },
      { k: 'expire_seconds', l: 'Registration every (seconds)', t: 'number' },
      { k: 'retry_seconds', l: 'Retry after failure (seconds)', t: 'number' },
      { k: 'caller_id_in_from', l: 'Caller ID in From header', t: 'bool', on: 'Yes' },
      { k: 'outbound_proxy', l: 'Outbound proxy', help: 'Usually leave empty.' },
      { k: 'enabled', l: 'Enabled', t: 'bool', on: 'Enabled' },
      { k: 'description', l: 'Description', full: 1 },
    ],
  },
  dids: {
    title: 'Inbound numbers (DIDs)', one: 'inbound number', key: 'destination_uuid', name: 'destination_number',
    intro: 'A DID is a phone number people dial to reach you. Choose the gateway it arrives on and where the call should go: an extension, queue, IVR menu, ring group, office hours…',
    cols: [
      { label: 'Number', key: 'destination_number' },
      { label: 'Country', render: (r) => { const c = didCountry(r); return [flag(r.destination_number, r.destination_prefix), ' ', c ? c[1] : '']; } },
      { label: 'Arrives on', render: (r) => r.gateway_uuid ? gatewayName(r.gateway_uuid) : 'Any gateway' },
      { label: 'Goes to', render: (r) => destLabel(r.action) },
      { label: 'Record', render: (r) => yesNo(r.destination_record) },
      { label: 'Enabled', render: (r) => yesNo(r.destination_enabled === '' ? 'true' : r.destination_enabled) },
      { label: 'Description', key: 'destination_description' },
    ],
    defaults: () => ({ destination_enabled: 'true' }),
    fields: [
      { k: 'destination_prefix', l: 'Country', t: 'select', o: () => countryOptions(), blank: '— choose the country —', help: 'Calls match the number with or without this country code.' },
      { k: 'destination_number', l: 'Phone number', req: 1, ph: 'e.g. 6531591234', help: 'As the provider sends it (with or without +).' },
      { k: 'gateway_uuid', l: 'Arrives on gateway', t: 'gateway', help: 'Pick the trunk this number belongs to, or "Any gateway".' },
      { k: 'action', l: 'Send calls to', t: 'dest', efax: 1, help: 'Leave empty to link it later on Call Flow (calls to it are not answered until then). eFax: the number answers as a fax machine for that extension.' },
      { k: 'destination_cid_name_prefix', l: 'Caller name prefix', help: 'e.g. "Sales:" so staff know which number was called.' },
      { k: 'destination_record', l: 'Record calls', t: 'bool', on: 'Record every call to this number' },
      { k: 'destination_enabled', l: 'Enabled', t: 'bool', on: 'Enabled' },
      { k: 'destination_description', l: 'Description', full: 1 },
    ],
  },
  routes: {
    title: 'Outbound rules', one: 'outbound rule', key: 'outbound_route_uuid', name: 'route_name',
    intro: 'Outbound rules decide which gateway carries calls to outside numbers, or block them. The first enabled rule (lowest order) that matches a call is used.',
    form: (id) => outboundRuleForm(id),
    cols: [
      { label: 'Name', key: 'route_name' },
      { label: 'Matching calls', render: (r) => ruleSummary(r) },
      { label: 'Route', render: (r) => truthy(r.route_block) ? badge('BLOCK', 'red') : (r.gateway_uuids || '').split(',').filter(Boolean).map(gatewayName).join(' → ') },
      { label: 'Strip / prepend', render: (r) => [Number(r.route_strip) ? 'strip ' + r.route_strip : '', r.route_prepend ? 'add ' + r.route_prepend : ''].filter(Boolean).join(', ') || '—' },
      { label: 'Caller ID', render: (r) => r.caller_id_number || h('span', { class: 'muted' }, 'Extension\'s') },
      { label: 'Order', key: 'route_order', num: 1 },
      { label: 'Enabled', render: (r) => yesNo(r.route_enabled === '' ? 'true' : r.route_enabled) },
    ],
    defaults: () => ({ route_pattern: '^\\+?(\\d{7,15})$', route_strip: '0', route_order: '100', route_enabled: 'true' }),
    fields: [
      { k: 'route_name', l: 'Name', req: 1, ph: 'e.g. All outside calls' },
      { k: 'route_pattern', l: 'Numbers matching (pattern)', req: 1, help: 'Examples: ^\\+?(\\d{7,15})$ = any number of 7–15 digits;  ^0(\\d+)$ = numbers starting with 0;  ^(9\\d{7})$ = 8-digit numbers starting with 9.' },
      { k: 'gateway_uuids', l: 'Send through gateway(s)', t: 'gateways', req: 1, full: 1, help: 'If the first gateway fails, the next one is tried.' },
      { k: 'route_strip', l: 'Remove digits from the front', t: 'number', help: 'e.g. 1 to drop a leading 0 or 9.' },
      { k: 'route_prepend', l: 'Add digits to the front', help: 'e.g. + or a country code.' },
      { k: 'caller_id_number', l: 'Caller ID number to send', help: 'Leave empty to use each extension\'s outside caller ID.' },
      { k: 'caller_id_name', l: 'Caller ID name to send' },
      { k: 'route_order', l: 'Order', t: 'number', help: 'Lower numbers are tried first.' },
      { k: 'route_enabled', l: 'Enabled', t: 'bool', on: 'Enabled' },
      { k: 'route_description', l: 'Description', full: 1 },
    ],
  },
  ringgroups: {
    title: 'Ring groups', one: 'ring group', key: 'ring_group_uuid', name: 'ring_group_name',
    intro: 'A ring group rings several phones for one number (e.g. "Sales" rings 1001, 1002 and 1003).',
    cols: [
      { label: 'Number', key: 'ring_group_extension' },
      { label: 'Name', key: 'ring_group_name' },
      { label: 'Strategy', key: 'ring_group_strategy' },
      { label: 'If nobody answers', render: (r) => destLabel(r.timeout_action) },
      { label: 'Enabled', render: (r) => yesNo(r.ring_group_enabled === '' ? 'true' : r.ring_group_enabled) },
    ],
    defaults: () => ({ ring_group_strategy: 'simultaneous', ring_group_call_timeout: '30', ring_group_enabled: 'true' }),
    fields: [
      { k: 'ring_group_name', l: 'Name', req: 1 },
      { k: 'ring_group_extension', l: 'Number', req: 1, help: 'The number to dial (or to choose for a DID), e.g. 2000.' },
      { k: 'ring_group_strategy', l: 'How to ring', t: 'select', o: [['simultaneous', 'All at once'], ['sequence', 'One after another'], ['enterprise', 'All at once (enterprise)'], ['rollover', 'Next free phone'], ['random', 'Random']] },
      { k: 'ring_group_call_timeout', l: 'Ring for (seconds)', t: 'number' },
      { k: 'timeout_action', l: 'If nobody answers, go to', t: 'dest' },
      { k: 'ring_group_greeting', l: 'Greeting to play first', t: 'sound' },
      { k: 'ring_group_cid_name_prefix', l: 'Caller name prefix' },
      { k: 'ring_group_enabled', l: 'Enabled', t: 'bool', on: 'Enabled' },
      { k: 'ring_group_description', l: 'Description', full: 1 },
    ],
    child: {
      field: 'destinations', title: 'Phones to ring', add: '+ Add a phone',
      cols: [
        { k: 'destination_number', l: 'Extension or number', t: 'extfree' },
        { k: 'destination_delay', l: 'Start after (s)', t: 'number', def: '0' },
        { k: 'destination_timeout', l: 'Ring for (s)', t: 'number', def: '30' },
      ],
    },
  },
  ivrs: {
    title: 'IVR menus', one: 'IVR menu', key: 'ivr_menu_uuid', name: 'ivr_menu_name',
    intro: 'An IVR menu plays a greeting ("Press 1 for sales, 2 for support") and sends the caller where they choose.',
    cols: [
      { label: 'Number', key: 'ivr_menu_extension' },
      { label: 'Name', key: 'ivr_menu_name' },
      { label: 'Greeting', key: 'ivr_menu_greet_long' },
      { label: 'Enabled', render: (r) => yesNo(r.ivr_menu_enabled === '' ? 'true' : r.ivr_menu_enabled) },
    ],
    defaults: () => ({ ivr_menu_timeout: '3000', ivr_menu_inter_digit_timeout: '2000', ivr_menu_max_failures: '3', ivr_menu_max_timeouts: '3', ivr_menu_digit_len: '5', ivr_menu_enabled: 'true' }),
    fields: [
      { k: 'ivr_menu_name', l: 'Name', req: 1 },
      { k: 'ivr_menu_extension', l: 'Number', req: 1, help: 'e.g. 5000. Point a DID or office hours at it.' },
      { k: 'ivr_menu_greet_long', l: 'Greeting', t: 'sound', help: 'Upload WAV files under Settings → Sounds & music.' },
      { k: 'ivr_menu_greet_short', l: 'Short greeting (repeat)', t: 'sound' },
      { k: 'ivr_menu_invalid_sound', l: 'Wrong key message', t: 'sound' },
      { k: 'ivr_menu_exit_sound', l: 'Goodbye message', t: 'sound' },
      { k: 'ivr_menu_timeout', l: 'Wait for a key (milliseconds)', t: 'number' },
      { k: 'ivr_menu_max_failures', l: 'Wrong keys allowed', t: 'number' },
      { k: 'ivr_menu_max_timeouts', l: 'Times to repeat when no key', t: 'number' },
      { k: 'ivr_menu_direct_dial', l: 'Callers may dial an extension', t: 'bool', on: 'Yes' },
      { k: 'ivr_menu_digit_len', l: 'Longest extension number', t: 'number' },
      { k: 'exit_action', l: 'When the caller does not choose, go to', t: 'dest' },
      { k: 'ivr_menu_cid_prefix', l: 'Caller name prefix' },
      { k: 'ivr_menu_enabled', l: 'Enabled', t: 'bool', on: 'Enabled' },
      { k: 'ivr_menu_description', l: 'Description', full: 1 },
    ],
    child: {
      field: 'options', title: 'Menu keys', add: '+ Add a key',
      cols: [
        { k: 'ivr_menu_option_digits', l: 'Key', def: '' },
        { k: 'action', l: 'Goes to', t: 'dest', ivr: 1 },
        { k: 'ivr_menu_option_description', l: 'Note', def: '' },
      ],
    },
  },
  queues: {
    title: 'Queues', one: 'queue', key: 'call_center_queue_uuid', name: 'queue_name',
    intro: 'A queue holds callers (with music) until an agent is free, using the strategy you choose. Agents on a break (AUX code) or logged out get no calls.',
    cols: [
      { label: 'Number', key: 'queue_extension' },
      { label: 'Name', key: 'queue_name' },
      { label: 'Strategy', key: 'queue_strategy' },
      { label: 'Record', render: (r) => yesNo(r.record) },
      { label: 'When nobody answers', render: (r) => destLabel(r.queue_timeout_action) },
    ],
    defaults: () => ({ queue_strategy: 'longest-idle-agent', queue_moh_sound: 'local_stream://default', queue_max_wait_time: '0', queue_max_wait_time_with_no_agent: '90',
      queue_max_wait_time_with_no_agent_time_reached: '5', queue_tier_rules_apply: 'false', queue_tier_rule_wait_second: '300', queue_tier_rule_no_agent_no_wait: 'true',
      queue_tier_rule_wait_multiply_level: 'true', queue_discard_abandoned_after: '900', record: 'false' }),
    fields: [
      { k: 'queue_name', l: 'Name', req: 1 },
      { k: 'queue_extension', l: 'Number', req: 1, help: 'e.g. 3000. Point a DID, IVR key or office hours at it.' },
      { k: 'queue_strategy', l: 'Which agent gets the call', t: 'select', o: () => (S.meta ? S.meta.strategies : []).map((s) => [s, s + ' — ' + (STRATEGY_HELP[s] || '')]), full: 1 },
      { k: 'queue_moh_sound', l: 'Music while waiting', t: 'moh' },
      { k: 'queue_greeting', l: 'Greeting', t: 'sound' },
      { k: 'queue_announce_sound', l: 'Repeat announcement', t: 'sound', help: 'e.g. "All our agents are busy, please hold".' },
      { k: 'queue_announce_frequency', l: 'Announce every (seconds)', t: 'number' },
      { k: 'queue_max_wait_time', l: 'Longest wait (seconds, 0 = no limit)', t: 'number' },
      { k: 'queue_max_wait_time_with_no_agent', l: 'Leave when no agent is available for (s)', t: 'number',
        help: 'Agents on a break (also "Max no answer" after missed calls), logged out or with no phone online are not available; agents busy on a call are. 0 = never leave.' },
      { k: 'queue_max_wait_time_with_no_agent_time_reached', l: '…after waiting at least (s)', t: 'number' },
      { k: 'queue_timeout_action', l: 'Then go to', t: 'dest', help: 'Where callers go when they leave the queue unanswered, after hearing "Sorry, no agent is available" (or "all our agents are still busy" after the longest wait): e.g. an extension\'s voicemail. Empty: the call ends.' },
      { k: 'queue_cc_exit_keys', l: 'Exit key', help: 'A key callers can press to leave the queue (e.g. 0), then "Then go to".' },
      { k: 'record', l: 'Record queue calls', t: 'bool', on: 'Record' },
      { k: 'queue_cid_prefix', l: 'Caller name prefix' },
      { sec: 'Tiers (advanced)' },
      { k: 'queue_tier_rules_apply', l: 'Use tier levels', t: 'bool', on: 'Yes', help: 'Level 2 agents only get calls after the caller waited the time below.' },
      { k: 'queue_tier_rule_wait_second', l: 'Wait per level (s)', t: 'number' },
      { k: 'queue_tier_rule_wait_multiply_level', l: 'Multiply wait by level', t: 'bool', on: 'Yes' },
      { k: 'queue_tier_rule_no_agent_no_wait', l: 'Skip empty levels', t: 'bool', on: 'Yes' },
      { k: 'queue_discard_abandoned_after', l: 'Forget abandoned callers after (s)', t: 'number' },
      { k: 'queue_description', l: 'Description', full: 1 },
    ],
    child: {
      field: 'agents', title: 'Agents in this queue (any extension; it becomes an agent)', add: '+ Add an agent',
      cols: [
        { k: 'extension', l: 'Extension', t: 'ext' },
        { k: 'tier_level', l: 'Level', t: 'number', def: '1' },
        { k: 'tier_position', l: 'Position', t: 'number', def: '1' },
      ],
    },
  },
  agents: {
    title: 'Agents', one: 'agent', key: 'call_center_agent_uuid', name: 'agent_name',
    intro: 'An agent is an extension that takes queue calls. Agents log in / out and take breaks with *22 and *23 on their phone, or on the Call centre page.',
    cols: [
      { label: 'Name', key: 'agent_name' },
      { label: 'Extension', key: 'extension' },
      { label: 'Status now', render: (r) => { const a = S.live && S.live.agents[r.call_center_agent_uuid]; return a ? statusBadge(a.status, a.aux) : ''; } },
      { label: 'Ring for (s)', key: 'agent_call_timeout', num: 1 },
      { label: 'Wrap-up (s)', key: 'agent_wrap_up_time', num: 1 },
      { label: 'Max no answer', key: 'agent_max_no_answer', num: 1 },
    ],
    defaults: () => ({ agent_call_timeout: '20', agent_max_no_answer: '3', agent_wrap_up_time: '10', agent_reject_delay_time: '10', agent_busy_delay_time: '60', agent_no_answer_delay_time: '30' }),
    fields: [
      { k: 'agent_name', l: 'Name', req: 1 },
      { k: 'extension', l: 'Extension', t: 'ext', req: 1, help: 'Queue calls ring this extension.' },
      { k: 'agent_id', l: 'Agent ID', help: 'Defaults to the extension number.' },
      { k: 'agent_call_timeout', l: 'Ring for (seconds)', t: 'number' },
      { k: 'agent_wrap_up_time', l: 'Wrap-up after a call (seconds)', t: 'number', help: 'Time to finish notes before the next call.' },
      { k: 'agent_max_no_answer', l: 'Missed calls before automatic break', t: 'number', help: 'After this many unanswered calls in a row the agent is put on break ("Max no answer"). 0 = never.' },
      { k: 'agent_no_answer_delay_time', l: 'Pause after a missed call (s)', t: 'number' },
      { k: 'agent_busy_delay_time', l: 'Pause when busy (s)', t: 'number' },
      { k: 'agent_reject_delay_time', l: 'Pause after rejecting (s)', t: 'number' },
    ],
  },
  auxcodes: {
    title: 'AUX / break codes', one: 'AUX code', key: 'aux_code_uuid', name: 'aux_code_name',
    intro: 'AUX codes are the reasons an agent is not taking calls (Lunch, Meeting…). While on any AUX code the agent gets no queue calls; with "Block direct calls" their extension does not ring either. Every change is logged for the reports. Desk phones and Agent Status keys dial *26 and the number shown (*261 is Available).',
    cols: [
      { label: 'Name', key: 'aux_code_name' },
      { label: 'Blocks direct calls too', render: (r) => yesNo(r.block_direct_calls) },
      { label: 'Order', key: 'aux_code_order', num: 1 },
      { label: 'Desk phone dials', render: (r) => r.dial ? h('code', null, r.dial) : '' },
      { label: 'Enabled', render: (r) => yesNo(r.aux_code_enabled === '' ? 'true' : r.aux_code_enabled) },
    ],
    defaults: () => ({ block_direct_calls: 'false', aux_code_order: '100', aux_code_enabled: 'true' }),
    fields: [
      { k: 'aux_code_name', l: 'Name', req: 1, ph: 'e.g. Lunch' },
      { k: 'block_direct_calls', l: 'Block direct calls', t: 'bool', on: 'Also stop calls to the agent\'s own extension (callers hear busy / voicemail)' },
      { k: 'aux_code_order', l: 'Order in lists', t: 'number' },
      { k: 'aux_code_enabled', l: 'Enabled', t: 'bool', on: 'Enabled' },
    ],
  },
  timeconditions: {
    title: 'Time conditions', one: 'time condition', key: 'time_condition_uuid', name: 'condition_name',
    intro: 'Send calls one way during opening hours and another way outside them (and on holidays). Point a DID at its number. The company-wide office hours, breaks and holidays are in Settings → Language & office hours.',
    cols: [
      { label: 'Number', key: 'condition_extension' },
      { label: 'Name', key: 'condition_name' },
      { label: 'Open', render: (r) => (r.weekdays || '').split(',').join(', ') + ' ' + (r.time_from || '') + '–' + (r.time_to || '') },
      { label: 'Open → ', render: (r) => destLabel(r.match_action) },
      { label: 'Closed →', render: (r) => destLabel(r.nomatch_action) },
    ],
    defaults: () => ({ weekdays: 'mon,tue,wed,thu,fri', time_from: '09:00', time_to: '18:00', condition_enabled: 'true' }),
    fields: [
      { k: 'condition_name', l: 'Name', req: 1, ph: 'e.g. Main office hours' },
      { k: 'condition_extension', l: 'Number', req: 1, help: 'e.g. 6000. Point your DID at this number.' },
      { k: 'weekdays', l: 'Open on', t: 'days', full: 1 },
      { k: 'time_from', l: 'Opens at', t: 'time' },
      { k: 'time_to', l: 'Closes at', t: 'time' },
      { k: 'match_action', l: 'When open, go to', t: 'dest', req: 1 },
      { k: 'nomatch_action', l: 'When closed, go to', t: 'dest', req: 1 },
      { k: 'holidays', l: 'Holidays (closed all day)', t: 'textarea', full: 1, help: 'Dates as YYYY-MM-DD separated by commas, e.g. 2026-12-25, 2027-01-01' },
      { k: 'condition_enabled', l: 'Enabled', t: 'bool', on: 'Enabled' },
      { k: 'condition_description', l: 'Description', full: 1 },
    ],
  },
  conferences: {
    title: 'Conference rooms', one: 'conference room', key: 'conference_uuid', name: 'conference_name',
    intro: 'Callers who dial the room number talk together. Press 0 to mute yourself, # to leave.',
    cols: [
      { label: 'Number', key: 'conference_extension' },
      { label: 'Name', key: 'conference_name' },
      { label: 'PIN', render: (r) => r.conference_pin_number ? 'Yes' : 'No' },
      { label: 'Record', render: (r) => yesNo(r.record) },
      { label: 'In the room now', render: (r) => { const c = S.live && S.live.confs[r.conference_extension]; return c ? badge(c + ' caller(s)', 'green') : ''; } },
    ],
    defaults: () => ({ conference_enabled: 'true', record: 'false' }),
    fields: [
      { k: 'conference_name', l: 'Name', req: 1 },
      { k: 'conference_extension', l: 'Number', req: 1, help: 'e.g. 4000.' },
      { k: 'conference_pin_number', l: 'PIN', help: 'Digits; leave empty for no PIN.' },
      { k: 'record', l: 'Record', t: 'bool', on: 'Record the conference' },
      { k: 'conference_enabled', l: 'Enabled', t: 'bool', on: 'Enabled' },
      { k: 'conference_description', l: 'Description', full: 1 },
    ],
  },
  faxes: {
    title: 'Fax extensions', one: 'fax extension', key: 'fax_uuid', name: 'fax_name',
    intro: 'A fax extension answers like a fax machine; faxes it receives arrive in eFax → Inbox. Assign a DID to it in Call Flow (Others).',
    cols: [
      { label: 'Number', key: 'fax_extension' },
      { label: 'Name', key: 'fax_name' },
      { label: 'Fax number (station id)', render: (r) => r.fax_caller_id_number || '' },
      { label: 'Description', key: 'fax_description' },
    ],
    defaults: () => ({}),
    fields: [
      { k: 'fax_name', l: 'Name', req: 1, ph: 'e.g. Office fax' },
      { k: 'fax_extension', l: 'Number', req: 1, help: 'e.g. 3290. Point your fax DID at this number (Call Flow → Others).' },
      { k: 'fax_caller_id_number', l: 'Fax number (station id)', ph: 'e.g. +6531591234', help: 'Shown on the sender\'s machine; the extension number when empty.' },
      { k: 'fax_caller_id_name', l: 'Name shown' },
      { k: 'fax_email', l: 'Email', help: 'Kept for the RingQ portal; received faxes are in eFax → Inbox.' },
      { k: 'fax_description', l: 'Description', full: 1 },
    ],
  },
};

// The window of Call Flow's + for a new extension: what it needs to work;
// the rest is on the Extensions page.
R.extensions.quick = R.extensions.fields.filter((f) => ['extension', 'password', 'directory_first_name', 'directory_last_name',
  'outbound_caller_id_number', 'voicemail_enabled', 'voicemail_password', 'web_role', 'web_password'].includes(f.k));
R.extensions.quickIntro = 'Forwarding, recording and call-centre settings are on the Extensions page.';


function gatewayName(uuid) {
  const g = (S.meta ? S.meta.gateways : []).find((x) => x.gateway_uuid === uuid);
  return g ? g.gateway : uuid;
}
function destLabel(v) {
  if (!v) return '';
  const d = (S.meta ? S.meta.destinations : []).concat(ivrExtra).find((x) => x.value === v);
  return d ? d.group + ': ' + d.label : v;
}
function gatewayState(uuid, row) {
  const g = S.live && S.live.gateways[uuid];
  if (!g) return truthy(row.enabled === '' ? 'true' : row.enabled) ? badge('Not loaded') : badge('Disabled');
  const st = g.state || '';
  if (/REGED|UP|NOREG/i.test(st)) return badge(st === 'NOREG' ? 'Ready (no sign-in)' : 'Connected', 'green');
  if (/FAIL|DOWN/i.test(st)) return h('span', null, badge('Failed', 'red'), ' ', h('span', { class: 'muted small' }, g.last_error || ''));
  return badge(st || 'Trying', 'amber');
}

// Live state used to decorate lists.
async function loadLive() {
  try {
    const l = await api('GET', 'live?domain=' + encodeURIComponent(S.domain));
    S.live = { raw: l, regs: {}, agents: {}, gateways: {}, confs: {} };
    for (const r of l.registrations) S.live.regs[r.user] = r;
    for (const a of l.agents) S.live.agents[a.uuid] = a;
    for (const g of l.gateways) S.live.gateways[g.uuid || g.name] = g;
    for (const c of l.conferences) S.live.confs[c.name] = c.members.length;
  } catch (e) { S.live = null; }
}

async function resourcePage(name, id) {
  const def = R[name];
  const main = $('#main');
  if (id !== undefined) return resourceForm(name, id === 'new' ? null : id);
  const [rows] = await Promise.all([api('GET', dpath(name)), loadLive()]);
  const q = h('input', { type: 'search', placeholder: 'Search ' + def.title.toLowerCase(), 'aria-label': 'Search ' + def.title.toLowerCase() });
  const body = h('div');
  let page = 0, size = 10;
  try { size = Number(localStorage.getItem('rq-page-size')) || 10; } catch (e) { /* private mode */ }
  // Search every text value of a row (number, name, description...).
  const text = (r) => Object.values(r).filter((v) => typeof v === 'string' && v.length < 200).join(' ').toLowerCase();
  const draw = () => {
    const f = q.value.trim().toLowerCase();
    const shown = f ? rows.filter((r) => text(r).includes(f)) : rows;
    const pages = Math.max(1, Math.ceil(shown.length / size));
    page = Math.min(page, pages - 1);
    const slice = shown.slice(page * size, (page + 1) * size);
    const go = (n) => { page = n; draw(); };
    const nums = [];
    for (let i = Math.max(0, page - 2); i < Math.min(pages, page + 3); i++) nums.push(h('button', { class: 'page-num' + (i === page ? ' on' : ''), type: 'button', onclick: () => go(i) }, String(i + 1)));
    const per = h('select', { 'aria-label': 'Rows per page' }, [10, 25, 50, 100].map((n) => h('option', { value: n, selected: n === size }, String(n))));
    per.addEventListener('change', () => { size = Number(per.value); page = 0; try { localStorage.setItem('rq-page-size', size); } catch (e) { /* private mode */ } draw(); });
    fill(body,
      table(def.cols, slice, { click: (r) => { location.hash = '#/' + name + '/' + r[def.key]; },
        empty: f ? 'Nothing matches "' + q.value.trim() + '".' : 'No ' + def.title.toLowerCase() + ' yet. Click "New ' + def.one + '".' }),
      shown.length > 10 ? h('div', { class: 'pager' }, h('span', { class: 'muted' }, 'Showing ' + (page * size + 1) + '–' + Math.min(shown.length, (page + 1) * size) + ' of ' + shown.length),
        h('span', { class: 'spacer' }), h('button', { class: 'btn small', type: 'button', disabled: page === 0, onclick: () => go(page - 1) }, 'Previous'), nums,
        h('button', { class: 'btn small', type: 'button', disabled: page >= pages - 1, onclick: () => go(page + 1) }, 'Next'), h('span', { class: 'spacer' }),
        h('label', { class: 'muted per-page' }, 'Rows per page ', per)) : null);
  };
  q.addEventListener('input', () => { page = 0; draw(); });
  fill(main,
    h('div', { class: 'page-head' }, h('div', null, h('h1', null, def.title, h('span', { class: 'count-pill' }, String(rows.length))),
      def.intro ? h('div', { class: 'muted page-sub' }, def.intro) : null),
    h('span', { class: 'spacer' }), rows.length > 5 ? h('label', { class: 'search-wrap' }, icon('search', 16), q) : null,
    h('a', { class: 'btn primary', href: '#/' + name + '/new' }, icon('plus', 16), 'New ' + def.one)),
    body);
  draw();
}

function childEditor(spec, rows) {
  const body = h('tbody');
  const addRow = (data) => {
    data = data || {};
    const cells = spec.cols.map((c) => {
      let w;
      if (c.t === 'extfree') {
        const [inp, dl] = datalistInput(data[c.k], (S.meta ? S.meta.extensions : []).map((e) => e.extension));
        w = { el: h('div', null, inp, dl), get: () => inp.value.trim() };
      } else {
        w = widget(c, data[c.k] !== undefined ? data[c.k] : c.def);
      }
      return { c, w };
    });
    const tr = h('tr', null, cells.map(({ w }) => h('td', null, w.el)),
      h('td', null, h('button', { type: 'button', class: 'btn small danger', onclick: () => { tr.remove(); } }, 'Remove')));
    tr._get = () => Object.fromEntries(cells.map(({ c, w }) => [c.k, w.get()]));
    body.append(tr);
  };
  (rows || []).forEach(addRow);
  const el = h('div', { class: 'field full' },
    h('label', null, spec.title),
    h('div', { class: 'table-wrap' }, h('table', { class: 'child-table' },
      h('thead', null, h('tr', null, spec.cols.map((c) => h('th', null, c.l)), h('th'))), body)),
    h('button', { type: 'button', class: 'btn small', onclick: () => addRow({}) }, spec.add));
  return { el, get: () => $$('tr', body).map((tr) => tr._get()) };
}

// resourceFields builds the fields of a resource's form (fields, then its
// child table): { el, body() } where body() is what to save. A field with
// w makes its own widget: w(value, row) returns { el, get() }.
function resourceFields(fields, childSpec, row, id) {
  const widgets = [];
  const form = h('div', { class: 'form' });
  for (const f of fields) {
    if (f.sec) { form.append(h('div', { class: 'section' }, f.sec)); continue; }
    const fo = Object.assign({}, f, { o: typeof f.o === 'function' ? f.o() : f.o, ph: typeof f.ph === 'function' ? f.ph(row) : f.ph });
    const w = f.w ? f.w(row[f.k], row) : widget(fo, row[f.k]);
    widgets.push([f, w]);
    form.append(h('div', { class: 'field' + (f.full ? ' full' : '') }, h('label', { class: f.req ? 'req' : null }, f.l), w.el,
      f.help ? h('div', { class: 'help' }, f.help) : null));
  }
  let child = null;
  if (childSpec) {
    child = childEditor(childSpec, row[childSpec.field] || []);
    form.append(child.el);
  }
  const body = () => {
    const out = {};
    for (const [f, w] of widgets) {
      const v = w.get();
      if (!id && v === '' && f.t !== 'bool') continue;
      out[f.k] = v;
    }
    if (child) out[childSpec.field] = child.get();
    return out;
  };
  return { el: form, body };
}

// resourceModal opens the form of a new (or existing) resource in a window,
// so the page underneath (Call Flow...) stays where it was. A resource with
// a page of its own for its form offers its quick fields here.
async function resourceModal(name, id, onSaved) {
  const def = R[name];
  await loadMeta();
  const row = id ? await api('GET', dpath(name + '/' + id)) : def.defaults();
  const quick = def.quick || def.fields;
  const fields = resourceFields(quick, def.quick ? def.quickChild : def.child, row, id);
  fields.el.classList.add('cf-form');
  modal((id ? t('Edit') + ' ' : t('New') + ' ') + t(def.one), h('div', null, def.quickIntro ? h('p', { class: 'muted small' }, def.quickIntro) : null, fields.el),
    t('Save'), async () => {
      const saved = await api(id ? 'PUT' : 'POST', dpath(name + (id ? '/' + id : '')), fields.body());
      toast(t('Saved'));
      await loadMeta();
      if (onSaved) await onSaved(saved);
    });
  const box = $$('.modal').pop();
  if (box && (def.child || def.quick)) box.classList.add('wide');
}

async function resourceForm(name, id) {
  const def = R[name];
  if (def.form) return def.form(id);
  const main = $('#main');
  await loadMeta();
  const row = id ? await api('GET', dpath(name + '/' + id)) : def.defaults();
  const fields = resourceFields(def.fields, def.child, row, id);
  const form = fields.el;
  const err = h('div', { class: 'error-text' });
  const save = async () => {
    err.textContent = '';
    const body = fields.body();
    try {
      saveBtn.disabled = true;
      await api(id ? 'PUT' : 'POST', dpath(name + (id ? '/' + id : '')), body);
      toast('Saved');
      await loadMeta();
      location.hash = '#/' + name;
    } catch (e) { err.textContent = e.message; } finally { saveBtn.disabled = false; }
  };
  const saveBtn = h('button', { class: 'btn primary', type: 'button', onclick: save }, 'Save');
  const del = id ? h('button', { class: 'btn danger', type: 'button', onclick: async () => {
    if (!confirm('Delete this ' + def.one + '? This cannot be undone.')) return;
    try { await api('DELETE', dpath(name + '/' + id)); toast('Deleted'); await loadMeta(); location.hash = '#/' + name; } catch (e) { fail(e); }
  } }, 'Delete') : null;
  fill(main, 
    pageHead((id ? 'Edit ' : 'New ') + def.one + (id && row[def.name] ? ': ' + row[def.name] : '')),
    h('div', { class: 'panel' }, form, err,
      h('div', { class: 'form-actions' }, saveBtn, h('a', { class: 'btn', href: '#/' + name }, 'Cancel'), h('span', { style: 'flex:1' }), del)));
  if (name === 'extensions' && id) {
    main.append(h('div', { class: 'panel' }, h('h2', null, 'Phone settings'),
      h('p', null, 'Enter these in the phone or softphone app:'),
      table([{ label: 'Setting', key: 's' }, { label: 'Value', key: 'v' }], [
        { s: 'SIP server / domain', v: S.domain }, { s: 'Proxy / outbound proxy', v: location.hostname + ':' + (S.live && S.live.raw.stats.sip_port || 5060) },
        { s: 'User name / extension', v: row.extension }, { s: 'Password', v: secretText(row.password) }, { s: 'Transport', v: 'UDP' }])));
    if (window.phoneKeysPanel) main.append(phoneKeysPanel(row.extension));
  }
  const first = $('input, select', form);
  if (first && !id) first.focus();
}

// ---------------------------------------------------------------------------
// Pages

const PAGES = {};

PAGES.dashboard = async () => {
  const main = $('#main');
  let recent = null;
  let entering = true; // the cards rise in once, not at every refresh
  const failedStatus = (s) => ['missed', 'failed', 'busy', 'cancelled', 'no answer', 'rejected'].includes(String(s || '').toLowerCase());
  const loadRecent = async () => {
    if (!S.me.database || !S.domain) return;
    try {
      const res = await api('GET', dpath('cdr?from=' + today() + '&to=' + today() + '&limit=100'));
      recent = { rows: res.rows.slice(0, 6), failed: res.rows.filter((r) => failedStatus(r.status)).length };
    } catch (e) { recent = { rows: [], failed: 0 }; }
    if (S.live && S.live.raw) render(S.live.raw);
  };
  const render = (l) => {
    S.live = { gateways: Object.fromEntries(l.gateways.map((g) => [g.uuid || g.name, g])), regs: {}, agents: {}, confs: {}, raw: l };
    const focused = document.activeElement && main.contains(document.activeElement) && /SELECT|INPUT/.test(document.activeElement.tagName);
    if (focused) return;
    const calls = groupCalls(l.channels);
    fill(main, h('div', { class: 'dash' + (entering ? ' dash-in' : '') },
      h('div', { class: 'dash-main' },
        activeCallsCard(calls),
        isSupervisor() ? devicesCard(l) : null,
        l.conferences.length ? h('div', { class: 'panel' }, h('h2', null, 'Conferences now'), table([{ label: 'Room', key: 'name' },
          { label: 'Callers', render: (c) => c.members.map((m) => (m.cid_name || m.cid_number) + (m.muted ? ' (muted)' : '')).join(', '), wrap: 1 }], l.conferences)) : null,
        Object.keys(l.parked || {}).length ? h('div', { class: 'panel' }, h('h2', null, 'Parked calls'),
          table([{ label: 'Slot', key: 's' }, { label: 'Call', key: 'c' }], Object.entries(l.parked).map(([s, c]) => ({ s, c })))) : null),
      h('div', { class: 'dash-side' },
        isOwner() ? healthCard(l.health) : null,
        extensionsCard(l),
        isOwner() ? trunksCard(l.gateways) : null,
        systemCard(l),
        S.me.database ? recentCard(recent) : null)));
    entering = false;
  };
  liveOn(render);
  loadRecent();
  every(30000, loadRecent);
  every(1000, () => tickTimers(main));
};

function card(num, label) { return h('div', { class: 'card' }, h('div', { class: 'lbl' }, label), h('div', { class: 'num' }, num)); }

// tickTimers updates the call timers of a page without redrawing it.
function tickTimers(root) {
  const now = Date.now() / 1000 - S.clockSkew;
  $$('[data-since]', root).forEach((el) => { el.textContent = fmtDur(now - Number(el.dataset.since)); });
}

function callDirection(c) {
  if (c.a.trunk) return 'in';
  if ((c.b && c.b.trunk) || c.legs.some((x) => x.trunk)) return 'out';
  return 'internal';
}

// animPhase keeps a looping animation in step across redraws (the
// dashboard is redrawn every 2 s): a negative delay into its cycle.
const animPhase = (ms) => '--t:-' + Math.round(performance.now() % ms) + 'ms';

function activeCallsCard(calls) {
  const dirs = calls.map(callDirection);
  const n = (d) => dirs.filter((x) => x === d).length;
  return h('div', { class: 'panel active-calls' },
    h('div', { class: 'ac-head' },
      h('span', { class: 'title-dot ' + (calls.length ? 'green live' : ''), style: animPhase(2000) }), h('h2', null, 'Active Calls'), h('span', { class: 'spacer' }),
      h('span', { class: 'chip' }, h('b', null, String(calls.length)), 'Active'),
      h('span', { class: 'chip blue' }, h('b', null, String(n('in'))), 'Inbound'),
      h('span', { class: 'chip orange' }, h('b', null, String(n('out'))), 'Outbound'),
      S.me.database ? h('a', { class: 'link-btn', href: '#/cdr' }, 'Call history', icon('right', 14)) : null),
    h('div', { class: 'ac-body' }, calls.length ? callsTable(calls) : h('div', { class: 'ac-empty' },
      h('div', { class: 'ring-icon', style: animPhase(3600) }, h('span', null, icon('phone', 32))),
      h('h3', null, 'No calls in progress'),
      h('p', null, 'Live calls show up here the moment they start.'),
      S.me.database ? h('a', { class: 'btn', href: '#/cdr' }, icon('history', 16), 'View call history') : null)));
}

function devicesCard(l) {
  return h('div', { class: 'panel' },
    h('div', { class: 'panel-head' }, h('h2', null, 'Devices online'), h('span', { class: 'count-pill' }, String(l.registrations.length))),
    table([{ label: 'Extension', render: (r) => r.user + '@' + r.domain }, { label: 'Address', key: 'source' },
      { label: 'Device', key: 'user_agent', wrap: 1 }, { label: 'Since', render: (r) => fmtTime(r.since) }], l.registrations, { empty: 'No phones are signed in.' }));
}

function spark(values) {
  if (!values || values.length < 2) return null;
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('class', 'spark');
  svg.setAttribute('viewBox', '0 0 84 18');
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.setAttribute('aria-hidden', 'true');
  const max = 100;
  const pts = values.map((v, i) => (i * 84 / (values.length - 1)).toFixed(1) + ',' + (17 - Math.min(max, v) / max * 16).toFixed(1));
  const pl = document.createElementNS(ns, 'polyline');
  pl.setAttribute('points', pts.join(' '));
  svg.append(pl);
  return svg;
}

function meter(label, pct, sub, hist) {
  const level = pct >= 90 ? 'red' : pct >= 75 ? 'amber' : '';
  return h('div', { class: 'meter' },
    h('div', { class: 'meter-top' }, h('span', null, label), h('span', null, Math.round(pct) + '%')),
    h('div', { class: 'bar ' + level, role: 'meter', 'aria-valuemin': '0', 'aria-valuemax': '100', 'aria-valuenow': String(Math.round(pct)), 'aria-label': label },
      h('i', { style: 'width:' + Math.max(0, Math.min(100, pct)) + '%' })),
    h('div', { class: 'meter-sub' }, h('span', null, sub), spark(hist)));
}

const fmtNum = (v, d) => Number(v || 0).toLocaleString(undefined, { minimumFractionDigits: d || 0, maximumFractionDigits: d || 0 });

function healthCard(hl) {
  return h('div', { class: 'side-card' },
    h('div', { class: 'sc-head' }, h('h2', null, 'System Health'), h('span', { class: 'spacer' }),
      hl && hl.available ? h('span', { class: 'muted small', title: 'Load average (1 minute)' }, 'Load ' + fmtNum(hl.load1, 2)) : null),
    !hl || !hl.available ? h('p', { class: 'muted small' }, 'Not available on this system.') : [
      meter('CPU', hl.cpu_pct, hl.cpus + ' CPU(s)', hl.cpu_history),
      meter('Memory', hl.mem_pct, fmtNum(hl.mem_free_mb, 0) + ' MB free of ' + fmtNum(hl.mem_total_mb, 0), hl.mem_history),
      hl.disk_total_gb ? meter('Disk', hl.disk_pct, fmtNum(hl.disk_free_gb, 1) + ' GB free of ' + fmtNum(hl.disk_total_gb, 0), null) : null,
    ]);
}

function extensionsCard(l) {
  const ex = l.extensions || { total: 0, registered: 0 };
  const off = Math.max(0, ex.total - ex.registered);
  const pct = ex.total ? Math.min(100, ex.registered / ex.total * 100) : 0;
  return h('div', { class: 'side-card' },
    h('div', { class: 'sc-head' }, h('h2', null, 'Extensions'), h('span', { class: 'spacer' }), h('span', { class: 'muted small' }, ex.total + ' total')),
    h('div', { class: 'big-stat' }, h('b', null, String(ex.registered)), h('span', { class: 'muted' }, 'Registered')),
    h('div', { class: 'bar', role: 'meter', 'aria-valuemin': '0', 'aria-valuemax': String(ex.total), 'aria-valuenow': String(ex.registered), 'aria-label': 'Registered extensions' },
      h('i', { style: 'width:' + pct + '%' })),
    h('div', { class: 'meter-sub' }, h('span', null, h('span', { class: 'dot amber' }), off + ' Unregistered'),
      isOwner() && S.me.database ? h('a', { class: 'link-btn', href: '#/extensions' }, 'Manage', icon('right', 14)) : null));
}

function trunksCard(gws) {
  const up = gws.filter((g) => /REGED|UP|NOREG/i.test(g.state || ''));
  const alert = gws.length > 0 && up.length === 0;
  return h('div', { class: 'side-card' + (alert ? ' alert' : '') },
    h('div', { class: 'sc-head' }, h('h2', null, 'Trunks'), h('span', { class: 'spacer' }),
      gws.length ? h('span', { class: 'muted small' }, up.length + ' / ' + gws.length + ' connected') : null),
    !gws.length ? h('p', { class: 'muted small', style: 'margin:0' }, 'No trunks yet. ', S.me.database ? h('a', { href: '#/callflow' }, 'Add a SIP trunk') : null) :
      alert ? h('p', { class: 'small', style: 'margin:0 0 8px;color:var(--red)' }, 'No trunks registered') : null,
    gws.map((g) => h('div', { class: 'recent-row' }, icon('server', 16), h('span', { class: 'ellipsis grow' }, g.label || g.name),
      gatewayState(g.uuid || g.name, { enabled: 'true' }))));
}

function systemCard(l) {
  const st = l.stats;
  const row = (k, v) => [h('dt', null, k), h('dd', null, v)];
  return h('div', { class: 'side-card' },
    h('div', { class: 'sc-head' }, h('h2', null, 'System Information'), h('span', { class: 'spacer' }),
      S.me.server_access ? h('a', { class: 'link-btn', href: '#/system' }, 'Details', icon('right', 14)) : null),
    h('dl', { class: 'info-rows' },
      row('Version', S.me.brand + ' ' + st.version),
      row('Server', st.hostname || '—'),
      S.domain ? row('Domain', S.domain) : null,
      row('Uptime', fmtUptime(st.uptime_seconds)),
      row('Calls since start', fmtNum(st.sessions_total)),
      row('Live', h('span', null, h('span', { class: 'dot ' + (WS.open ? 'green' : 'red') }), WS.open ? 'Connected' : 'Reconnecting…'))));
}

function fmtUptime(s) {
  s = Number(s) || 0;
  const d = Math.floor(s / 86400), hh = Math.floor((s % 86400) / 3600), mm = Math.floor((s % 3600) / 60);
  return d ? `${d}d ${hh}h` : hh ? `${hh}h ${mm}m` : `${mm}m`;
}

function timeAgo(v) {
  let t = /^\d+$/.test(String(v)) ? Number(v) * (String(v).length > 11 ? 1 : 1000) : Date.parse(v);
  if (!t) return '';
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 60) return 'now';
  if (s < 3600) return Math.floor(s / 60) + 'm';
  if (s < 86400) return Math.floor(s / 3600) + 'h';
  return Math.floor(s / 86400) + 'd';
}

function recentCard(recent) {
  const failed = (s) => ['missed', 'failed', 'busy', 'cancelled', 'no answer', 'rejected'].includes(String(s || '').toLowerCase());
  return h('div', { class: 'side-card' },
    h('div', { class: 'sc-head' }, h('h2', null, 'Recent calls'), recent && recent.failed ? h('span', { class: 'pill-red' }, recent.failed + ' failed') : null,
      h('span', { class: 'spacer' }), h('a', { class: 'link-btn', href: '#/cdr' }, 'Details', icon('right', 14))),
    !recent ? h('p', { class: 'muted small' }, 'Loading…') : !recent.rows.length ? h('p', { class: 'muted small', style: 'margin:0' }, 'No calls today.') :
      recent.rows.map((r) => h('div', { class: 'recent-row' + (failed(r.status) ? ' failed' : ''), title: (r.status || '') + (Number(r.billsec) ? ' · ' + fmtDur(r.billsec) : '') },
        icon(r.direction === 'outbound' ? 'outgoing' : r.direction === 'local' ? 'phone' : 'incoming', 16),
        h('span', { class: 'ellipsis' }, (r.caller_id_number || '') + ' → ' + (r.caller_destination || r.destination_number || '')),
        h('span', { class: 'ago' }, timeAgo(r.start_stamp)))));
}

// groupCalls turns channels (legs) into calls: one row per call, with the
// leg that was dialled (B leg) shown as the destination.
function groupCalls(chans) {
  const byUUID = Object.fromEntries(chans.map((c) => [c.uuid, c]));
  const calls = [];
  for (const a of chans) {
    if (a.call_uuid && a.call_uuid !== a.uuid && byUUID[a.call_uuid]) continue; // a B leg: shown with its A leg
    const legs = chans.filter((c) => c.uuid !== a.uuid && c.call_uuid === a.uuid);
    const partner = a.bridged_to ? byUUID[a.bridged_to] : null;
    const b = partner || legs.find((x) => x.answered_epoch) || legs[0] || null;
    const talking = !!(a.answered_epoch && partner);
    let state = 'Ringing';
    if (talking) state = 'Talking';
    else if (a.answered_epoch) state = ({ callcenter: 'In queue', conference: 'Conference', voicemail: 'Voicemail', ivr: 'IVR menu', valet_park: 'Parked', playback: 'Announcement', bridge: 'Ringing' })[a.application] || (a.application || 'Answered');
    calls.push({ a, b, legs, talking, state, to: b ? (b.dest || b.callee_num || a.dest) : (a.dest || a.callee_num || ''),
      toName: b ? (b.cid_name && b.cid_name !== a.cid_name ? '' : b.callee_name || '') : (a.callee_name || ''),
      since: Number((talking && (partner.answered_epoch || a.answered_epoch)) || a.created_epoch) });
  }
  return calls.sort((x, y) => x.since - y.since);
}

function callsTable(calls) {
  const now = Date.now() / 1000 - S.clockSkew;
  const act = async (c, action, body) => {
    try { await api('POST', 'calls/' + c.a.uuid + '/' + action, body || {}); toast({ hangup: 'Call ended', transfer: 'Call diverted' }[action] || 'Calling your extension to ' + action + '…'); } catch (e) { fail(e); }
  };
  const monitor = (c, mode) => {
    let ext = S.me.extension;
    if (!ext) {
      ext = prompt('Which extension should ring so you can ' + mode + '?');
      if (!ext) return;
    }
    act(c, mode, { extension: ext });
  };
  const dirLabel = { in: 'Inbound', out: 'Outbound', internal: 'Internal' };
  const cols = [
    { label: 'From', render: (c) => {
      const d = callDirection(c);
      return h('span', { class: 'call-from' }, h('span', { class: 'call-dir ' + d, title: dirLabel[d] }, icon(d === 'in' ? 'incoming' : d === 'out' ? 'outgoing' : 'phone', 14)),
        (c.a.cid_name && c.a.cid_name !== c.a.cid_num ? c.a.cid_name + ' ' : '') + c.a.cid_num);
    } },
    { label: 'To', render: (c) => {
      // The dialled leg carries the caller's name as its "callee"; show a name only when it is a different one.
      const name = c.toName && c.toName !== c.a.cid_num && c.toName !== c.a.cid_name && c.toName !== c.to ? c.toName : '';
      return (c.to + ' ' + name).trim();
    } },
    { label: 'State', render: (c) => badge(c.state, c.talking ? 'green' : c.state === 'Ringing' ? 'amber' : 'blue') },
    { label: 'Time', render: (c) => h('span', { 'data-since': c.since }, fmtDur(now - c.since)), num: 1 },
  ];
  if (isSupervisor()) {
    const b = (ic, label, title, fn, cls) => h('button', { class: 'btn small ' + (cls || ''), title, onclick: fn }, icon(ic, 14), label);
    cols.push({ label: '', actions: 1, render: (c) => h('div', { class: 'btn-group' },
      c.talking ? [
        b('ear', 'Listen', 'Hear both sides; nobody hears you', () => monitor(c, 'listen')),
        b('whisper', 'Whisper', 'Talk to your agent only; the caller does not hear you', () => monitor(c, 'whisper')),
        b('barge', 'Barge in', 'Join the call: everybody hears everybody', () => monitor(c, 'barge')),
      ] : null,
      b('transfer', 'Divert', 'Send the caller to another number', () => {
        const n = prompt('Divert the caller (' + c.a.cid_num + ') to which number?');
        if (n) act(c, 'transfer', { destination: n.trim() });
      }),
      b('hangup', 'Hang up', 'End the call', () => { if (confirm('Hang up this call?')) act(c, 'hangup'); }, 'danger')) });
  }
  return table(cols, calls, { empty: 'No calls right now.' });
}

PAGES.callcentre = async () => {
  const main = $('#main');
  await loadMeta();
  const auxCodes = S.meta ? S.meta.auxcodes : [];
  let last = null;
  const setStatus = async (a, status, aux) => {
    try { await api('POST', 'agents/' + a.uuid + '/status', { status, aux: aux || '' }); toast(a.name + ': ' + (aux || status)); } catch (e) { fail(e); }
  };
  const agentActions = (a) => {
    if (!isSupervisor() && a.extension !== S.me.extension) return '';
    const sel = h('select', { title: 'Put on break with a reason' }, h('option', { value: '' }, 'Break…'),
      auxCodes.map((x) => h('option', { value: x.name }, x.name + (truthy(x.block_direct_calls) ? ' (no direct calls)' : ''))));
    sel.addEventListener('change', () => { if (sel.value) setStatus(a, 'On Break', sel.value); });
    return h('div', { class: 'btn-group' },
      a.status !== 'Available' ? h('button', { class: 'btn small', onclick: () => setStatus(a, 'Available') }, 'Available') : null,
      sel,
      a.status !== 'Logged Out' ? h('button', { class: 'btn small', onclick: () => setStatus(a, 'Logged Out') }, 'Log out') : null);
  };
  const render = (l) => { last = l; draw(); };
  const draw = () => {
    if (!last) return;
    const l = last;
    const now = Date.now() / 1000 - S.clockSkew;
    const focused = document.activeElement && document.activeElement.tagName === 'SELECT' && main.contains(document.activeElement);
    if (focused) return; // do not close a menu the user has open
    fill(main, 
      pageHead('Call centre', h('span', { class: 'muted small' }, h('span', { class: 'dot ' + (WS.open ? 'green' : 'red') }), WS.open ? 'Live' : 'Reconnecting…')),
      h('p', { class: 'page-intro' }, 'Agents on a break (AUX code) or logged out receive no queue calls. Agents can also use *22 (log in / out) and *23 (break on / off) on their phone. Every change is recorded in Reports → Agent status log.'),
      l.queues.length ? h('div', { class: 'queue-cards' }, l.queues.map((q) => h('div', { class: 'card queue-card' },
        h('h2', null, h('span', null, q.name + ' (' + q.extension + ')'), h('span', { class: 'muted small' }, q.strategy)),
        h('div', { class: 'kv' },
          kv(q.waiting, 'Waiting'), kv(q.waiting ? fmtDur(q.longest_wait) : '–', 'Longest wait'), kv(q.available + ' / ' + q.logged_in, 'Free / logged in'),
          kv(q.on_call, 'On a call'), kv(q.on_break, 'On break'), kv(q.answered_today, 'Answered today'),
          kv(q.abandoned_today, 'Abandoned today'), kv(fmtDur(q.avg_wait), 'Avg wait'), kv(fmtDur(q.avg_talk), 'Avg talk')),
        q.callers && q.callers.length ? h('div', { class: 'small', style: 'margin-top:8px' }, q.callers.map((c) => h('div', null,
          h('span', { class: 'dot ' + (c.agent ? 'green' : 'amber') }), (c.cid_name || '') + ' ' + c.cid_number + ' · ' + fmtDur(now - c.joined) + (c.agent ? ' · with ' + c.agent : '')))) : null))) :
        h('div', { class: 'panel' }, 'No queues yet. Create one under Call centre → Queues.'),
      h('h2', null, 'Agents'),
      table([
        { label: 'Agent', render: (a) => a.name },
        { label: 'Ext', key: 'extension' },
        { label: 'Status', render: (a) => statusBadge(a.status, a.aux) },
        { label: 'For', render: (a) => fmtDur(now - a.status_since), num: 1 },
        { label: 'Activity', render: (a) => a.caller ? badge('Queue call: ' + a.caller, 'blue') : a.on_call ? badge('On a call', 'blue') : a.ready_in > 0 ? badge('Wrap-up ' + a.ready_in + 's') : a.state },
        { label: 'Answered', key: 'calls_answered', num: 1 },
        { label: 'Talk', render: (a) => fmtDur(a.talk_seconds), num: 1 },
        { label: 'Missed', key: 'no_answer_count', num: 1 },
        { label: 'Queues', render: (a) => (a.queues || []).join(', '), wrap: 1 },
        { label: 'Change status', actions: 1, render: agentActions },
      ], l.agents, { empty: 'No agents yet. Create them under Call centre → Agents and add them to a queue.' }));
  };
  liveOn(render);
  every(1000, draw); // timers tick
};
function kv(v, l) { return h('div', null, h('b', null, v), h('span', null, l)); }

PAGES.logs = async () => {
  const main = $('#main');
  const kinds = [['', 'Everything'], ['agent', 'Agent status'], ['call', 'Calls'], ['queue', 'Queues'], ['registration', 'Phones'], ['gateway', 'Gateways'], ['security', 'Security'], ['system', 'System']];
  let kind = '';
  const search = h('input', { placeholder: 'Search…', type: 'search' });
  const auto = h('input', { type: 'checkbox', checked: true });
  const tabs = h('div', { class: 'tabs' });
  const out = h('div');
  const colors = { agent: 'amber', call: 'blue', queue: 'blue', registration: 'green', gateway: 'green', security: 'red', system: '' };
  const load = async () => {
    const rows = await api('GET', 'logs?limit=1000&kind=' + kind + '&q=' + encodeURIComponent(search.value));
    fill(out, table([
      { label: 'Time', render: (r) => new Date(r.time).toLocaleString() },
      { label: 'Type', render: (r) => badge(r.kind, colors[r.kind]) },
      { label: 'What happened', key: 'text', wrap: 1 }], rows, { empty: 'Nothing logged yet.' }));
  };
  const drawTabs = () => tabs.replaceChildren(...kinds.map(([k, l]) => h('button', { class: k === kind ? 'on' : '', onclick: () => { kind = k; drawTabs(); load(); } }, l)));
  drawTabs();
  search.addEventListener('input', () => load());
  fill(main, pageHead('Event log', h('label', { class: 'switch' }, auto, 'Live')),
    h('p', { class: 'page-intro' }, 'Recent activity since the switch started (the newest first): agent status and AUX changes, calls, queue results, phones signing in, gateways and blocked attackers. Permanent history is in Reports.'),
    tabs, h('div', { class: 'filters' }, search), out);
  await load();
  every(3000, () => { if (auto.checked) load().catch(() => {}); });
};

PAGES.voicemail = async () => {
  const main = $('#main');
  await loadMeta();
  const box = selectOf(extOptions(), '', 'All boxes');
  const out = h('div');
  const pager = h('div');
  const limit = 50;
  let offset = 0;
  let rows = [];
  const draw = () => {
    offset = Math.min(offset, Math.max(0, rows.length - 1) - (Math.max(0, rows.length - 1) % limit));
    fill(out, table([
      { label: 'Received', render: (r) => h('span', { title: fmtTime(r.created) }, fmtShort(r.created)) },
      { label: 'Box', key: 'box' },
      { label: 'From', render: (r) => h('span', { class: 'cell-clip', title: party(r.caller_id_name, r.caller_id_number) }, party(r.caller_id_name, r.caller_id_number)) },
      { label: 'Length', render: (r) => fmtDur(r.length), num: 1 },
      { label: 'Status', render: (r) => r.status === 'saved' ? badge('Heard') : badge('New', 'green') },
      { label: 'Message', wrap: 1, render: (r) => vmTextCell(r, load) },
      { label: 'Sentiment', render: (r) => sentimentBadge(r.sentiment, r.sentiment_score) },
      { label: '', actions: 1, render: (r) => vmIcons(r, load) },
    ], rows.slice(offset, offset + limit), { empty: 'No voicemail messages.' }));
    fill(pager, pagerBar(rows.length, offset, limit, (o) => { offset = Math.max(0, o); draw(); }));
  };
  const load = async () => {
    rows = await api('GET', dpath('voicemail-messages?box=' + encodeURIComponent(box.value)));
    if (currentPage() === 'voicemail') draw();
  };
  TR.onUpdate = () => { if (currentPage() === 'voicemail') load().catch(() => {}); };
  box.addEventListener('change', () => { offset = 0; load().catch(fail); });
  fill(main, pageHead('Voicemail messages'),
    h('p', { class: 'page-intro' }, 'Messages callers left. Phones show a message light; dialling *97 beeps once per new message. Turn voicemail on per extension under Extensions. ' +
      'With voicemail transcription on (Settings → Call transcription), each message is written down: click its text to read it all.'),
    isOwner() ? h('div', { class: 'filters' }, h('label', null, 'Box', box)) : null, out, pager);
  await load();
};

// vmIcons: play (stop while playing), download and delete a voicemail
// message, as icons; playing marks it heard.
function vmIcons(r, reload) {
  const del = h('button', { class: 'icon-btn rec-btn danger', type: 'button', title: 'Delete the message', 'aria-label': 'Delete the message', onclick: async (e) => {
    e.stopPropagation();
    if (!confirm('Delete this message?')) return;
    try { await api('DELETE', dpath('voicemail-messages/' + r.uuid)); toast('Deleted'); reload(); } catch (err) { fail(err); }
  } }, icon('trash', 16));
  const icons = recordingIcons(dpath('voicemail-messages/' + r.uuid + '/audio'), () => vmHeard(r));
  icons.append(del);
  return icons;
}
function vmHeard(r) {
  if (r.status === 'saved') return;
  r.status = 'saved';
  api('POST', dpath('voicemail-messages/' + r.uuid + '/read')).catch(() => {});
}

function playButton(path, onPlay) {
  const wrap = h('span', { class: 'btn-group' });
  const url = '/api/' + path;
  const dl = h('a', { class: 'btn small', href: url + (path.includes('?') ? '&' : '?') + 'download=1', title: 'Download' }, '⤓ Download');
  const showButton = () => fill(wrap, h('button', { class: 'btn small', onclick: (ev) => {
    ev.stopPropagation();
    const audio = h('audio', { controls: true, autoplay: true, preload: 'none', src: url });
    audio.addEventListener('ended', showButton);
    audio.addEventListener('error', showButton);
    fill(wrap, audio, h('button', { class: 'btn small', title: 'Stop', onclick: (e) => { e.stopPropagation(); audio.pause(); showButton(); } }, '■'), dl);
    if (onPlay) onPlay();
  } }, '▶ Play'), dl);
  showButton();
  return wrap;
}

function dateFilters(onChange, extra) {
  const from = h('input', { type: 'date', value: '', disabled: true });
  const to = h('input', { type: 'date', value: '', disabled: true });
  const quick = h('select', null, [['all', 'All'], ['0', 'Today'], ['1', 'Yesterday'], ['7', 'Last 7 days'], ['30', 'Last 30 days'], ['c', 'Custom']].map(([v, l]) => h('option', { value: v }, l)));
  const set = (a, b) => { from.value = a; to.value = b; from.disabled = to.disabled = quick.value === 'all'; };
  quick.addEventListener('change', () => {
    const v = quick.value;
    if (v === 'all') set('', '');
    else if (v === '1') set(daysAgo(1), daysAgo(1));
    else if (v === '7') set(daysAgo(6), today());
    else if (v === '30') set(daysAgo(29), today());
    else if (v === '0') set(today(), today());
    else set(from.value || today(), to.value || today());
    onChange();
  });
  from.addEventListener('change', () => { quick.value = 'c'; onChange(); });
  to.addEventListener('change', () => { quick.value = 'c'; onChange(); });
  const el = h('div', { class: 'filters' }, h('label', null, 'Period', quick), h('label', null, 'From', from), h('label', null, 'To', to), extra || []);
  const all = () => quick.value === 'all';
  return { el, qs: () => (all() ? 'all=1' : 'from=' + from.value + '&to=' + to.value), name: () => (all() ? 'all' : from.value + '_' + to.value) };
}

// pagerBar is the row under a paged table: "Showing 51–100 of 230",
// Previous, page numbers, Next. go(offset) shows another page.
function pagerBar(total, offset, limit, go) {
  if (total <= limit) return h('div', { class: 'pager' }, total ? h('span', { class: 'muted' }, total + (total === 1 ? ' row' : ' rows')) : null);
  const page = Math.floor(offset / limit), pages = Math.ceil(total / limit);
  const nums = [];
  for (let i = Math.max(0, page - 2); i < Math.min(pages, page + 3); i++) {
    nums.push(h('button', { class: 'page-num' + (i === page ? ' on' : ''), type: 'button', onclick: () => go(i * limit) }, String(i + 1)));
  }
  return h('div', { class: 'pager' }, h('span', { class: 'muted' }, 'Showing ' + (offset + 1) + '–' + Math.min(total, offset + limit) + ' of ' + total),
    h('span', { class: 'spacer' }),
    h('button', { class: 'btn small', type: 'button', disabled: page === 0, onclick: () => go(offset - limit) }, 'Previous'), nums,
    h('button', { class: 'btn small', type: 'button', disabled: page >= pages - 1, onclick: () => go(offset + limit) }, 'Next'));
}

// recordingIcons: play (stop while playing) and download, as icons.
let REC_PLAYING = null;
function recordingIcons(path, onPlay) {
  const url = '/api/' + path;
  const play = h('button', { class: 'icon-btn rec-btn', type: 'button', title: 'Play the recording', 'aria-label': 'Play the recording' }, icon('play', 16));
  const dl = h('a', { class: 'icon-btn rec-btn', href: url + (path.includes('?') ? '&' : '?') + 'download=1', title: 'Download the recording',
    'aria-label': 'Download the recording', onclick: (e) => e.stopPropagation() }, icon('download', 16));
  let audio = null;
  const show = (on) => { fill(play, icon(on ? 'square' : 'play', 16)); play.title = on ? 'Stop' : 'Play the recording'; play.classList.toggle('on', on); };
  play.addEventListener('click', (ev) => {
    ev.stopPropagation();
    if (audio && !audio.paused) { audio.pause(); return; }
    if (REC_PLAYING && REC_PLAYING !== audio) REC_PLAYING.pause();
    if (!audio) {
      audio = new Audio(url);
      audio.addEventListener('playing', () => show(true));
      audio.addEventListener('pause', () => { show(false); audio.currentTime = 0; });
      audio.addEventListener('ended', () => show(false));
      audio.addEventListener('error', () => { show(false); toast('The recording cannot be played', true); });
    }
    REC_PLAYING = audio;
    audio.play().catch(() => {});
    if (onPlay) onPlay();
  });
  return h('span', { class: 'rec-icons' }, play, dl);
}

// The call's path, shown under its row in Call history.
const FLOW_DOT = { start: 'blue hollow', route: 'blue small', ivr: 'blue small', digits: 'blue small', queue: 'blue small', ring_group: 'blue small',
  ring: 'amber hollow', no_answer: 'red', answer: 'green', voicemail: 'blue', conference: 'blue', ai: 'violet', hangup: 'grey hollow', end: 'dark' };
async function callFlowRow(r, tr) {
  const next = tr.nextElementSibling;
  if (next && next.classList.contains('flow-row')) { next.remove(); tr.classList.remove('open'); return; }
  const cell = h('td', { colspan: String(tr.children.length) }, h('div', { class: 'muted small flow-wait' }, 'Loading…'));
  const row = h('tr', { class: 'flow-row' }, cell);
  tr.after(row);
  tr.classList.add('open');
  try {
    const data = await api('GET', dpath('cdr/' + r.xml_cdr_uuid + '/flow'));
    const t = (ms) => new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    fill(cell, h('div', { class: 'flow' }, (data.steps || []).map((st) => h('div', { class: 'flow-step k-' + st.k },
      h('span', { class: 'flow-time' }, t(st.t)), h('span', { class: 'flow-dot ' + (FLOW_DOT[st.k] || 'blue small') }), h('span', { class: 'flow-text' }, st.x))),
    data.kept ? null : h('div', { class: 'muted small flow-note' }, 'Rebuilt from the call record (calls before 0.6.1 or written by the previous switch keep fewer details).')));
  } catch (e) { fill(cell, h('div', { class: 'muted small' }, 'The call\'s path cannot be shown: ' + e.message)); }
}

PAGES.cdr = async () => {
  const main = $('#main');
  let offset = 0;
  const limit = 50;
  const search = h('input', { type: 'search', placeholder: 'Number or name' });
  const dir = selectOf([['inbound', 'Incoming'], ['outbound', 'Outgoing'], ['local', 'Internal']], '', 'All');
  const status = selectOf([['answered', 'Answered'], ['missed', 'Missed'], ['voicemail', 'Voicemail'], ['busy', 'Busy'], ['cancelled', 'Cancelled'], ['failed', 'Failed']], '', 'All');
  const out = h('div');
  const pager = h('div');
  const query = () => dpath('cdr?' + df.qs() + '&q=' + encodeURIComponent(search.value) + '&direction=' + dir.value + '&status=' + status.value);
  const DIR = { inbound: ['incoming', 'Incoming'], outbound: ['outgoing', 'Outgoing'], local: ['phone', 'Internal'] };
  const shortQueue = (q) => (q || '').split('@')[0];
  const summary = (r) => [r.answered_by ? 'Answered by ' + r.answered_by : '', r.ended_by ? (r.ended_by === 'the system' ? 'ended by the system' : r.ended_by + ' hung up') : ''].filter(Boolean).join(' · ');
  const load = async () => {
    const res = await api('GET', query() + '&limit=' + limit + '&offset=' + offset);
    fill(out, table([
      { label: '', render: () => h('span', { class: 'flow-chev', 'aria-hidden': 'true' }, icon('right', 14)) },
      { label: 'Started', render: (r) => h('span', { title: fmtTime(r.start_stamp) }, fmtShort(r.start_stamp)) },
      { label: '', render: (r) => { const d = DIR[r.direction]; return d ? h('span', { class: 'dir-ic d-' + r.direction, title: d[1], 'aria-label': d[1] }, icon(d[0], 15)) : (r.direction || ''); } },
      { label: 'From', render: (r) => h('span', { class: 'cell-clip', title: party(r.caller_id_name, r.caller_id_number) }, party(r.caller_id_name, r.caller_id_number)) },
      { label: 'To', render: (r) => r.caller_destination || r.destination_number },
      { label: 'Result', render: (r) => h('div', { class: 'res-cell' }, badge(r.status || r.hangup_cause, { answered: 'green', missed: 'red', voicemail: 'blue', busy: 'amber' }[r.status]),
        summary(r) ? h('div', { class: 'muted small res-sum', title: summary(r) }, summary(r)) : null) },
      { label: 'Wait', render: (r) => r.waitsec !== undefined && r.waitsec !== '' ? fmtDur(r.waitsec) : '', num: 1 },
      { label: 'Talk', render: (r) => fmtDur(r.talksec !== undefined ? r.talksec : r.billsec), num: 1 },
      { label: 'Queue', render: (r) => (r.cc_queue || r.cc_agent ? h('div', { class: 'q-cell', title: [r.cc_queue, r.cc_agent].filter(Boolean).join(' / ') },
        r.cc_queue ? h('div', { class: 'cell-clip' }, shortQueue(r.cc_queue)) : null, r.cc_agent ? h('div', { class: 'muted small cell-clip' }, r.cc_agent) : null) : '') },
      { label: 'Recording', render: (r) => r.record_name ? recordingIcons(dpath('cdr/' + r.xml_cdr_uuid + '/recording')) : '' },
      { label: 'Transcript', render: (r) => trCell(r, r.xml_cdr_uuid, callTitle(r), isSupervisor(), load) },
      { label: 'Sentiment', render: (r) => sentimentBadge(r.sentiment, r.sentiment_score) },
    ], res.rows, { empty: 'No calls in this period.', click: (r, tr, ev) => { if (!ev.target.closest('button, a, audio')) callFlowRow(r, tr); } }));
    const t = out.querySelector('table');
    if (t) t.classList.add('cdr-table');
    fill(pager, pagerBar(res.total, offset, limit, (o) => { offset = Math.max(0, o); load().catch(fail); }));
  };
  const reset = () => { offset = 0; load().catch(fail); };
  TR.onUpdate = () => { if (currentPage() === 'cdr') load().catch(() => {}); };
  const df = dateFilters(reset, [h('label', null, 'Search', search), h('label', null, 'Direction', dir), h('label', null, 'Result', status)]);
  search.addEventListener('change', reset);
  dir.addEventListener('change', reset);
  status.addEventListener('change', reset);
  const exportCSV = async () => {
    try {
      const res = await api('GET', query() + '&limit=10000&offset=0');
      downloadCSV('calls_' + df.name() + '.csv',
        ['Started', 'Direction', 'From name', 'From', 'To', 'Result', 'Answered by', 'Ended by', 'Wait', 'Talk', 'Queue', 'Agent', 'Hangup cause', 'Transcript', 'Sentiment'],
        res.rows.map((r) => [r.start_stamp, r.direction, r.caller_id_name, r.caller_id_number, r.caller_destination || r.destination_number, r.status, r.answered_by || '', r.ended_by || '',
          r.waitsec, r.talksec !== undefined ? r.talksec : r.billsec, r.cc_queue, r.cc_agent, r.hangup_cause, r.transcript || '', r.sentiment ? r.sentiment + ' ' + trScore(r.sentiment_score) : '']));
    } catch (e) { fail(e); }
  };
  fill(main, pageHead('Call history', h('button', { class: 'btn', onclick: exportCSV }, 'Export CSV')),
    h('p', { class: 'page-intro' }, 'Click a call to see its path: where it came from, the menus and queues it went through, who answered and who hung up.'),
    df.el, out, pager);
  await load();
};

// callTitle describes a call for its transcript window.
function callTitle(r) {
  const from = party(r.caller_id_name, r.caller_id_number);
  return from + ' → ' + (r.caller_destination || r.destination_number || r.agent_name || '') + ' · ' + fmtTime(r.start_stamp || r.joined_at);
}

// Reports
function reportPage(title, intro, kind, cols, csv, extraFilter) {
  return async () => {
    const main = $('#main');
    await loadMeta();
    const out = h('div');
    const pager = h('div');
    const limit = 50;
    let offset = 0;
    let rows = [];
    // The lists are paged by the server, the summaries (a row per queue or
    // agent) here.
    const serverPaged = kind === 'agent-log' || kind === 'queue-calls';
    const extra = extraFilter ? extraFilter() : null;
    const url = () => dpath('reports/' + kind + '?' + df.qs() + (extra ? extra.qs() : ''));
    const load = async () => {
      let total, shown;
      if (serverPaged) {
        const res = await api('GET', url() + '&limit=' + limit + '&offset=' + offset);
        rows = res.rows || [];
        total = res.total || 0;
        shown = rows;
      } else {
        rows = await api('GET', url());
        total = rows.length;
        offset = Math.min(offset, Math.max(0, total - 1) - (Math.max(0, total - 1) % limit));
        shown = rows.slice(offset, offset + limit);
      }
      const c = typeof cols === 'function' ? cols(rows, load) : cols;
      fill(out, table(c, shown, { empty: 'No data for this period.' }));
      fill(pager, pagerBar(total, offset, limit, (o) => { offset = Math.max(0, o); load().catch(fail); }));
    };
    const reset = () => { offset = 0; load().catch(fail); };
    TR.onUpdate = () => { if (currentPage() === 'rep-' + kind) load().catch(() => {}); };
    const df = dateFilters(reset, extra ? extra.el : null);
    if (extra) extra.onChange(reset);
    const exportCSV = async () => {
      try {
        const all = serverPaged ? (await api('GET', url())) : rows;
        const c = typeof cols === 'function' ? cols(all) : cols;
        downloadCSV(kind + '_' + df.name() + '.csv', c.map((x) => x.label), all.map((r) => c.map((x) => x.csv ? x.csv(r) : (x.key ? r[x.key] : ''))));
      } catch (e) { fail(e); }
    };
    fill(main, pageHead(title, h('button', { class: 'btn', onclick: exportCSV }, 'Export CSV')), h('p', { class: 'page-intro' }, intro), df.el, out, pager);
    await load();
  };
}

function agentFilter() {
  const sel = selectOf(agentOptions(), '', 'All agents');
  return { el: h('label', null, 'Agent', sel), qs: () => '&agent=' + sel.value, onChange: (fn) => sel.addEventListener('change', fn) };
}
function queueFilter() {
  const sel = h('select', null, h('option', { value: '' }, 'All queues'));
  api('GET', dpath('queues')).then((rows) => rows.forEach((q) => sel.append(h('option', { value: q.call_center_queue_uuid }, q.queue_extension + ' ' + q.queue_name)))).catch(() => {});
  return { el: h('label', null, 'Queue', sel), qs: () => '&queue=' + sel.value, onChange: (fn) => sel.addEventListener('change', fn) };
}

PAGES['rep-agent-status'] = reportPage('Agent status times',
  'How long each agent spent available, on each break / AUX code and logged in during the period (the status they are in now is counted up to this moment).',
  'agent-status', (rows) => {
    const auxKeys = Array.from(new Set(rows.flatMap((r) => Object.keys(r.seconds).filter((k) => k.startsWith('On Break: '))))).sort();
    return [
      { label: 'Agent', key: 'agent_name' }, { label: 'Ext', key: 'extension' },
      { label: 'Now', render: (r) => r.current, csv: (r) => r.current },
      { label: 'Logged in', render: (r) => fmtDur(r.logged_in_seconds), csv: (r) => fmtDur(r.logged_in_seconds), num: 1 },
      { label: 'Available', render: (r) => fmtDur(r.available_seconds), csv: (r) => fmtDur(r.available_seconds), num: 1 },
      { label: 'On break (all)', render: (r) => fmtDur(r.break_seconds), csv: (r) => fmtDur(r.break_seconds), num: 1 },
      ...auxKeys.map((k) => ({ label: k.replace('On Break: ', ''), num: 1,
        render: (r) => r.seconds[k] ? fmtDur(r.seconds[k]) + (r.breaks[k.replace('On Break: ', '')] ? ' (' + r.breaks[k.replace('On Break: ', '')] + '×)' : '') : '',
        csv: (r) => r.seconds[k] ? fmtDur(r.seconds[k]) : '' })),
      { label: 'Status changes', key: 'changes', num: 1 },
    ];
  });

PAGES['rep-agent-log'] = reportPage('Agent status log',
  'Every status and AUX code change, newest first: who, when, the new status, the previous status and how long it lasted, and where the change came from (web, phone *22/*23, automatic after missed calls…).',
  'agent-log', [
    { label: 'Time', render: (r) => h('span', { title: fmtTime(r.time) }, fmtShort(r.time)), csv: (r) => r.time },
    { label: 'Agent', key: 'agent_name' }, { label: 'Ext', key: 'extension' },
    { label: 'New status', render: (r) => statusBadge(r.status, r.aux_code), csv: (r) => r.status + (r.aux_code ? ': ' + r.aux_code : '') },
    { label: 'Previous status', render: (r) => r.previous_status ? statusBadge(r.previous_status, r.previous_aux_code) : '', csv: (r) => r.previous_status + (r.previous_aux_code ? ': ' + r.previous_aux_code : '') },
    { label: 'Previous lasted', render: (r) => fmtDur(r.previous_seconds), csv: (r) => fmtDur(r.previous_seconds), num: 1 },
    { label: 'Changed by', key: 'source' },
  ], null, agentFilter);

PAGES['rep-queues'] = reportPage('Queue report',
  'Per queue: calls offered, answered, abandoned (caller hung up) and timed out, waiting and talk times, and the service level (answered within 20 seconds).',
  'queues', [
    { label: 'Queue', render: (r) => r.queue_extension + ' ' + r.queue_name, csv: (r) => r.queue_extension + ' ' + r.queue_name },
    { label: 'Offered', key: 'offered', num: 1 }, { label: 'Answered', key: 'answered', num: 1 },
    { label: 'Abandoned', key: 'abandoned', num: 1 }, { label: 'Timed out', key: 'timeout', num: 1 }, { label: 'Left by key / transfer', key: 'exited', num: 1 },
    { label: 'Answer rate', render: (r) => r.offered ? Math.round(100 * r.answered / r.offered) + '%' : '', csv: (r) => r.offered ? Math.round(100 * r.answered / r.offered) + '%' : '', num: 1 },
    { label: 'Service level (20s)', render: (r) => r.offered ? Math.round(100 * r.in_sla / r.offered) + '%' : '', csv: (r) => r.offered ? Math.round(100 * r.in_sla / r.offered) + '%' : '', num: 1 },
    { label: 'Avg wait', render: (r) => fmtDur(r.avg_wait), csv: (r) => fmtDur(r.avg_wait), num: 1 },
    { label: 'Longest wait', render: (r) => fmtDur(r.max_wait), csv: (r) => fmtDur(r.max_wait), num: 1 },
    { label: 'Avg talk', render: (r) => fmtDur(r.avg_talk), csv: (r) => fmtDur(r.avg_talk), num: 1 },
    { label: 'Avg wait before hang-up', render: (r) => fmtDur(r.avg_abandon_wait), csv: (r) => fmtDur(r.avg_abandon_wait), num: 1 },
  ]);

PAGES['rep-agents'] = reportPage('Agent performance', 'Queue calls each agent answered, with talk and waiting times. Break times are in "Agent status times".',
  'agents', [
    { label: 'Agent', key: 'agent_name' }, { label: 'Ext', key: 'extension' },
    { label: 'Calls answered', key: 'answered', num: 1 },
    { label: 'Talk time', render: (r) => fmtDur(r.talk_total), csv: (r) => fmtDur(r.talk_total), num: 1 },
    { label: 'Avg talk', render: (r) => fmtDur(r.avg_talk), csv: (r) => fmtDur(r.avg_talk), num: 1 },
    { label: 'Avg caller wait', render: (r) => fmtDur(r.avg_wait), csv: (r) => fmtDur(r.avg_wait), num: 1 },
    { label: 'Queues', key: 'queues', wrap: 1 },
  ]);

PAGES['rep-queue-calls'] = reportPage('Queue calls', 'Every queue call with its result.',
  'queue-calls', (rows, load) => [
    { label: 'Joined', render: (r) => h('span', { title: fmtTime(r.joined_at) }, fmtShort(r.joined_at)), csv: (r) => r.joined_at },
    { label: 'Queue', render: (r) => r.queue_extension + ' ' + (r.queue_name || ''), csv: (r) => r.queue_extension + ' ' + (r.queue_name || '') },
    { label: 'Caller', render: (r) => party(r.caller_id_name, r.caller_id_number), csv: (r) => party(r.caller_id_name, r.caller_id_number) },
    { label: 'Result', render: (r) => badge(r.result, { answered: 'green', abandoned: 'red', timeout: 'amber', no_agent_timeout: 'amber' }[r.result]), csv: (r) => r.result },
    { label: 'Wait', render: (r) => fmtDur(r.wait_seconds), csv: (r) => r.wait_seconds, num: 1 },
    { label: 'Talk', render: (r) => fmtDur(r.talk_seconds), csv: (r) => r.talk_seconds, num: 1 },
    { label: 'Agent', render: (r) => party(r.agent_name, r.agent_extension), csv: (r) => r.agent_name },
    { label: 'Agents tried', key: 'agents_offered', num: 1 },
    { label: 'Transcript', render: (r) => (r.call_uuid ? trCell(r, r.call_uuid, callTitle(r), false, load) : ''), csv: (r) => r.transcript || '' },
    { label: 'Sentiment', render: (r) => sentimentBadge(r.sentiment, r.sentiment_score), csv: (r) => (r.sentiment ? r.sentiment + ' ' + trScore(r.sentiment_score) : '') },
  ], null, queueFilter);

PAGES.sounds = async () => {
  const main = $('#main');
  const data = await api('GET', 'sounds');
  const upload = async (path, file) => {
    if (!file) { toast('Choose a WAV file first', true); return; }
    try {
      const buf = await file.arrayBuffer();
      const r = await api('POST', 'sounds?path=' + encodeURIComponent(path), buf, true);
      toast('Uploaded (' + r.seconds + ' s)');
      await loadMeta();
      PAGES.sounds();
    } catch (e) { fail(e); }
  };
  const del = async (path) => {
    if (!confirm('Delete ' + path + '?')) return;
    try { await api('DELETE', 'sounds?path=' + encodeURIComponent(path)); toast('Deleted'); PAGES.sounds(); } catch (e) { fail(e); }
  };
  const pName = h('input', { placeholder: 'e.g. welcome' });
  const pFile = h('input', { type: 'file', accept: '.wav,audio/wav' });
  const folders = Object.keys(data.music).sort();
  const mFolder = h('input', { value: 'default', list: 'music-folders' });
  const mFile = h('input', { type: 'file', accept: '.wav,audio/wav' });
  const fileName = (f) => (f ? f.name.replace(/\.wav$/i, '').replace(/[^A-Za-z0-9 ._-]/g, '_') : '');
  fill(main, pageHead('Sounds & music'),
    h('div', { class: 'help-box' }, 'Upload recordings as ', h('b', null, 'WAV'), ' files (any quality — they are converted for phones automatically). ',
      'Use a prompt in IVR menus, queues and ring groups by choosing its name (e.g. prompts/welcome.wav). Music folders are used for music on hold: "default" is used everywhere unless you choose another folder.'),
    h('div', { class: 'grid2' },
      h('div', { class: 'panel' }, h('h2', null, 'Prompts (greetings, announcements)'),
        h('div', { class: 'filters' }, h('label', null, 'Name', pName), h('label', null, 'WAV file', pFile),
          h('button', { class: 'btn primary', onclick: () => upload('prompts/' + (pName.value.trim() || fileName(pFile.files[0])) + '.wav', pFile.files[0]) }, 'Upload')),
        table([{ label: 'Use as', render: (f) => 'prompts/' + f }, { label: 'Listen', render: (f) => playButton('sounds/file?path=' + encodeURIComponent('prompts/' + f)) },
          { label: '', actions: 1, render: (f) => h('button', { class: 'btn small danger', onclick: () => del('prompts/' + f) }, 'Delete') }], data.prompts, { empty: 'No prompts uploaded yet.' })),
      h('div', { class: 'panel' }, h('h2', null, 'Music on hold'),
        h('div', { class: 'filters' }, h('label', null, 'Folder', mFolder, h('datalist', { id: 'music-folders' }, folders.map((f) => h('option', { value: f })))),
          h('label', null, 'WAV file', mFile),
          h('button', { class: 'btn primary', onclick: () => upload('music/' + (mFolder.value.trim() || 'default') + '/' + fileName(mFile.files[0]) + '.wav', mFile.files[0]) }, 'Upload')),
        folders.map((folder) => [h('h3', null, 'Folder: ' + folder + (folder === 'default' ? ' (used by default)' : '')),
          table([{ label: 'File', render: (f) => f }, { label: 'Listen', render: (f) => playButton('sounds/file?path=' + encodeURIComponent('music/' + folder + '/' + f)) },
            { label: '', actions: 1, render: (f) => h('button', { class: 'btn small danger', onclick: () => del('music/' + folder + '/' + f) }, 'Delete') }],
          data.music[folder], { empty: folder === 'default' ? 'No files: callers hear the built-in melody.' : 'Empty folder.' })]))));
};

PAGES.domains = async () => {
  const main = $('#main');
  const rows = await api('GET', 'domains');
  const name = h('input', { placeholder: 'e.g. office.example.com' });
  const desc = h('input', { placeholder: 'e.g. Head office' });
  fill(main, pageHead('Domains'),
    h('p', { class: 'page-intro' }, 'A domain is one company\'s phone system (extensions, queues, numbers). Phones register with the domain name as their SIP domain. Most installations need one domain.'),
    h('div', { class: 'panel' }, h('h2', null, 'Add a domain'), h('div', { class: 'filters' }, h('label', null, 'Domain name', name), h('label', null, 'Description', desc),
      h('button', { class: 'btn primary', onclick: async () => {
        try {
          await api('POST', 'domains', { domain_name: name.value, domain_description: desc.value });
          toast('Domain added');
          S.domain = name.value.trim().toLowerCase();
          await loadDomains(); await loadMeta(); PAGES.domains();
        } catch (e) { fail(e); }
      } }, 'Add'))),
    table([{ label: 'Domain', key: 'domain_name' }, { label: 'Description', key: 'domain_description' }, { label: 'Extensions', key: 'extensions', num: 1 },
      { label: 'Enabled', render: (r) => yesNo(r.domain_enabled) },
      { label: '', actions: 1, render: (r) => h('button', { class: 'btn small danger', onclick: async () => {
        if (!confirm('Delete domain ' + r.domain_name + ' and all its settings?')) return;
        try { await api('DELETE', 'domains/' + r.domain_uuid); toast('Deleted'); await loadDomains(); await loadMeta(); PAGES.domains(); } catch (e) { fail(e); }
      } }, 'Delete') }], rows, { empty: 'No domains yet.' }));
};

PAGES.dialplans = async () => {
  const main = $('#main');
  const rows = await api('GET', dpath('dialplans'));
  fill(main, pageHead('Dialplans (advanced)'),
    h('p', { class: 'page-intro' }, 'The call routing rules the pages above generate (XML dialplan format), in the order they are checked. They are read-only here; change them through the pages above (or the RingQ portal).'),
    table([{ label: 'Order', key: 'dialplan_order', num: 1 }, { label: 'Context', key: 'dialplan_context' }, { label: 'Name', key: 'dialplan_name' },
      { label: 'Number', key: 'dialplan_number' }, { label: 'Enabled', render: (r) => yesNo(r.dialplan_enabled) },
      { label: 'Rule', wrap: 1, render: (r) => {
        const pre = h('pre', { class: 'xml hidden' }, r.dialplan_xml);
        return h('div', null, h('span', { class: 'muted' }, r.dialplan_description + ' '), h('button', { class: 'btn small', onclick: () => pre.classList.toggle('hidden') }, 'Show'), pre);
      } }], rows));
};

PAGES.system = async () => {
  const main = $('#main');
  const s = await api('GET', 'system');
  const st = s.stats;
  fill(main, pageHead('System',
    h('button', { class: 'btn', onclick: async () => { try { await api('POST', 'system/reload'); toast('Configuration reloaded'); } catch (e) { fail(e); } } }, 'Reload configuration'),
    s.database ? h('button', { class: 'btn', onclick: async () => {
      try { const r = await api('POST', 'system/repair'); toast('Done: ' + r.added + ' item(s) added'); } catch (e) { fail(e); }
    } }, 'Repair defaults') : null),
  h('div', { class: 'cards' }, card(st.version, 'Version'), card(fmtDur(st.uptime_seconds), 'Up for'), card(st.channels, 'Call legs'),
    card(st.registrations, 'Phones online'), card(st.sessions_total, 'Calls since start'), card(st.memory_mb + ' MB', 'Memory')),
  h('div', { class: 'panel' }, table([{ label: 'Setting', key: 'k' }, { label: 'Value', key: 'v', wrap: 1 }], [
    { k: 'Server', v: st.hostname }, { k: 'SIP address', v: (st.sip_ip || '') + ':' + st.sip_port },
    { k: 'Database', v: s.database ? (s.database_name + ' on ' + s.database_host) : 'not used (XML files)' },
    { k: 'Configuration folder', v: s.config_dir }, { k: 'Sounds folder', v: s.sounds_dir }, { k: 'Recordings folder', v: s.recordings_dir }, { k: 'Voicemail folder', v: s.voicemail_dir }])),
  h('h2', null, 'Blocked addresses'),
  h('p', { class: 'page-intro' }, 'Addresses that failed to sign in too many times are blocked for a while (protection against password guessing). Unblock one if it is your own phone.'),
  table([{ label: 'Address', key: 'ip' }, { label: 'Blocked until', render: (b) => fmtTime(b.until) },
    { label: '', actions: 1, render: (b) => h('button', { class: 'btn small', onclick: async () => {
      try { await api('POST', 'system/unblock', { ip: b.ip }); toast('Unblocked'); PAGES.system(); } catch (e) { fail(e); }
    } }, 'Unblock') }], s.blocked, { empty: 'Nothing is blocked.' }));
};
