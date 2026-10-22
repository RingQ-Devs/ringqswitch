'use strict';
// Call status (the top bar's tower button, like RingQ's): the calls in
// progress and the extensions signed in right now, live, with today's
// previous calls under them. Supervisors listen, whisper, barge in or hang
// up from here.

const CS_KINDS = [['queue', 'Queue'], ['in', 'Inbound'], ['out', 'Outbound'], ['fax', 'Fax'], ['internal', 'Internal']];

function csKind(c) {
  if (/fax/i.test(c.a.application || '')) return 'fax';
  if (c.queue || c.a.application === 'callcenter') return 'queue';
  return callDirection(c);
}

// csQueue finds the queue a live call is in (or was answered from).
function csQueue(c, queues) {
  for (const q of queues || []) {
    const m = (q.callers || []).find((x) => x.uuid === c.a.uuid || (c.b && x.uuid === c.b.uuid));
    if (m) return { name: q.extension ? q.extension + ' ' + q.name : q.name, caller: m };
  }
  return null;
}

function csStatus(c) {
  if (c.queue && c.queue.caller) {
    const s = c.queue.caller.state || '';
    if (/answer|bridg|trying/i.test(s) && c.talking) return ['Talking', 'green'];
    return ['Waiting in queue', 'violet'];
  }
  return [c.state, c.talking ? 'green' : c.state === 'Ringing' ? 'amber' : 'blue'];
}

// The status of a finished call, from its call record.
function cdrStatus(r) {
  const q = (r.cc_queue || '').split('@')[0];
  if (q) {
    if (r.cc_agent || (Number(r.billsec) > 0 && r.status === 'answered')) return [q + ' - ' + t('Queue Answered'), 'green'];
    if (/timeout/i.test(r.cc_cancel_reason || '')) return [q + ' - ' + t('Queue Timeout'), 'amber'];
    return [t('Queue Caller Abandoned'), 'red'];
  }
  const st = r.status || (Number(r.billsec) > 0 ? 'answered' : r.missed_call === 'true' ? 'missed' : '');
  return ({ answered: ['Answered', 'green'], missed: ['Missed', 'red'], voicemail: ['Voicemail', 'amber'], cancelled: ['Cancelled', 'grey'],
    failed: ['Failed', 'red'], busy: ['Busy', 'amber'] })[st] || [(r.hangup_cause || '—').replace(/_/g, ' ').toLowerCase(), 'grey'];
}

function cdrKind(r) {
  if (/fax/i.test(r.last_app || '')) return 'fax';
  if (r.cc_queue) return 'queue';
  return { inbound: 'in', outbound: 'out' }[r.direction] || 'internal';
}

PAGES.callstatus = async () => {
  const main = $('#main');
  await loadMeta();
  let tab = 'active';
  let q = '';
  let live = null;
  let prev = [];
  let prevTotal = 0;
  let lastCount = -1;
  const agentName = (uuid) => {
    const a = (S.meta ? S.meta.agents : []).find((x) => x.uuid === uuid);
    return a ? a.name + (a.extension ? ' (' + a.extension + ')' : '') : uuid || ''; // records keep the agent's name
  };
  const extName = (ext) => {
    const e = (S.meta ? S.meta.extensions : []).find((x) => x.extension === ext);
    return e && e.name && e.name !== e.extension ? e.name : '';
  };
  const dirPill = (k) => h('span', { class: 'cs-pill k-' + k }, t((CS_KINDS.find((x) => x[0] === k) || [k, k])[1]));
  const hms = (sec) => new Date(Number(sec) * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
  const matches = (...vals) => !q || vals.join(' ').toLowerCase().includes(q);

  const searchBox = h('input', { type: 'search', class: 'cs-search', placeholder: t('Search number, type, route'), 'aria-label': t('Search') });
  searchBox.addEventListener('input', () => { q = searchBox.value.trim().toLowerCase(); draw(); });
  const body = h('div');
  fill(main,
    pageHead(t('Call status'), h('label', { class: 'cs-search-wrap' }, icon('search', 16), searchBox)),
    body);

  async function loadPrevious() {
    if (!S.me.database) return;
    try {
      const r = await api('GET', dpath('cdr?limit=50'));
      prev = (r.rows || []).filter((x) => !x.leg || x.leg === 'a');
      prevTotal = prev.length;
    } catch (e) { prev = []; }
    draw();
  }

  function monitor(c, mode) {
    let ext = S.me.extension;
    if (!ext) {
      ext = prompt(t('Which extension should ring so you can listen?'));
      if (!ext) return;
    }
    api('POST', 'calls/' + c.a.uuid + '/' + mode, { extension: ext })
      .then(() => toast(t('Calling your extension…'))).catch(fail);
  }

  function activeTable(calls) {
    const now = Date.now() / 1000 - S.clockSkew;
    const sup = isSupervisor();
    const rows = calls.map((c) => {
      const k = csKind(c);
      const [st, color] = csStatus(c);
      const from = (c.a.cid_name && c.a.cid_name !== c.a.cid_num ? c.a.cid_name + ' ' : '') + c.a.cid_num;
      const answeredBy = c.queue && c.queue.caller && c.queue.caller.agent ? c.queue.caller.agent : c.talking && c.b ? (c.b.cid_num === c.a.cid_num ? c.to : c.b.cid_num || c.to) : '';
      const actions = sup ? h('div', { class: 'cs-actions' },
        c.talking ? [
          h('button', { type: 'button', class: 'icon-btn sm', title: t('Listen'), onclick: () => monitor(c, 'listen') }, icon('ear', 15)),
          h('button', { type: 'button', class: 'icon-btn sm', title: t('Whisper'), onclick: () => monitor(c, 'whisper') }, icon('whisper', 15)),
          h('button', { type: 'button', class: 'icon-btn sm', title: t('Barge in'), onclick: () => monitor(c, 'barge') }, icon('barge', 15))] : null,
        h('button', { type: 'button', class: 'icon-btn sm danger', title: t('Hang up'), onclick: () => {
          if (confirm(t('Hang up this call?'))) api('POST', 'calls/' + c.a.uuid + '/hangup', {}).then(() => toast(t('Call ended'))).catch(fail);
        } }, icon('hangup', 15))) : '';
      return { k, row: h('tr', { class: 'cs-row k-' + k },
        h('td', null, hms(c.a.created_epoch)),
        h('td', null, dirPill(k)),
        h('td', null, h('span', { class: 'cs-fromto' }, from, h('span', { class: 'cs-arrow' }, '→'), c.to || '—')),
        h('td', null, c.queue ? c.queue.name : '—'),
        h('td', null, answeredBy || '—'),
        h('td', { class: 'num-cell' }, h('span', { 'data-since': c.since }, fmtDur(now - c.since))),
        h('td', null, h('span', { class: 'cs-status ' + color }, t(st))),
        h('td', { class: 'actions' }, actions),
        h('td', null, c.a.trunk || (c.b && c.b.trunk) || '—')),
      text: [from, c.to, t(CS_KINDS.find((x) => x[0] === k)[1]), c.queue ? c.queue.name : '', c.a.trunk || '', st].join(' ') };
    }).filter((r) => matches(r.text));
    const head = ['Time', 'Direction', 'From → To', 'Queue', 'Answered by', 'Duration', 'Status', 'Action', 'Rule / Route'];
    return h('div', { class: 'table-wrap cs-table' }, h('table', null,
      h('thead', null, h('tr', null, head.map((x, i) => h('th', { class: i === 5 ? 'num-cell' : null }, t(x))))),
      h('tbody', null, rows.length ? rows.map((r) => r.row) : h('tr', null, h('td', { colspan: String(head.length), class: 'cs-empty' }, t('No calls in progress'))))));
  }

  function onlineTable(regs, chans) {
    const busy = new Set();
    chans.forEach((c) => { busy.add(c.cid_num); busy.add(c.dest); });
    const seen = new Map();
    for (const r of regs) {
      const list = seen.get(r.user) || [];
      list.push(r);
      seen.set(r.user, list);
    }
    const rows = [...seen.entries()].sort((a, b) => a[0].localeCompare(b[0], undefined, { numeric: true }))
      .filter(([ext, list]) => matches(ext, extName(ext), list.map((r) => r.user_agent + ' ' + r.source).join(' ')))
      .map(([ext, list]) => h('tr', { class: 'cs-row k-' + (busy.has(ext) ? 'in' : 'internal') },
        h('td', null, h('b', null, ext)),
        h('td', null, extName(ext) || '—'),
        h('td', null, h('span', { class: 'cs-status ' + (busy.has(ext) ? 'green' : 'grey') }, t(busy.has(ext) ? 'On a call' : 'Idle'))),
        h('td', null, list.map((r) => h('div', { class: 'cs-dev' }, /web dialer/i.test(r.user_agent || '') ? t('Browser dialer') : (r.user_agent || t('SIP phone'))))),
        h('td', null, list.map((r) => h('div', { class: 'cs-dev mono' }, r.source.replace(/^[a-z]+:/i, '')))),
        h('td', null, list.map((r) => h('div', { class: 'cs-dev' }, hms(r.since))))));
    const head = ['Extension', 'Name', 'Status', 'Device', 'Address', 'Signed in at'];
    return h('div', { class: 'table-wrap cs-table' }, h('table', null,
      h('thead', null, h('tr', null, head.map((x) => h('th', null, t(x))))),
      h('tbody', null, rows.length ? rows : h('tr', null, h('td', { colspan: String(head.length), class: 'cs-empty' }, t('No extension is signed in'))))));
  }

  function previousTable() {
    const rows = prev.map((r) => {
      const k = cdrKind(r);
      const [st, color] = cdrStatus(r);
      const queue = (r.cc_queue || '').split('@')[0];
      const answered = r.cc_agent ? agentName(r.cc_agent) : r.status === 'answered' || Number(r.billsec) > 0 ? (k === 'out' ? '' : r.destination_number) : '';
      const route = k === 'in' || k === 'queue' ? (r.caller_destination && r.caller_destination !== r.destination_number ? r.caller_destination : '') : '';
      const wait = r.waitsec !== undefined && r.waitsec !== '' ? fmtDur(r.waitsec) : '—';
      const when = r.start_stamp ? new Date(r.start_stamp.replace(' ', 'T')).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }) : '';
      return { row: h('tr', { class: 'cs-row k-' + k },
        h('td', null, when),
        h('td', null, dirPill(k)),
        h('td', null, h('span', { class: 'cs-fromto' }, (r.caller_id_number || '—'), h('span', { class: 'cs-arrow' }, '→'), r.destination_number || '—')),
        h('td', null, queue || '—'),
        h('td', null, answered || '—'),
        h('td', null, wait + ' / ' + fmtDur(r.talksec !== undefined ? r.talksec : (r.billsec || 0))),
        h('td', null, h('span', { class: 'cs-status ' + color }, t(st))),
        h('td', null, route || '—')),
      text: [r.caller_id_number, r.caller_id_name, r.destination_number, t(CS_KINDS.find((x) => x[0] === k)[1]), queue, route, st].join(' ') };
    }).filter((r) => matches(r.text));
    const head = ['Time', 'Direction', 'From → To', 'Queue', 'Answered by', 'Wait / Dur', 'Status', 'Rule / Route'];
    return [h('div', { class: 'cs-sub' }, h('h2', null, t('Previous calls')), h('span', { class: 'cs-count' }, String(rows.length))),
      h('div', { class: 'table-wrap cs-table' }, h('table', null,
        h('thead', null, h('tr', null, head.map((x) => h('th', null, t(x))))),
        h('tbody', null, rows.length ? rows.map((r) => r.row) : h('tr', null, h('td', { colspan: String(head.length), class: 'cs-empty' }, t('No calls today yet'))))))];
  }

  function draw() {
    if (!live) { fill(body, h('div', { class: 'muted' }, t('Loading…'))); return; }
    const calls = groupCalls(live.channels || []);
    calls.forEach((c) => { c.queue = csQueue(c, live.queues); });
    const regs = live.registrations || [];
    const online = new Set(regs.map((r) => r.user)).size;
    const tabBtn = (id, label, n) => h('button', { type: 'button', class: 'cs-tab' + (tab === id ? ' on' : ''), onclick: () => { tab = id; draw(); } },
      t(label), h('span', { class: 'cs-count' }, String(n)));
    fill(body,
      h('div', { class: 'cs-tabs' }, tabBtn('active', 'Active calls', calls.length), tabBtn('online', 'Online extensions', online)),
      tab === 'active' ? h('div', { class: 'cs-legend' }, CS_KINDS.map(([k, label]) => h('span', null, h('i', { class: 'k-' + k }), t(label)))) : null,
      tab === 'active' ? activeTable(calls) : onlineTable(regs, live.channels || []),
      S.me.database ? previousTable() : null);
  }

  liveOn((l) => {
    live = l;
    const n = (l.channels || []).length;
    if (lastCount > n) { setTimeout(loadPrevious, 1500); setTimeout(loadPrevious, 5000); } // a call ended: its record is written a moment later
    lastCount = n;
    const focused = document.activeElement === searchBox;
    draw();
    if (focused) searchBox.focus();
  });
  every(1000, () => tickTimers(body));
  every(30000, loadPrevious);
  draw();
  loadPrevious();
};
