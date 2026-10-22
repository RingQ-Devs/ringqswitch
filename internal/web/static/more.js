'use strict';
// Wallboard, meetings, security and account pages.

// ---------------------------------------------------------------------------
// Wallboard (TV)

const WB_DEFAULTS = { waitAmber: 1, waitRed: 5, longAmber: 30, longRed: 120, slaTarget: 80, slaSeconds: 20, queues: [] };

function wbSettings() {
  try { return Object.assign({}, WB_DEFAULTS, JSON.parse(localStorage.getItem('rq-wallboard') || '{}')); } catch (e) { return Object.assign({}, WB_DEFAULTS); }
}

function wbLevel(v, amber, red) { return v >= red ? 'red' : v >= amber ? 'amber' : 'green'; }

// hourlyChart: queue calls per hour today, answered and abandoned stacked.
function hourlyChart(rows) {
  const hours = Array.from({ length: 24 }, (_, i) => ({ h: i, answered: 0, abandoned: 0, other: 0 }));
  for (const r of rows) {
    const x = hours[Number(r.hour)];
    if (!x) continue;
    x.answered += Number(r.answered);
    x.abandoned += Number(r.abandoned);
    x.other += Number(r.offered) - Number(r.answered) - Number(r.abandoned);
  }
  const now = new Date().getHours();
  const shown = hours.filter((x) => x.h <= now && (x.h >= 7 || x.answered + x.abandoned + x.other > 0));
  const max = Math.max(4, ...shown.map((x) => x.answered + x.abandoned));
  const W = 720, H = 220, L = 34, B = 24, T = 10;
  const bw = (W - L - 8) / Math.max(shown.length, 1);
  const y = (v) => T + (H - T - B) * (1 - v / max);
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', `0 0 ${W} ${H}`);
  svg.setAttribute('class', 'wb-chart');
  svg.setAttribute('role', 'img');
  svg.setAttribute('aria-label', 'Queue calls per hour today: answered and abandoned');
  const el = (tag, attrs, text) => {
    const n = document.createElementNS(ns, tag);
    for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
    if (text !== undefined) n.textContent = text;
    svg.append(n);
    return n;
  };
  const step = max <= 8 ? 2 : max <= 20 ? 5 : max <= 50 ? 10 : Math.ceil(max / 5 / 10) * 10;
  for (let v = 0; v <= max; v += step) {
    el('line', { x1: L, x2: W - 4, y1: y(v), y2: y(v), class: 'wb-grid' });
    el('text', { x: L - 6, y: y(v) + 4, class: 'wb-axis', 'text-anchor': 'end' }, v);
  }
  const tip = h('div', { class: 'wb-tip hidden' });
  const barTop = (x, y0, y1, cls, rounded) => {
    const hgt = Math.max(0, y0 - y1);
    if (hgt <= 0) return null;
    const r = rounded ? Math.min(4, hgt, bw / 4) : 0;
    const x0 = x, x1 = x + Math.max(bw - 6, 2);
    const d = r ? `M${x0},${y0} V${y1 + r} Q${x0},${y1} ${x0 + r},${y1} H${x1 - r} Q${x1},${y1} ${x1},${y1 + r} V${y0} Z` : `M${x0},${y0} V${y1} H${x1} V${y0} Z`;
    return el('path', { d, class: cls });
  };
  shown.forEach((x, i) => {
    const bx = L + 4 + i * bw;
    const ya = y(x.answered), yb = y(x.answered + x.abandoned);
    barTop(bx, y(0), ya, 'wb-answered', x.abandoned === 0);
    if (x.abandoned) barTop(bx, ya - 2, yb, 'wb-abandoned', true); // 2px surface gap between the segments
    el('text', { x: bx + (bw - 6) / 2, y: H - 6, class: 'wb-axis', 'text-anchor': 'middle' }, String(x.h).padStart(2, '0'));
    const hit = el('rect', { x: bx - 2, y: T, width: bw, height: H - T - B, class: 'wb-hit' });
    hit.addEventListener('mouseenter', () => {
      fill(tip, h('b', null, `${String(x.h).padStart(2, '0')}:00–${String(x.h + 1).padStart(2, '0')}:00`),
        h('div', null, h('span', { class: 'sw wb-answered-sw' }), 'Answered ', h('b', null, x.answered)),
        h('div', null, h('span', { class: 'sw wb-abandoned-sw' }), 'Abandoned ', h('b', null, x.abandoned)),
        x.other ? h('div', { class: 'muted' }, 'Other ' + x.other) : null);
      tip.classList.remove('hidden');
      tip.style.left = Math.min(100 * (bx + bw) / W, 78) + '%';
    });
    hit.addEventListener('mouseleave', () => tip.classList.add('hidden'));
  });
  const legend = h('div', { class: 'wb-legend' }, h('span', null, h('span', { class: 'sw wb-answered-sw' }), 'Answered'),
    h('span', null, h('span', { class: 'sw wb-abandoned-sw' }), 'Abandoned'));
  const tableView = h('details', { class: 'wb-table' }, h('summary', null, 'Show as table'),
    table([{ label: 'Hour', render: (x) => String(x.h).padStart(2, '0') + ':00' }, { label: 'Answered', key: 'answered', num: 1 },
      { label: 'Abandoned', key: 'abandoned', num: 1 }], shown));
  return h('div', { class: 'wb-chartbox' }, legend, h('div', { class: 'wb-plot' }, svg, tip), tableView);
}

PAGES.wallboard = async () => {
  const main = $('#main');
  let set = wbSettings();
  let last = null, hourly = [], sla = {}, chart = null;
  const loadStats = async () => {
    try {
      const [hr, q] = await Promise.all([api('GET', dpath('reports/hourly')), api('GET', dpath('reports/queues?sla=' + set.slaSeconds))]);
      hourly = hr;
      sla = Object.fromEntries(q.map((x) => [x.queue_uuid, x]));
      chart = hourlyChart(hourly.filter((r) => !set.queues.length || set.queues.includes(r.queue_uuid)));
      draw();
    } catch (e) { /* the live part still works */ }
  };
  const settingsDialog = () => {
    const f = {};
    const num = (k, label) => { f[k] = h('input', { type: 'number', value: set[k], min: '0' }); return h('label', null, label, f[k]); };
    const qbox = h('div', { class: 'cmembers' }, (last ? last.queues : []).map((q) => h('label', null,
      h('input', { type: 'checkbox', value: q.uuid, checked: !set.queues.length || set.queues.includes(q.uuid) }), ' ' + q.extension + ' ' + q.name)));
    const back = h('div', { class: 'modal-back' });
    back.append(h('div', { class: 'modal' }, h('h2', null, 'Wallboard settings'),
      h('div', { class: 'form' }, num('waitAmber', 'Callers waiting: amber from'), num('waitRed', 'Callers waiting: red from'),
        num('longAmber', 'Longest wait (s): amber from'), num('longRed', 'Longest wait (s): red from'),
        num('slaSeconds', 'Service level: answered within (s)'), num('slaTarget', 'Service level target (%)')),
      h('p', { class: 'small muted' }, 'Queues shown'), qbox,
      h('div', { class: 'form-actions' }, h('button', { class: 'btn primary', onclick: () => {
        for (const k of Object.keys(f)) set[k] = Number(f[k].value) || 0;
        const chosen = $$('input:checked', qbox).map((c) => c.value);
        set.queues = chosen.length === (last ? last.queues.length : 0) ? [] : chosen;
        try { localStorage.setItem('rq-wallboard', JSON.stringify(set)); } catch (e) { /* private mode */ }
        back.remove();
        loadStats();
        draw();
      } }, 'Save'), h('button', { class: 'btn', onclick: () => back.remove() }, 'Cancel'))));
    document.body.append(back);
  };
  const levelCls = (lvl) => (lvl === 'red' ? ' alert' : lvl === 'amber' ? ' warn' : '');
  // A section with RingQ's header controls: − folds it, + opens it, the
  // arrows show it on the whole screen (remembered by this browser).
  let folded = {};
  try { folded = JSON.parse(localStorage.getItem('rq-wallboard-folded') || '{}'); } catch (e) { /* private mode */ }
  let wide = '';
  const section = (key, color, title, count, body) => {
    const fold = (on) => { folded[key] = on; try { localStorage.setItem('rq-wallboard-folded', JSON.stringify(folded)); } catch (e) { /* private mode */ } draw(); };
    return h('div', { class: 'wb-section ' + color + (folded[key] ? ' folded' : '') + (wide === key ? ' wide' : '') },
      h('div', { class: 'wb-section-head' }, h('h2', null, h('span', { class: 'title-dot ' + color }), title, count === null ? null : h('span', { class: 'count-pill' }, String(count))),
        h('span', { class: 'spacer' }),
        h('button', { class: 'icon-btn sm', type: 'button', title: 'Fold', 'aria-label': 'Fold ' + title, onclick: () => fold(true) }, icon('minus', 16)),
        h('button', { class: 'icon-btn sm', type: 'button', title: 'Open', 'aria-label': 'Open ' + title, onclick: () => fold(false) }, icon('plus', 16)),
        h('button', { class: 'icon-btn sm', type: 'button', title: wide === key ? 'Back' : 'Full screen', 'aria-label': (wide === key ? 'Back from ' : 'Full screen: ') + title,
          onclick: () => { wide = wide === key ? '' : key; draw(); } }, icon(wide === key ? 'shrink' : 'expand', 16))),
      folded[key] ? null : body);
  };
  const kpi = (label, value, opts) => {
    opts = opts || {};
    return h('div', { class: 'kpi' + (opts.size ? ' ' + opts.size : '') + levelCls(opts.level), title: opts.title || null },
      h('div', { class: 'kpi-l' }, label), h('div', { class: 'kpi-v' }, value), opts.sub ? h('div', { class: 'muted small' }, opts.sub) : null);
  };
  // People's names (first and last name of their extension) instead of
  // agent labels such as "Ext 1000".
  let names = {};
  const person = (a) => names[a.extension] || a.name;
  const loadNames = () => api('GET', dpath('directory')).then((d) => {
    names = {};
    for (const x of d) if (x.name && x.name !== x.extension) names[x.extension] = x.name;
    draw();
  }).catch(() => {});
  const setAgent = async (a, on) => {
    try {
      await api('POST', 'agents/' + a.uuid + '/status', { status: on ? 'Available' : 'Logged Out', aux: '' });
      toast(a.name + ': ' + (on ? 'logged in' : 'logged out'));
    } catch (e) { fail(e); }
  };
  const callAct = async (uuid, action, body, msg) => {
    try { await api('POST', 'calls/' + uuid + '/' + action, body || {}); toast(msg); } catch (e) { fail(e); }
  };
  const draw = () => {
    if (!last) return;
    if (document.activeElement && main.contains(document.activeElement) && document.activeElement.tagName === 'INPUT') return;
    const now = Date.now() / 1000 - S.clockSkew;
    const queues = last.queues.filter((q) => !set.queues.length || set.queues.includes(q.uuid));
    const qids = new Set(queues.map((q) => q.uuid));
    const agents = last.agents.filter((a) => !set.queues.length || (a.queues || []).some((n) => queues.some((q) => q.name === n)));
    const sum = (list, f) => list.reduce((n, x) => n + (Number(f(x)) || 0), 0);
    const callers = [];
    for (const q of queues) for (const c of q.callers || []) callers.push(Object.assign({ queue: q }, c));
    const waiting = callers.filter((c) => !c.agent).length;
    const active = callers.filter((c) => c.agent).length;
    const longest = Math.max(0, ...queues.map((q) => q.longest_wait));
    const answered = sum(queues, (q) => q.answered_today);
    const abandoned = sum(queues, (q) => q.abandoned_today);
    const missed = sum(queues, (q) => q.timeout_today);
    const total = answered + abandoned + missed + callers.length;
    const ended = answered + abandoned + missed;
    let offered = 0, inSla = 0;
    for (const id of qids) if (sla[id]) { offered += Number(sla[id].offered); inSla += Number(sla[id].in_sla); }
    const slaPct = offered ? Math.round(100 * inSla / offered) : null;
    const avgWait = answered ? sum(queues, (q) => q.avg_wait * q.answered_today) / answered : 0;
    const avgTalk = answered ? sum(queues, (q) => q.avg_talk * q.answered_today) / answered : 0;
    const talk = sum(agents, (a) => a.talk_seconds);
    const busy = agents.filter((a) => a.on_call || a.caller).length;
    const registered = new Set(last.registrations.map((r) => r.user));
    // Logged in with a phone online: an agent whose phone is offline gets no calls.
    const loggedIn = agents.filter((a) => a.status !== 'Logged Out' && registered.has(a.extension)).length;
    const longFmt = (s) => { s = Math.max(0, Math.round(s)); return `${pad(Math.floor(s / 3600))}:${pad(Math.floor(s % 3600 / 60))}:${pad(s % 60)}`; };
    const since = (t) => {
      const s = Math.max(0, now - t);
      return s >= 86400 ? Math.floor(s / 86400) + 'd' : s >= 3600 ? Math.floor(s / 3600) + 'h ' + Math.floor(s % 3600 / 60) + 'm' : fmtDur(s);
    };
    const canMonitor = isSupervisor();
    const queueCols = [
      { label: 'Queue', render: (c) => c.queue.name + ' (' + c.queue.extension + ')' },
      { label: 'Caller', render: (c) => (c.cid_name && c.cid_name !== c.cid_number ? c.cid_name + ' ' : '') + c.cid_number },
      { label: 'Agent', render: (c) => c.agent || h('span', { class: 'muted' }, '—') },
      { label: 'Status', render: (c) => c.agent ? badge('Talking', 'green') : badge('Waiting', wbLevel(now - c.joined, set.longAmber, set.longRed) === 'red' ? 'red' : 'amber') },
      { label: 'Time', num: 1, render: (c) => h('span', { 'data-since': c.joined }, fmtDur(now - c.joined)) },
    ];
    if (canMonitor) {
      queueCols.push({ label: 'Action', actions: 1, render: (c) => h('div', { class: 'btn-group' },
        c.agent ? [
          h('button', { class: 'btn small', title: 'Hear both sides', onclick: () => callAct(c.uuid, 'listen', { extension: S.me.extension || prompt('Which extension should ring?') || '' }, 'Calling your extension to listen…') }, icon('ear', 14), 'Listen'),
          h('button', { class: 'btn small', title: 'Talk to the agent only', onclick: () => callAct(c.uuid, 'whisper', { extension: S.me.extension || prompt('Which extension should ring?') || '' }, 'Calling your extension to whisper…') }, icon('whisper', 14), 'Whisper'),
          h('button', { class: 'btn small', title: 'Join the call', onclick: () => callAct(c.uuid, 'barge', { extension: S.me.extension || prompt('Which extension should ring?') || '' }, 'Calling your extension to barge in…') }, icon('barge', 14), 'Barge in'),
        ] : null,
        h('button', { class: 'btn small', title: 'Send the caller to another number', onclick: () => {
          const n = prompt('Divert ' + (c.cid_name || c.cid_number) + ' to which number?');
          if (n) callAct(c.uuid, 'transfer', { destination: n.trim() }, 'Call diverted');
        } }, icon('transfer', 14), 'Divert')) });
    }
    const agentCols = [
      { label: 'Agent', render: (a) => {
        const mine = a.extension === S.me.extension;
        const sw = h('input', { type: 'checkbox', checked: a.status !== 'Logged Out', disabled: !(isSupervisor() || mine), 'aria-label': (a.status !== 'Logged Out' ? 'Log out ' : 'Log in ') + a.name });
        sw.addEventListener('change', () => setAgent(a, sw.checked));
        return h('span', { class: 'wb-agent' }, h('span', { class: 'tgl', title: 'Logged in' }, sw, h('span')), person(a));
      } },
      { label: 'Extension', render: (a) => h('span', null, h('span', { class: 'dot ' + (registered.has(a.extension) ? 'green' : 'red'), title: registered.has(a.extension) ? 'Phone online' : 'Phone offline' }), a.extension) },
      { label: 'State', render: (a) => a.status === 'Logged Out' ? h('span', { class: 'muted' }, '—') : !registered.has(a.extension) ? h('span', { class: 'muted', title: 'No phone or browser dialer is signed in: no queue calls' }, 'Phone offline') : a.on_call || a.caller ? (a.caller ? 'In a queue call' : 'On a call') : a.ready_in > 0 ? 'Wrap-up' : a.status === 'On Break' ? 'Idle' : 'Waiting' },
      { label: 'Reason', render: (a) => a.status === 'Logged Out' ? badge('Offline') : a.status === 'On Break' ? badge(a.aux || 'Break', 'amber') : badge('Available', 'green') },
      { label: 'Since', render: (a) => since(a.status_since) },
      { label: 'Answered', key: 'calls_answered', num: 1 },
      { label: 'Missed', num: 1, render: (a) => a.missed_today ? h('span', { style: 'color:var(--red);font-weight:600' }, String(a.missed_today)) : '0' },
      { label: 'In Call', render: (a) => a.caller ? a.caller : a.on_call ? 'Direct call' : h('span', { class: 'muted' }, 'No call') },
      { label: 'Avg. Talking', num: 1, render: (a) => longFmt(a.calls_answered ? a.talk_seconds / a.calls_answered : 0) },
    ];
    const ansPct = ended ? Math.round(100 * answered / ended) : 0;
    const pct = (q) => { const n = q.answered_today + q.abandoned_today + q.timeout_today; return n ? (100 * q.answered_today / n).toFixed(2) + '%' : '0%'; };
    const queueDetailCols = [
      { label: 'Queue No.', render: (q) => h('span', null, h('b', null, q.extension), ' · ' + q.name) },
      { label: 'Answered', render: (q) => String(q.answered_today) },
      { label: 'Abandoned', render: (q) => q.abandoned_today ? h('span', { style: 'color:var(--red);font-weight:600' }, String(q.abandoned_today)) : '0' },
      { label: 'Missed', render: (q) => String(q.timeout_today) },
      { label: '% Answered', render: pct },
      { label: 'Call Waiting', render: (q) => q.waiting ? h('span', { class: 'badge ' + wbLevel(q.waiting, set.waitAmber, set.waitRed) }, String(q.waiting)) : '0' },
      { label: 'Avg. Waiting', render: (q) => longFmt(q.avg_wait) },
      { label: 'Avg. Talking', render: (q) => longFmt(q.avg_talk) },
      { label: 'Agents', render: (q) => String(q.agents) },
      { label: 'Busy Agents', render: (q) => String(q.on_call) },
    ];
    fill(main,
      h('div', { class: 'wb-head' }, h('h1', null, 'Wallboard'), h('span', { class: 'wb-clock', 'data-clock': '1' }, new Date().toLocaleTimeString()),
        h('span', { class: 'spacer' }), h('span', { class: 'small muted' }, h('span', { class: 'dot ' + (WS.open ? 'green' : 'red') }), WS.open ? 'Live' : 'Reconnecting…'),
        h('button', { class: 'btn small', onclick: settingsDialog }, icon('sliders', 14), 'Settings'),
        h('button', { class: 'btn small', onclick: () => {
          document.body.classList.toggle('tv');
          if (document.body.classList.contains('tv') && document.documentElement.requestFullscreen) document.documentElement.requestFullscreen().catch(() => {});
          else if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
          draw();
        } }, icon('monitor', 14), document.body.classList.contains('tv') ? 'Exit TV mode' : 'TV mode')),
      h('div', { class: 'wb-grid' },
        kpi('Call Waiting', String(waiting), { size: 'big', level: wbLevel(waiting, set.waitAmber, set.waitRed) === 'green' ? '' : wbLevel(waiting, set.waitAmber, set.waitRed),
          sub: waiting ? 'Longest ' + fmtDur(longest) : null }),
        kpi('Active', String(active), { size: 'big', title: 'Queue calls connected to an agent now' }),
        kpi('Total Calls', String(total), { title: 'Queue calls today' }),
        kpi('Abandoned', String(abandoned), { level: abandoned ? 'red' : '', title: 'Callers who hung up while waiting' }),
        kpi('Agent Busy', String(busy), { title: 'Agents on a call now' }),
        kpi('Answered Calls', ansPct + '%', { sub: slaPct === null ? null : 'Within ' + set.slaSeconds + ' s: ' + slaPct + '%',
          level: slaPct === null ? '' : slaPct >= set.slaTarget ? '' : slaPct >= set.slaTarget - 10 ? 'amber' : 'red' }),
        kpi('Missed', String(missed), { title: 'Queue calls that timed out without an answer', level: missed ? 'amber' : '' }),
        kpi('Answered', String(answered)),
        kpi('Talk Time', longFmt(talk), { size: 'wide' }),
        kpi('Avg. Waiting', longFmt(avgWait), { size: 'wide' }),
        kpi('Avg. Handling', longFmt(avgTalk), { size: 'wide', title: 'Average talk time of answered queue calls' }),
        kpi('Logged In', loggedIn + '/' + agents.length, { size: 'wide', title: 'Agents logged in with a phone online, of all agents' })),
      section('calls', 'orange', 'Queue Calls', callers.length, table(queueCols, callers.sort((x, y) => x.joined - y.joined), { empty: 'No active queue calls' })),
      section('agents', 'blue', 'Agent Details', agents.length, table(agentCols, agents, { empty: 'No agents yet: add extensions to a queue (Contact center → Queues).' })),
      section('queues', 'green', 'Queue Details', queues.length, table(queueDetailCols, queues.slice().sort((x, y) => extCompare(x.extension, y.extension)), { empty: 'No queues yet (Contact center → Queues).' })),
      section('hourly', '', 'Queue calls per hour today', null, chart || h('div', { class: 'empty' }, 'Loading…')));
  };
  liveOn((l) => { last = l; draw(); });
  every(1000, () => {
    tickTimers(main);
    const c = $('[data-clock]', main);
    if (c) c.textContent = new Date().toLocaleTimeString();
  });
  every(30000, loadStats);
  loadNames();
  every(60000, loadNames);
  loadStats();
};

// ---------------------------------------------------------------------------
// Meetings

PAGES.meetings = async () => {
  const main = $('#main');
  const rows = await api('GET', 'meetings');
  const link = (code) => location.origin + '/meet/m/' + code;
  const edit = (m) => {
    const f = {
      title: h('input', { value: m ? m.title : '', placeholder: 'e.g. Weekly sales meeting' }),
      when: h('input', { type: 'datetime-local', value: m && m.scheduled_at ? new Date(m.scheduled_at).toISOString().slice(0, 16) : '' }),
      dur: h('input', { type: 'number', value: m ? m.duration_minutes || 60 : 60, min: '5' }),
      pass: h('input', { value: m ? m.passcode : '', placeholder: 'optional, digits' }),
      lobby: h('input', { type: 'checkbox', checked: m ? truthy(m.lobby) : true }),
      guests: h('input', { type: 'checkbox', checked: m ? truthy(m.allow_guests) : true }),
      desc: h('textarea', { value: m ? m.description : '' }),
    };
    f.desc.value = m ? m.description : '';
    const back = h('div', { class: 'modal-back' });
    const err = h('div', { class: 'error-text' });
    back.append(h('div', { class: 'modal' }, h('h2', null, m ? 'Edit meeting' : 'Plan a meeting'),
      h('div', { class: 'form' },
        h('div', { class: 'field full' }, h('label', null, 'Title'), f.title),
        h('div', { class: 'field' }, h('label', null, 'When (optional)'), f.when),
        h('div', { class: 'field' }, h('label', null, 'Duration (minutes)'), f.dur),
        h('div', { class: 'field' }, h('label', null, 'Passcode'), f.pass),
        h('div', { class: 'field' }, h('label', { class: 'switch' }, f.lobby, 'Waiting room: you (the organizer) let each attendee in')),
        h('div', { class: 'field' }, h('label', { class: 'switch' }, f.guests, 'Guests without an account may join with the link')),
        h('div', { class: 'field full' }, h('label', null, 'Description'), f.desc)), err,
      h('div', { class: 'form-actions' }, h('button', { class: 'btn primary', onclick: async () => {
        try {
          const body = { title: f.title.value, scheduled_at: f.when.value ? new Date(f.when.value).toISOString() : '', duration_minutes: f.dur.value,
            passcode: f.pass.value, lobby: f.lobby.checked, allow_guests: f.guests.checked, description: f.desc.value };
          await api(m ? 'PUT' : 'POST', 'meetings' + (m ? '/' + m.code : ''), body);
          back.remove();
          PAGES.meetings();
        } catch (e) { err.textContent = e.message; }
      } }, 'Save'), h('button', { class: 'btn', onclick: () => back.remove() }, 'Cancel'))));
    document.body.append(back);
  };
  const copy = async (text) => {
    try { await navigator.clipboard.writeText(text); toast('Link copied'); } catch (e) { prompt('Copy the link:', text); }
  };
  fill(main,
    pageHead('Meetings',
      h('button', { class: 'btn primary', onclick: () => Meet.openWindow() }, 'Meet now'),
      h('button', { class: 'btn', onclick: () => edit(null) }, '+ Plan a meeting')),
    h('p', { class: 'page-intro' }, 'Planned video meetings. "Meet now" (or the camera button at the top) opens the Meet window to start a meeting straight away. Share the link: colleagues join with their account, others as guests (with the passcode, through the waiting room if you switched it on).'),
    table([
      { label: 'Meeting', render: (m) => h('div', null, h('b', null, m.title), h('div', { class: 'muted small' }, m.code + (m.passcode ? ' · passcode ' + m.passcode : ''))) },
      { label: 'When', render: (m) => m.scheduled_at ? fmtTime(m.scheduled_at) + ' · ' + m.duration_minutes + ' min' : 'Any time' },
      { label: 'Host', key: 'host_name' },
      { label: 'Now', render: (m) => m.participants ? badge(m.participants + ' in the meeting', 'green') : '' },
      { label: '', actions: 1, render: (m) => h('div', { class: 'btn-group' },
        h('button', { class: 'btn small primary', onclick: () => Meet.openWindow(m.code) }, 'Join'),
        h('button', { class: 'btn small', onclick: () => copy(link(m.code)) }, 'Copy link'),
        m.can_edit ? [h('button', { class: 'btn small', onclick: () => edit(m) }, 'Edit'),
          h('button', { class: 'btn small danger', onclick: async () => {
            if (!confirm('Delete the meeting ' + m.title + '?')) return;
            try { await api('DELETE', 'meetings/' + m.code); PAGES.meetings(); } catch (e) { fail(e); }
          } }, 'Delete')] : null) },
    ], rows, { empty: 'No meetings yet. "Meet now" starts one straight away.' }));
};

// ---------------------------------------------------------------------------
// My account

PAGES.profile = async () => {
  const main = $('#main');
  const cur = h('input', { type: 'password', autocomplete: 'current-password' });
  const n1 = h('input', { type: 'password', autocomplete: 'new-password' });
  const n2 = h('input', { type: 'password', autocomplete: 'new-password' });
  const err = h('div', { class: 'error-text' });
  fill(main, pageHead('My account'),
    h('div', { class: 'panel' }, table([{ label: '', key: 'k' }, { label: '', key: 'v' }], [
      { k: 'Signed in as', v: S.me.kind === 'extension' ? S.me.extension + ' ' + (S.me.name || '') : S.me.user },
      { k: 'Role', v: { admin: 'Administrator (all domains)', owner: 'Owner: manages this domain', user: 'User: dialer, chat, meetings, monitor, own reports' }[S.me.role] },
      { k: 'Domain', v: S.me.domain || 'all' }])),
    S.me.kind === 'extension' ? h('div', { class: 'panel' }, h('h2', null, 'Change my web password'),
      h('p', { class: 'small muted' }, 'This is the password for this web site. Your phone keeps its own SIP password.'),
      h('div', { class: 'form' }, h('div', { class: 'field' }, h('label', null, 'Current password'), cur),
        h('div', { class: 'field' }, h('label', null, 'New password (8+ characters)'), n1), h('div', { class: 'field' }, h('label', null, 'New password again'), n2)), err,
      h('div', { class: 'form-actions' }, h('button', { class: 'btn primary', onclick: async () => {
        err.textContent = '';
        if (n1.value !== n2.value) { err.textContent = 'The new passwords are different'; return; }
        try { await api('POST', 'password', { current: cur.value, new: n1.value }); toast('Password changed'); cur.value = n1.value = n2.value = ''; } catch (e) { err.textContent = e.message; }
      } }, 'Change password'))) :
      h('div', { class: 'panel' }, 'The administrator password is set in /etc/ringqswitch/ringqswitch.xml (admin-password). Sign in with an extension to use the dialer, chat and meetings.'));
};
