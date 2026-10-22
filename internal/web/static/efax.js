'use strict';
// eFax (like RingQ's): send a document as a fax from one of the company's
// DID numbers, and receive faxes on fax extensions (a DID assigned to a
// fax extension in Call Flow → Others). Tabs: Overview, Send eFax, Inbox,
// Status log, Settings.

const EFAX_STATUS = {
  queued: ['Queued', 'blue'], dialing: ['Dialling', 'amber'], sending: ['Sending', 'amber'], receiving: ['Receiving', 'amber'],
  sent: ['Sent', 'green'], received: ['Received', 'green'], failed: ['Failed', 'red'],
};
const EFAX_BUSY = new Set(['queued', 'dialing', 'sending', 'receiving']);

function efaxWhen(sec) {
  const d = new Date(Number(sec) * 1000);
  if (isNaN(d)) return '';
  const day = new Date(); day.setHours(0, 0, 0, 0);
  const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  if (d >= day) return t('Today') + ' ' + time;
  if (d >= day - 86400000) return t('Yesterday') + ' ' + time;
  return d.toLocaleDateString([], { day: 'numeric', month: 'short', year: d.getFullYear() === day.getFullYear() ? undefined : 'numeric' }) + ' ' + time;
}

function efaxStatus(j) {
  const [label, tone] = EFAX_STATUS[j.status] || [j.status, ''];
  let txt = t(label);
  if ((j.status === 'sending' || j.status === 'receiving') && Number(j.pages) > 0) txt += ' · ' + t('page') + ' ' + (Number(j.pages) + 1);
  return h('div', { class: 'ef-status' }, badge(txt, tone),
    j.status === 'failed' && j.error_reason ? h('div', { class: 'ef-reason', title: j.error_reason }, j.error_reason) : null);
}

PAGES.efax = async (args) => {
  const main = $('#main');
  const tabs = h('div', { class: 'ht-tabs', role: 'tablist' });
  const body = h('div', { class: 'ef-body' });
  let tab = ['overview', 'send', 'inbox', 'log', 'settings'].includes(args && args[0]) ? args[0] : 'overview';
  let ov = { counts: {}, numbers: [], settings: {} };
  let jobs = [];
  let filter = '';
  const q = h('input', { type: 'search', class: 'search-box', placeholder: t('Search numbers'), 'aria-label': t('Search numbers') });

  async function loadOverview() {
    ov = await api('GET', dpath('fax'));
  }
  async function loadJobs() {
    const p = new URLSearchParams({ limit: '300' });
    if (tab === 'inbox') p.set('direction', 'in');
    if (tab === 'log' && filter) {
      if (filter === 'active') p.set('status', 'active');
      else p.set('status', filter);
    }
    if (q.value.trim()) p.set('q', q.value.trim());
    jobs = await api('GET', dpath('fax/jobs?' + p));
  }
  const fileURL = (j, dl) => '/api/' + dpath('fax/jobs/' + j.id + '/file') + (dl ? '?download=1' : '');

  // ---- actions
  async function retry(j) {
    try { await api('POST', dpath('fax/jobs/' + j.id + '/retry')); toast(t('Queued to send again')); await refresh(); } catch (e) { fail(e); }
  }
  async function remove(j) {
    if (!confirm(t('Delete this fax?') + ' ' + (j.direction === 'in' ? t('From') + ' ' + j.from_number : t('To') + ' ' + j.to_number))) return;
    try { await api('DELETE', dpath('fax/jobs/' + j.id)); toast(t('Deleted')); await refresh(); } catch (e) { fail(e); }
  }
  function actions(j) {
    const out = [];
    if (j.has_file === 'true') {
      out.push(h('a', { class: 'icon-btn', href: fileURL(j), target: '_blank', rel: 'noopener', title: t(j.direction === 'in' ? 'Open the fax' : 'Open the document') }, icon('eye', 16)));
      out.push(h('a', { class: 'icon-btn', href: fileURL(j, true), title: t('Download') }, icon('download', 16)));
    }
    if (j.has_trace === 'true') {
      const tr = (side) => '/api/' + dpath('fax/jobs/' + j.id + '/trace?side=' + side);
      out.push(h('a', { class: 'icon-btn', href: tr('heard'), title: t('Call audio heard from the far end (for troubleshooting)') }, icon('ear', 16)));
      out.push(h('a', { class: 'icon-btn', href: tr('sent'), title: t('Call audio sent (for troubleshooting)') }, icon('volume', 16)));
    }
    if (j.direction === 'out' && j.status === 'failed') out.push(h('button', { type: 'button', class: 'icon-btn', title: t('Send again'), onclick: () => retry(j) }, icon('refresh', 16)));
    if (!EFAX_BUSY.has(j.status)) out.push(h('button', { type: 'button', class: 'icon-btn danger', title: t('Delete'), onclick: () => remove(j) }, icon('trash', 16)));
    return h('div', { class: 'row-actions' }, out);
  }
  const dirCell = (j) => h('span', { class: 'ef-dir ' + j.direction, title: t(j.direction === 'in' ? 'Received' : 'Sent') },
    icon(j.direction === 'in' ? 'incoming' : 'outgoing', 15), t(j.direction === 'in' ? 'In' : 'Out'));
  const logCols = [
    { label: t('Time'), render: (j) => efaxWhen(j.created_at) },
    { label: t('Direction'), render: dirCell },
    { label: t('From'), key: 'from_number' },
    { label: t('To'), key: 'to_number' },
    { label: t('Pages'), num: 1, render: (j) => Number(j.pages) || (EFAX_BUSY.has(j.status) ? '' : '0') },
    { label: t('Status'), wrap: 1, render: efaxStatus },
    { label: '', actions: 1, render: actions },
  ];

  // ---- tabs
  function kpi(label, value, cls, sub) {
    return h('div', { class: 'kpi ' + (cls || '') }, h('span', { class: 'kpi-l' }, t(label)), h('span', { class: 'kpi-v' }, String(value || 0)),
      sub ? h('span', { class: 'muted small' }, sub) : null);
  }
  function gsWarning() {
    return ov.ghostscript ? null : h('div', { class: 'ef-warn' }, icon('info', 16),
      h('span', null, t('PDF documents need the PDF converter on the server:'), ' ', h('code', null, 'sudo apt install ghostscript'), ' ', t('(TIFF and images send without it).')));
  }
  function overview() {
    const c = ov.counts || {};
    const recent = jobs.slice(0, 8);
    return [gsWarning(),
      h('div', { class: 'kpis ef-kpis' },
        kpi('Sent', c.sent, '', (c.pages_sent || 0) + ' ' + t('pages')),
        kpi('Received', c.received, '', (c.pages_received || 0) + ' ' + t('pages')),
        kpi('In progress', c.active, c.active ? 'warn' : ''),
        kpi('Failed', c.failed, c.failed ? 'alert' : ''),
        kpi('Today', (c.today_sent || 0) + (c.today_received || 0), '', (c.today_sent || 0) + ' ' + t('sent') + ' · ' + (c.today_received || 0) + ' ' + t('received'))),
      h('div', { class: 'ef-cards' },
        h('div', { class: 'card ef-card' }, h('h3', null, icon('outgoing', 16), t('Send a fax')),
          h('p', { class: 'muted' }, t('Upload a PDF, TIFF or image and send it from one of your numbers.')),
          h('button', { type: 'button', class: 'btn primary', onclick: () => go('send') }, icon('fax', 16), t('Send eFax'))),
        h('div', { class: 'card ef-card' }, h('h3', null, icon('incoming', 16), t('Receive faxes')),
          h('p', { class: 'muted' }, t('Assign a DID number to a fax extension in Call Flow (Others). Faxes it receives arrive in the Inbox as PDF.')),
          h('div', { class: 'ht-btns' }, h('a', { class: 'btn', href: '#/callflow' }, icon('route', 16), t('Call Flow')),
            h('button', { type: 'button', class: 'btn', onclick: () => go('inbox') }, icon('download', 16), t('Inbox'))))),
      h('h3', { class: 'ef-h' }, t('Recent faxes')),
      table(logCols, recent, { empty: t('No faxes yet.') })];
  }

  function sendForm() {
    const nums = ov.numbers || [];
    const from = selectOf(nums.map((n) => [n.number, n.number + (n.label ? ' · ' + n.label : '') + ' (' + n.trunk + ')']), nums.length === 1 ? nums[0].number : '', t('— choose the number —'));
    const to = h('input', { type: 'text', placeholder: t('e.g. +6531591234'), autocomplete: 'off', inputmode: 'tel' });
    const fileIn = h('input', { type: 'file', accept: '.pdf,.tif,.tiff,.png,.jpg,.jpeg,.gif,application/pdf,image/*', class: 'hidden' });
    const fname = h('span', { class: 'muted' }, t('No file chosen'));
    let file = null;
    const pick = (f) => {
      if (!f) return;
      if (f.size > 20 * 1024 * 1024) { toast(t('The document is too large (20 MB at most)'), true); return; }
      file = f;
      fill(fname, h('b', null, f.name), ' · ' + Math.max(1, Math.round(f.size / 1024)) + ' KB');
      drop.classList.add('has');
    };
    fileIn.addEventListener('change', () => pick(fileIn.files[0]));
    const drop = h('div', { class: 'ef-drop', tabindex: '0', role: 'button', onclick: () => fileIn.click(),
      onkeydown: (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); fileIn.click(); } } },
      icon('upload', 26), h('b', null, t('Drop the document here or click to choose')), h('span', { class: 'muted small' }, t('PDF, TIFF, PNG, JPEG or GIF · 20 MB at most')), fname, fileIn);
    drop.addEventListener('dragover', (e) => { e.preventDefault(); drop.classList.add('over'); });
    drop.addEventListener('dragleave', () => drop.classList.remove('over'));
    drop.addEventListener('drop', (e) => { e.preventDefault(); drop.classList.remove('over'); pick(e.dataTransfer.files[0]); });
    const btn = h('button', { type: 'submit', class: 'btn primary' }, icon('fax', 16), t('Send eFax'));
    const form = h('form', { class: 'card ef-send', novalidate: true },
      h('div', { class: 'form' },
        h('div', { class: 'field' }, h('label', { class: 'req' }, t('From')), from,
          h('div', { class: 'help' }, nums.length ? t('The fax goes out on this number\'s trunk, with it as caller ID.') : t('No DID numbers yet: add one in Call Flow.'))),
        h('div', { class: 'field' }, h('label', { class: 'req' }, t('To (fax number)')), to,
          h('div', { class: 'help' }, t('Several numbers: separate them with commas (20 at most).'))),
        h('div', { class: 'field full' }, h('label', { class: 'req' }, t('Document')), drop)),
      h('div', { class: 'ef-send-foot' }, h('span', { class: 'muted small' }, t('Header on every page') + ': ' + (ov.settings.header || '') + ' · ' + t('station id') + ' ' + (ov.settings.ident || '')),
        h('span', { class: 'spacer' }), btn));
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      if (!from.value) { toast(t('Choose the number to send from'), true); from.focus(); return; }
      if (!to.value.trim()) { toast(t('Enter the fax number to send to'), true); to.focus(); return; }
      if (!file) { toast(t('Choose the document to fax'), true); return; }
      btn.disabled = true;
      fill(btn, icon('refresh', 16), t('Uploading…'));
      try {
        const p = new URLSearchParams({ from: from.value, to: to.value, name: file.name });
        const r = await api('POST', dpath('fax/send?' + p), await file.arrayBuffer(), true);
        toast(t('Fax queued') + ': ' + r.pages + ' ' + t('page(s)') + (r.jobs.length > 1 ? ', ' + r.jobs.length + ' ' + t('numbers') : ''));
        filter = '';
        go('log');
      } catch (err) {
        fail(err);
        btn.disabled = false;
        fill(btn, icon('fax', 16), t('Send eFax'));
      }
    });
    return [gsWarning(), form];
  }

  function inbox() {
    const cols = [
      { label: t('Received'), render: (j) => efaxWhen(j.created_at) },
      { label: t('From'), key: 'from_number' },
      { label: t('To (your number)'), key: 'to_number' },
      { label: t('Pages'), num: 1, render: (j) => Number(j.pages) || '' },
      { label: t('Status'), wrap: 1, render: efaxStatus },
      { label: '', actions: 1, render: actions },
    ];
    return [h('div', { class: 'ef-bar' }, q), table(cols, jobs, { empty: t('No faxes received yet. Assign a DID number to a fax extension in Call Flow (Others).') })];
  }

  function log() {
    const chips = [['', 'All'], ['sent', 'Sent'], ['received', 'Received'], ['failed', 'Failed'], ['active', 'In progress']].map(([k, l]) =>
      h('button', { type: 'button', class: 'chip' + (filter === k ? ' on' : ''), onclick: async () => { filter = k; await refresh(); } }, t(l)));
    return [h('div', { class: 'ef-bar' }, h('div', { class: 'chips' }, chips), h('span', { class: 'spacer' }), q), table(logCols, jobs, { empty: t('No faxes match.') })];
  }

  function settings() {
    const st = ov.settings || {};
    const ident = h('input', { value: st.fax_ident || '', maxlength: '20', placeholder: st.company_name || 'Fax' });
    const header = h('input', { value: st.fax_header || '', maxlength: '60', placeholder: st.company_name || 'Fax' });
    const save = h('button', { type: 'submit', class: 'btn primary' }, t('Save'));
    const form = h('form', { class: 'card ef-settings' }, h('h3', null, icon('settings', 16), t('Sender identity')),
      h('div', { class: 'form' },
        h('div', { class: 'field' }, h('label', null, t('Station id')), ident, h('div', { class: 'help' }, t('Shown on the receiving fax machine (20 characters). Empty: the company name.'))),
        h('div', { class: 'field' }, h('label', null, t('Page header')), header, h('div', { class: 'help' }, t('Printed at the top of every page with the date, number and page count. Empty: the company name.')))),
      h('div', { class: 'ef-send-foot' }, h('span', { class: 'spacer' }), save));
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      try { await api('PUT', dpath('fax/settings'), { fax_ident: ident.value.trim(), fax_header: header.value.trim() }); toast(t('Saved')); await refresh(); } catch (err) { fail(err); }
    });
    const faxNums = (ov.numbers || []).filter((n) => n.fax);
    const ext = h('div', { class: 'card ef-settings' }, h('h3', null, icon('fax', 16), t('Fax extensions')),
      h('p', { class: 'muted' }, t('A fax extension answers like a fax machine. Point a DID number at it in Call Flow (Others) to receive faxes on that number.')),
      faxNums.length ? h('p', { class: 'small' }, t('DID numbers assigned now') + ': ' + faxNums.map((n) => n.number + ' → ' + n.fax).join(', ')) : null,
      h('div', { class: 'ht-btns' }, isOwner() ? h('a', { class: 'btn', href: '#/faxes' }, icon('list', 16), t('Manage fax extensions')) : null,
        isOwner() ? h('a', { class: 'btn', href: '#/callflow' }, icon('route', 16), t('Call Flow')) : null));
    return [form, ext];
  }

  // ---- drawing
  function go(k) { tab = k; history.replaceState(null, '', '#/efax/' + k); refresh(); }
  function drawTabs() {
    const c = ov.counts || {};
    fill(tabs, [['overview', 'Overview'], ['send', 'Send eFax'], ['inbox', 'Inbox'], ['log', 'Status log'], ['settings', 'Settings']].map(([k, l]) =>
      h('button', { type: 'button', role: 'tab', 'aria-selected': String(tab === k), class: tab === k ? 'on' : '', onclick: () => go(k) },
        t(l), k === 'log' && c.active ? h('span', { class: 'ef-count' }, String(c.active)) : null)));
  }
  function draw() {
    drawTabs();
    const parts = { overview, send: sendForm, inbox, log, settings }[tab]();
    fill(body, parts);
  }
  async function refresh() {
    try {
      await Promise.all([loadOverview(), tab === 'send' || tab === 'settings' ? null : loadJobs()]);
      draw();
    } catch (e) { fail(e); }
  }
  let qTimer;
  q.addEventListener('input', () => { clearTimeout(qTimer); qTimer = setTimeout(async () => { await loadJobs(); draw(); q.focus(); }, 300); });

  fill(main, h('div', { class: 'muted' }, t('Loading…')));
  await Promise.all([loadOverview(), loadJobs()]);
  fill(main, h('div', { class: 'page-head' }, h('h1', null, t('eFax'), h('span', { class: 'info-dot', title: t('Send faxes from your numbers and receive them on fax extensions') }, icon('info', 15)))),
    tabs, body);
  draw();
  // Live status while faxes are in progress (every 4 s), else every 20 s.
  let tick = 0;
  every(4000, async () => {
    if (!document.body.contains(body)) return;
    tick++;
    const busy = (ov.counts && ov.counts.active) || jobs.some((j) => EFAX_BUSY.has(j.status));
    if (!busy && tick % 5) return;
    if (tab === 'send' || tab === 'settings' || $('.modal-back') || document.activeElement === q) return;
    try { await Promise.all([loadOverview(), loadJobs()]); draw(); } catch (e) { /* next time */ }
  });
};
