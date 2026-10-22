'use strict';
// Call Flow (menu Call Flow, like RingQ's): the trunks (SIP trunks, IP
// gateways and their DID numbers) and where each DID goes. A new DID waits
// in "Available DID Numbers"; drag it onto IVR, Queue, Extension or Others
// and choose which one in the window that opens (IVR also offers the AI
// agents: a number answered by a voice AI). An extension chosen under
// Others is eFax: the number answers as a fax machine for that extension
// (dropped on Extension, it rings the extension). Dragging works both ways:
// a linked number dragged back to Available DID Numbers is unlinked.
// Adding trunks, DIDs and modules (the + of each card) happens in windows too.

// Countries and their calling codes: the country of a DID (stored as its
// destination_prefix, as the RingQ portal does) and the flag shown with a number.
const COUNTRIES = [
  ['AF', 'Afghanistan', '93'], ['AR', 'Argentina', '54'], ['AU', 'Australia', '61'], ['AT', 'Austria', '43'], ['BH', 'Bahrain', '973'],
  ['BD', 'Bangladesh', '880'], ['BE', 'Belgium', '32'], ['BT', 'Bhutan', '975'], ['BR', 'Brazil', '55'], ['BN', 'Brunei', '673'],
  ['KH', 'Cambodia', '855'], ['CA', 'Canada', '1'], ['CL', 'Chile', '56'], ['CN', 'China', '86'], ['CO', 'Colombia', '57'],
  ['CZ', 'Czechia', '420'], ['DK', 'Denmark', '45'], ['EG', 'Egypt', '20'], ['ET', 'Ethiopia', '251'], ['FJ', 'Fiji', '679'],
  ['FI', 'Finland', '358'], ['FR', 'France', '33'], ['DE', 'Germany', '49'], ['GH', 'Ghana', '233'], ['GR', 'Greece', '30'],
  ['HK', 'Hong Kong', '852'], ['HU', 'Hungary', '36'], ['IN', 'India', '91'], ['ID', 'Indonesia', '62'], ['IR', 'Iran', '98'],
  ['IQ', 'Iraq', '964'], ['IE', 'Ireland', '353'], ['IL', 'Israel', '972'], ['IT', 'Italy', '39'], ['JP', 'Japan', '81'],
  ['JO', 'Jordan', '962'], ['KZ', 'Kazakhstan', '7'], ['KE', 'Kenya', '254'], ['KW', 'Kuwait', '965'], ['LA', 'Laos', '856'],
  ['LB', 'Lebanon', '961'], ['MO', 'Macau', '853'], ['MY', 'Malaysia', '60'], ['MV', 'Maldives', '960'], ['MX', 'Mexico', '52'],
  ['MA', 'Morocco', '212'], ['MM', 'Myanmar', '95'], ['NP', 'Nepal', '977'], ['NL', 'Netherlands', '31'], ['NZ', 'New Zealand', '64'],
  ['NG', 'Nigeria', '234'], ['NO', 'Norway', '47'], ['OM', 'Oman', '968'], ['PK', 'Pakistan', '92'], ['PG', 'Papua New Guinea', '675'],
  ['PE', 'Peru', '51'], ['PH', 'Philippines', '63'], ['PL', 'Poland', '48'], ['PT', 'Portugal', '351'], ['QA', 'Qatar', '974'],
  ['RO', 'Romania', '40'], ['RU', 'Russia', '7'], ['SA', 'Saudi Arabia', '966'], ['SG', 'Singapore', '65'], ['ZA', 'South Africa', '27'],
  ['KR', 'South Korea', '82'], ['ES', 'Spain', '34'], ['LK', 'Sri Lanka', '94'], ['SE', 'Sweden', '46'], ['CH', 'Switzerland', '41'],
  ['TW', 'Taiwan', '886'], ['TZ', 'Tanzania', '255'], ['TH', 'Thailand', '66'], ['TR', 'Turkey', '90'], ['UG', 'Uganda', '256'],
  ['UA', 'Ukraine', '380'], ['AE', 'United Arab Emirates', '971'], ['GB', 'United Kingdom', '44'], ['US', 'United States', '1'],
  ['UZ', 'Uzbekistan', '998'], ['VN', 'Vietnam', '84'],
];
// The usual country of a calling code (+1: United States, +7: Russia).
const CALLING_CODES = {};
for (const [iso, name, code] of COUNTRIES) if (!CALLING_CODES[code] || iso === 'US' || iso === 'RU') CALLING_CODES[code] = [iso, name];

function countryOf(num, prefix) {
  if (prefix && CALLING_CODES[prefix]) return CALLING_CODES[prefix];
  let n = String(num || '').replace(/[^0-9+]/g, '');
  n = n.replace(/^\+/, '').replace(/^00/, '');
  for (const len of [3, 2, 1]) {
    const c = CALLING_CODES[n.slice(0, len)];
    if (c && n.length > len) return c;
  }
  return null;
}
const didCountry = (d) => countryOf(d.destination_number, d.destination_prefix);

// countryOptions: the choices of a DID's Country field (value: calling code).
function countryOptions() {
  const seen = new Set();
  return COUNTRIES.filter(([, , code]) => !seen.has(code) && seen.add(code)).map(([iso, name, code]) =>
    [code, name + (code === '1' ? ' / Canada' : code === '7' ? ' / Kazakhstan' : '') + ' (+' + code + ')']).sort((a, b) => a[1].localeCompare(b[1]));
}

// Flags are emoji; where the system has no flag emoji (Windows) the
// country code shows instead.
let flagSupport = null;
function canFlag() {
  if (flagSupport !== null) return flagSupport;
  try {
    const c = document.createElement('canvas');
    c.width = c.height = 16;
    const x = c.getContext('2d');
    x.font = '14px sans-serif';
    x.fillText('\u{1F1FA}\u{1F1F8}', 0, 14);
    const d = x.getImageData(0, 0, 16, 16).data;
    flagSupport = false;
    for (let i = 0; i < d.length; i += 4) if (d[i + 3] && (d[i] !== d[i + 1] || d[i + 1] !== d[i + 2])) { flagSupport = true; break; }
  } catch (e) { flagSupport = false; }
  return flagSupport;
}

function flag(num, prefix) {
  const c = countryOf(num, prefix);
  if (!c) return h('span', { class: 'cf-flag none', title: t('Unknown country') }, '—');
  const emoji = String.fromCodePoint(...[...c[0]].map((ch) => 0x1F1E6 + ch.charCodeAt(0) - 65));
  return canFlag() ? h('span', { class: 'cf-flag', title: c[1] }, emoji) : h('span', { class: 'cf-flag code', title: c[1] }, c[0]);
}

const CF_KINDS = [
  { id: 'ivr', title: 'Interactive Voice Response', list: 'IVR Modules', groups: ['IVR menu', 'AI agent'], noun: 'an IVR or AI agent',
    add: [['ivrs', 'IVR menu'], ['aiagents', 'AI agent']],
    drop: 'Drag a DID number here to assign it to an IVR or an AI agent' },
  { id: 'queue', title: 'Queue', list: 'Existing Queues', groups: ['Queue'], noun: 'a queue', add: [['queues', 'Queue']],
    drop: 'Drag a DID number here to assign it to a queue' },
  { id: 'extension', title: 'Extension', list: 'Existing Extensions', groups: ['Extension'], noun: 'an extension', add: [['extensions', 'Extension']],
    drop: 'Drag a DID number here to assign it to an extension' },
  { id: 'other', title: 'Others', list: 'Existing Modules', groups: ['Ring group', 'Conference', 'Time condition', 'Voicemail', 'Fax', 'eFax', 'Other'], noun: 'a ring group, conference, office hours, voicemail, fax or eFax',
    // the window that opens on a drop also offers every extension, as eFax
    pick: ['Ring group', 'Conference', 'Time condition', 'Fax', 'eFax', 'Voicemail', 'Other'],
    add: [['ringgroups', 'Ring group'], ['conferences', 'Conference room'], ['timeconditions', 'Office hours'], ['faxes', 'Fax extension']],
    drop: 'Drag a DID number here to assign it to a ring group, conference, office hours, voicemail, fax or eFax (an extension receiving faxes)' },
];
const CF_TAGS = { 'Ring group': 'Group', Conference: 'Conf', 'Time condition': 'Hours', Voicemail: 'VM', Fax: 'Fax', eFax: 'eFax', Extension: 'Ext', Other: '',
  'IVR menu': 'IVR', 'AI agent': 'AI' };
// Cards holding more than one kind of module tag each one.
const cfTagged = (k) => (k.pick || k.groups).length > 1;

function cfPref(k, def) { try { return localStorage.getItem('rq-cf-' + k) || def; } catch (e) { return def; } }
function cfSetPref(k, v) { try { localStorage.setItem('rq-cf-' + k, v); } catch (e) { /* private mode */ } }

// formModal shows fields (like the full forms) in a window; after(widgets
// by field key) can wire them together.
function formModal(title, fields, row, okLabel, save, after) {
  const ws = fields.map((f) => [f, widget(Object.assign({}, f, { o: typeof f.o === 'function' ? f.o() : f.o }), row[f.k] !== undefined ? row[f.k] : (f.def || ''))]);
  const body = h('div', { class: 'form cf-form' }, ws.map(([f, w]) => h('div', { class: 'field' + (f.full ? ' full' : '') },
    h('label', { class: f.req ? 'req' : null }, f.l), w.el, f.help ? h('div', { class: 'help' }, f.help) : null)));
  modal(title, body, okLabel || 'Save', async () => {
    const v = {};
    ws.forEach(([f, w]) => { v[f.k] = w.get(); });
    await save(v);
  });
  if (after) after(Object.fromEntries(ws.map(([f, w]) => [f.k, w])));
}

const CF_TRUNK_FIELDS = [
  { k: 'gateway', l: 'Name', req: 1, ph: 'e.g. MyCarrier' },
  { k: 'proxy', l: 'Provider address', req: 1, ph: 'sip.provider.com or 203.0.113.10:5060' },
  { k: 'username', l: 'Username', req: 1 },
  { k: 'password', l: 'Password', t: 'password' },
  { k: 'auth_username', l: 'Authentication username', help: 'Only if the provider gave a different one.' },
  { k: 'register_transport', l: 'Transport', t: 'select', o: [['udp', 'UDP'], ['tcp', 'TCP']] },
  { k: 'expire_seconds', l: 'Registration every (seconds)', t: 'number' },
  { k: 'caller_id_in_from', l: 'Caller ID in From header', t: 'bool', on: 'Yes' },
  { k: 'enabled', l: 'Enabled', t: 'bool', on: 'Enabled' },
  { k: 'description', l: 'Description', full: 1 },
];
const CF_GATEWAY_FIELDS = [
  { k: 'gateway', l: 'Name', req: 1, ph: 'e.g. Carrier SBC' },
  { k: 'proxy', l: 'Address', req: 1, ph: '203.0.113.10 or sbc.carrier.com:5060', help: 'Calls from this address are accepted without a password (it trusts your IP).' },
  { k: 'from_domain', l: 'From domain', help: 'Usually leave empty.' },
  { k: 'caller_id_in_from', l: 'Caller ID in From header', t: 'bool', on: 'Yes' },
  { k: 'enabled', l: 'Enabled', t: 'bool', on: 'Enabled' },
  { k: 'description', l: 'Description', full: 1 },
];
const CF_DID_FIELDS = [
  { k: 'destination_prefix', l: 'Country', t: 'select', o: () => countryOptions(), blank: '— choose the country —',
    help: 'Calls match the number with or without this country code (+65 6123 4567, 6561234567 or 61234567).' },
  { k: 'destination_number', l: 'DID number', req: 1, ph: 'e.g. 6531591234', help: 'As the trunk sends it (with or without +).' },
  { k: 'gateway_uuid', l: 'Trunk', t: 'gateway', blank: 'Any trunk' },
  { k: 'destination_record', l: 'Record calls', t: 'bool', on: 'Record every call to this number' },
  { k: 'destination_description', l: 'Description', full: 1 },
];

PAGES.callflow = async () => {
  const main = $('#main');
  let view = cfPref('view', '1');
  let tab = cfPref('tab', 'sip');
  let hidden = new Set();
  try { hidden = new Set(JSON.parse(cfPref('hidden', '[]'))); } catch (e) { /* default */ }
  let big = '';
  let dragged = null; // the DID being dragged
  let gws = [];
  let dids = [];
  const body = h('div');

  async function load() {
    [gws, dids] = await Promise.all([api('GET', dpath('gateways')), api('GET', dpath('dids')), loadMeta(), loadLive()]);
  }
  const reload = async () => { await load(); draw(); };
  const dests = () => (S.meta ? S.meta.destinations : []);
  const destOf = (action) => dests().find((d) => d.value === action);
  const kindOf = (d) => {
    if (!d.action) return null;
    const x = destOf(d.action);
    const k = x && CF_KINDS.find((k) => k.groups.includes(x.group));
    return k ? k.id : 'other';
  };
  const targetLabel = (d) => {
    const x = destOf(d.action);
    if (!x) return destLabel(d.action);
    return x.group === 'eFax' ? t('eFax') + ' · ' + x.label : x.label;
  };
  const gwName = (uuid) => { const g = gws.find((x) => x.gateway_uuid === uuid); return g ? g.gateway : t('Any trunk'); };
  const registers = (g) => truthy(g.register === '' ? 'true' : g.register);

  // ---- changes
  const saveGateway = async (id, v, register) => {
    const bodyV = Object.assign({}, v, { register: register ? 'true' : 'false' });
    if (id) await api('PUT', dpath('gateways/' + id), bodyV); else await api('POST', dpath('gateways'), bodyV);
    toast(t('Saved'));
    await reload();
  };
  const editGateway = (g, register) => {
    const fields = register ? CF_TRUNK_FIELDS : CF_GATEWAY_FIELDS;
    const row = g || { register_transport: 'udp', expire_seconds: '800', enabled: 'true', caller_id_in_from: 'false' };
    formModal(g ? t('Edit') + ' ' + g.gateway : t(register ? 'New SIP trunk' : 'New gateway'), fields, row, 'Save', (v) => saveGateway(g && g.gateway_uuid, v, register));
  };
  const deleteGateway = async (g) => {
    if (dids.some((d) => d.gateway_uuid === g.gateway_uuid) && !confirm(t('Some DIDs arrive on this trunk; they will accept calls from any trunk.') + ' ' + t('Delete?'))) return;
    if (!confirm(t('Delete') + ' ' + g.gateway + '?')) return;
    try { await api('DELETE', dpath('gateways/' + g.gateway_uuid)); toast(t('Deleted')); await reload(); } catch (e) { fail(e); }
  };
  const editDid = (d) => {
    const fields = d ? CF_DID_FIELDS.concat([{ k: 'action', l: 'Send calls to', t: 'dest', efax: 1, help: 'Or drag the number onto IVR, Queue, Extension or Others (eFax: an extension receiving faxes).' }]) : CF_DID_FIELDS;
    formModal(d ? t('Edit') + ' ' + d.destination_number : t('New DID number'), fields, d || { destination_record: 'false' }, 'Save', async (v) => {
      if (d) await api('PUT', dpath('dids/' + d.destination_uuid), v);
      else await api('POST', dpath('dids'), Object.assign({ destination_enabled: 'true', action: '' }, v));
      toast(t('Saved'));
      await reload();
    }, (w) => {
      // A full number picks its country (when none is chosen yet).
      const num = w.destination_number.el, sel = w.destination_prefix.el;
      num.addEventListener('input', () => {
        if (sel.value) return;
        const n = num.value.replace(/[^0-9]/g, '');
        for (const len of [3, 2, 1]) {
          if (n.length > len + 5 && [...sel.options].some((o) => o.value === n.slice(0, len))) { sel.value = n.slice(0, len); break; }
        }
      });
    });
  };
  const deleteDid = async (d) => {
    if (!confirm(t('Delete') + ' ' + d.destination_number + '?')) return;
    try { await api('DELETE', dpath('dids/' + d.destination_uuid)); toast(t('Deleted')); await reload(); } catch (e) { fail(e); }
  };
  const link = async (d, action) => {
    await api('PUT', dpath('dids/' + d.destination_uuid), { action });
    toast(action ? t('Assigned') + ': ' + d.destination_number + ' → ' + (destOf(action) || { label: destLabel(action) }).label : t('Back to Available DID Numbers') + ': ' + d.destination_number);
    await reload();
  };
  // Back to Available DID Numbers (dragged back, or the bin on the number).
  const unlink = (d) => {
    modal(t('Back to Available DID Numbers') + ': ' + d.destination_number, h('div', null,
      h('p', { class: 'muted cf-pick-intro' }, flag(d.destination_number, d.destination_prefix), ' ', d.destination_number, ' → ', targetLabel(d)),
      h('p', null, t('It stops going to') + ' ' + targetLabel(d) + '. ' + t('Calls to it are not answered until you assign it again.'))),
    t('Move back'), () => link(d, ''));
  };
  // The window that opens on a drop: which IVR / queue / extension / other
// to assign the number to.
  const assign = (d, kindId) => {
    const kind = CF_KINDS.find((k) => k.id === kindId);
    const groups = kind.pick || kind.groups;
    const targets = dests().filter((x) => groups.includes(x.group)).sort((a, b) => groups.indexOf(a.group) - groups.indexOf(b.group));
    let chosen = destOf(d.action) && groups.includes(destOf(d.action).group) ? d.action : (targets.length === 1 ? targets[0].value : '');
    const q = h('input', { type: 'search', placeholder: t('Search'), 'aria-label': t('Search') });
    const list = h('div', { class: 'cf-pick' });
    const drawList = () => {
      const f = q.value.trim().toLowerCase();
      const shown = targets.filter((x) => (x.label + ' ' + x.group).toLowerCase().includes(f));
      fill(list, shown.length ? shown.map((x) => h('label', { class: 'cf-pick-row' + (x.value === chosen ? ' on' : '') },
        h('input', { type: 'radio', name: 'cf-target', value: x.value, checked: x.value === chosen, onchange: () => { chosen = x.value; drawList(); } }),
        cfTagged(kind) && CF_TAGS[x.group] ? h('span', { class: 'cf-tag t-' + CF_TAGS[x.group].toLowerCase() }, CF_TAGS[x.group]) : null,
        h('span', null, x.label))) : h('div', { class: 'muted' }, targets.length ? t('Nothing found.') : t('None yet: create one first with +.')));
    };
    const efaxNote = kind.id === 'other' && targets.some((x) => x.group === 'eFax')
      ? h('p', { class: 'muted small cf-pick-note' }, h('span', { class: 'cf-tag t-efax' }, 'eFax'), ' ',
        t('An extension under eFax receives faxes on this number: it answers as a fax machine and the fax arrives in the eFax Inbox. To ring the extension instead, drop the number on Extension.'))
      : null;
    q.addEventListener('input', drawList);
    drawList();
    modal(t('Assign') + ' ' + d.destination_number + ' ' + t('to') + ' ' + t(kind.noun), h('div', null,
      h('p', { class: 'muted cf-pick-intro' }, flag(d.destination_number, d.destination_prefix), ' ', d.destination_number, ' · ', gwName(d.gateway_uuid)),
      targets.length > 6 ? q : null, list, efaxNote), t('Assign'), async () => {
      if (!chosen) { toast(t('Choose one'), true); return false; }
      await link(d, chosen);
    });
  };
  // A click on an available DID (no dragging): first which kind.
  const chooseKind = (d) => {
    const close = modal(t('Assign') + ' ' + d.destination_number, h('div', { class: 'cf-kinds' }, CF_KINDS.map((k) =>
      h('button', { type: 'button', class: 'cf-kind-btn', onclick: () => { close(); assign(d, k.id); } }, icon({ ivr: 'ivr', queue: 'list', extension: 'user', other: 'apps' }[k.id], 20), t(k.title)))),
    false);
  };
  const customize = () => {
    const cards = [['trunks', 'Trunks'], ['available', 'Available DID Numbers']].concat(CF_KINDS.map((k) => [k.id, k.title]));
    const boxes = cards.map(([id, label]) => [id, h('input', { type: 'checkbox', checked: !hidden.has(id) }), label]);
    modal(t('Customize'), h('div', { class: 'cf-custom' }, h('p', { class: 'muted' }, t('Cards shown on Call Flow:')),
      boxes.map(([, cb, label]) => h('label', { class: 'switch' }, cb, t(label)))), 'Save', () => {
      hidden = new Set(boxes.filter(([, cb]) => !cb.checked).map(([id]) => id));
      cfSetPref('hidden', JSON.stringify([...hidden]));
      draw();
    });
  };

  // ---- drawing
  const iconBtn = (ic, title, fn) => h('button', { type: 'button', class: 'icon-btn cf-act', title: t(title), 'aria-label': t(title), onclick: fn }, icon(ic, 17));
  const expandBtn = (id) => iconBtn(big === id ? 'shrink' : 'expand', big === id ? 'Back' : 'Expand', () => { big = big === id ? '' : id; draw(); });
  const card = (id, title, actions, content, cls) => h('section', { class: 'cf-card ' + (cls || '') + (big === id ? ' big' : ''), 'data-card': id },
    h('div', { class: 'cf-head' }, title, h('span', { class: 'spacer' }), actions, expandBtn(id)), content);
  const toggle = (on, fn) => { const cb = h('input', { type: 'checkbox', checked: on }); cb.addEventListener('change', () => fn(cb.checked)); return h('label', { class: 'cf-toggle' }, cb, h('span')); };
  const setEnabled = async (g, on) => { try { await api('PUT', dpath('gateways/' + g.gateway_uuid), { enabled: String(on) }); toast(t(on ? 'Enabled' : 'Disabled')); await reload(); } catch (e) { fail(e); } };
  const table = (head, rows, empty) => h('div', { class: 'cf-table' }, h('table', null,
    h('thead', null, h('tr', null, head.map((x) => h('th', null, t(x))))),
    h('tbody', null, rows.length ? rows : h('tr', null, h('td', { colspan: String(head.length), class: 'cf-empty' }, t(empty))))));

  function trunksCard() {
    const seg = h('div', { class: 'cf-seg' }, [['sip', 'SIP Trunk'], ['gw', 'Gateway'], ['did', 'DID']].map(([k, l]) =>
      h('button', { type: 'button', class: tab === k ? 'on' : '', onclick: () => { tab = k; cfSetPref('tab', k); draw(); } }, t(l))));
    let content;
    if (tab === 'did') {
      content = table(['Trunk', 'DID Number', 'Country', 'Goes to', 'Action'], dids.map((d) => h('tr', null,
        h('td', null, gwName(d.gateway_uuid)), h('td', { class: 'mono' }, d.destination_number),
        h('td', null, flag(d.destination_number, d.destination_prefix), ' ', h('span', { class: 'muted small' }, (didCountry(d) || ['', ''])[1])),
        h('td', null, d.action ? targetLabel(d) : h('span', { class: 'muted' }, t('Not assigned'))),
        h('td', { class: 'cf-row-acts' }, iconBtn('edit', 'Edit', () => editDid(d)), iconBtn('x', 'Delete', () => deleteDid(d))))), 'No DID numbers yet. Use + to add one.');
    } else {
      const reg = tab === 'sip';
      const list = gws.filter((g) => registers(g) === reg);
      content = table(reg ? ['Status', 'Trunk', 'Register', 'Enable', 'Action', 'Delete'] : ['Status', 'Gateway', 'Address', 'Enable', 'Action', 'Delete'],
        list.map((g) => h('tr', null,
          h('td', null, gatewayState(g.gateway_uuid, g)),
          h('td', null, h('b', null, g.gateway), reg ? h('div', { class: 'muted small' }, g.proxy) : null),
          h('td', null, reg ? yesNo(g.register) : g.proxy),
          h('td', null, toggle(truthy(g.enabled === '' ? 'true' : g.enabled), (on) => setEnabled(g, on))),
          h('td', null, iconBtn('edit', 'Edit', () => editGateway(g, reg))),
          h('td', null, iconBtn('trash', 'Delete', () => deleteGateway(g))))),
        reg ? 'No SIP trunks yet. Use + to add one.' : 'No gateways yet. Use + to add one.');
    }
    const add = iconBtn('plus', { sip: 'New SIP trunk', gw: 'New gateway', did: 'New DID number' }[tab], () => (tab === 'did' ? editDid(null) : editGateway(null, tab === 'sip')));
    const dl = tab === 'did' ? iconBtn('download', 'Export CSV', () => downloadCSV('dids.csv', ['Trunk', 'DID', 'Country', 'Goes to'],
      dids.map((d) => [gwName(d.gateway_uuid), d.destination_number, (didCountry(d) || ['', ''])[1], d.action ? targetLabel(d) : '']))) : null;
    return card('trunks', h('h2', null, t('Trunks')), [seg, add, dl], content, 'cf-trunks');
  }

  const chip = (d, assigned) => {
    const el = h('div', { class: 'cf-chip' + (assigned ? ' linked' : ''), draggable: 'true', 'data-did': d.destination_number,
      title: t(assigned ? 'Drag to another card, or back to Available DID Numbers' : 'Drag onto IVR, Queue, Extension or Others (or click)') },
      flag(d.destination_number, d.destination_prefix),
      h('div', { class: 'cf-chip-txt' }, h('b', null, d.destination_number),
        h('span', null, assigned ? '→ ' + targetLabel(d) : gwName(d.gateway_uuid))),
      assigned ? h('button', { type: 'button', class: 'icon-btn cf-unlink', title: t('Back to Available DID Numbers'), 'aria-label': t('Back to Available DID Numbers'),
        onclick: (e) => { e.stopPropagation(); unlink(d); } }, icon('trash', 15)) : null);
    el.addEventListener('dragstart', (e) => {
      dragged = d;
      e.dataTransfer.setData('text/plain', d.destination_uuid);
      e.dataTransfer.effectAllowed = 'move';
      el.classList.add('dragging');
      document.body.classList.add('cf-dragging', assigned ? 'cf-dragging-linked' : 'cf-dragging-free');
    });
    el.addEventListener('dragend', () => {
      dragged = null;
      el.classList.remove('dragging');
      document.body.classList.remove('cf-dragging', 'cf-dragging-linked', 'cf-dragging-free');
      $$('.cf-drop.over, .cf-avail.over').forEach((z) => z.classList.remove('over'));
    });
    if (!assigned) el.addEventListener('click', () => chooseKind(d));
    return el;
  };

  // A drop zone both ways: a linked number dragged back here is unlinked.
  function availableCard() {
    const free = dids.filter((d) => !d.action);
    const zone = h('div', { class: 'cf-avail' }, free.length ? free.map((d) => chip(d, false)) : h('div', { class: 'cf-empty' }, t('Every DID number is assigned. Drag one back here to make it available again.')),
      h('div', { class: 'cf-avail-hint' }, icon('down', 16), t('Drop here to make the number available again')));
    const back = () => dragged && dragged.action;
    zone.addEventListener('dragover', (e) => { if (!back()) return; e.preventDefault(); e.dataTransfer.dropEffect = 'move'; zone.classList.add('over'); });
    zone.addEventListener('dragleave', (e) => { if (!zone.contains(e.relatedTarget)) zone.classList.remove('over'); });
    zone.addEventListener('drop', (e) => {
      e.preventDefault();
      zone.classList.remove('over');
      const d = back() ? dragged : null;
      if (d) unlink(d);
    });
    return card('available', h('h2', null, t('Available DID Numbers')), [iconBtn('refresh', 'Refresh', reload)], zone);
  }

  function kindCard(k) {
    const linked = dids.filter((d) => kindOf(d) === k.id);
    const zone = h('div', { class: 'cf-drop' }, linked.length ? linked.map((d) => chip(d, true)) : h('div', { class: 'cf-drop-hint' }, t(k.drop)));
    zone.addEventListener('dragover', (e) => { if (!dragged) return; e.preventDefault(); e.dataTransfer.dropEffect = 'move'; zone.classList.add('over'); });
    zone.addEventListener('dragleave', (e) => { if (!zone.contains(e.relatedTarget)) zone.classList.remove('over'); });
    zone.addEventListener('drop', (e) => {
      e.preventDefault();
      zone.classList.remove('over');
      const d = dragged || dids.find((x) => x.destination_uuid === e.dataTransfer.getData('text/plain'));
      if (d) assign(d, k.id);
    });
    // eFax is every extension: chosen when a number is dropped, not listed
    const mods = dests().filter((x) => k.groups.includes(x.group) && x.group !== 'Other' && x.group !== 'eFax');
    const items = mods.map((x) => h('div', { class: 'cf-mod', title: x.label },
      cfTagged(k) && CF_TAGS[x.group] ? h('span', { class: 'cf-tag t-' + CF_TAGS[x.group].toLowerCase() }, CF_TAGS[x.group]) : null,
      h('span', null, x.label)));
    if (k.id === 'other' && dests().some((x) => x.group === 'eFax')) {
      items.push(h('div', { class: 'cf-mod', title: t('Choose the extension when you drop a number here') },
        h('span', { class: 'cf-tag t-efax' }, 'eFax'), h('span', null, t('Any extension (receives faxes)'))));
    }
    const side = h('div', { class: 'cf-mods' }, h('div', { class: 'cf-mods-head' }, t(k.list)),
      h('div', { class: 'cf-mods-list' }, items.length ? items : h('div', { class: 'cf-mod muted' }, t('None yet'))));
    // + opens the new module's form in a window; the card shows it at once.
    const create = (page) => resourceModal(page, null, reload).catch(fail);
    const add = iconBtn('plus', 'New', () => {
      if (k.add.length === 1) { create(k.add[0][0]); return; }
      const close = modal(t('New'), h('div', { class: 'cf-kinds' }, k.add.map(([page, label]) =>
        h('button', { type: 'button', class: 'cf-kind-btn', onclick: () => { close(); create(page); } },
          icon({ ivrs: 'ivr', aiagents: 'bot', ringgroups: 'users', conferences: 'team', timeconditions: 'clock', faxes: 'fax' }[page] || 'plus', 20), t(label)))), false);
    });
    return card(k.id, h('h2', null, t(k.title)), [add], h('div', { class: 'cf-kind-body' }, zone, side), 'cf-kind');
  }

  function view2() {
    return h('section', { class: 'cf-card' }, h('div', { class: 'cf-head' }, h('h2', null, t('Where each number goes')), h('span', { class: 'spacer' }),
      iconBtn('plus', 'New DID number', () => editDid(null))),
    table(['DID Number', 'Country', 'Trunk', 'Goes to', 'Action'], dids.map((d) => {
      const k = CF_KINDS.find((x) => x.id === kindOf(d));
      return h('tr', null, h('td', { class: 'mono' }, d.destination_number), h('td', null, flag(d.destination_number, d.destination_prefix)), h('td', null, gwName(d.gateway_uuid)),
        h('td', null, k ? [h('span', { class: 'cf-tag t-' + k.id }, t(k.title)), ' ', targetLabel(d)] : h('span', { class: 'muted' }, t('Not assigned'))),
        h('td', { class: 'cf-row-acts' }, h('button', { type: 'button', class: 'btn small', onclick: () => chooseKind(d) }, t(d.action ? 'Change' : 'Assign')),
          d.action ? h('button', { type: 'button', class: 'btn small', onclick: () => unlink(d) }, t('Unlink')) : null,
          iconBtn('edit', 'Edit', () => editDid(d))));
    }), 'No DID numbers yet. Use + to add one.'));
  }

  function draw() {
    const viewSeg = h('div', { class: 'cf-views' }, [['1', 'View 1'], ['2', 'View 2']].map(([v, l]) =>
      h('button', { type: 'button', class: view === v ? 'on' : '', onclick: () => { view = v; cfSetPref('view', v); draw(); } }, t(l))));
    const show = (id) => !hidden.has(id) && (!big || big === id);
    const left = [show('trunks') ? trunksCard() : null, show('available') ? availableCard() : null];
    const right = CF_KINDS.filter((k) => show(k.id)).map(kindCard);
    fill(body, view === '2' ? view2() : h('div', { class: 'cf-grid' + (big ? ' one' : '') }, h('div', { class: 'cf-col' }, left), h('div', { class: 'cf-col' }, right)));
    fill(main, h('div', { class: 'page-head' }, h('h1', null, t('Call Flow'), h('span', { class: 'info-dot', title: t('Trunks, DID numbers and where calls to each number go') }, icon('info', 15))),
      viewSeg, h('div', { class: 'spacer' }),
      view === '1' ? h('button', { type: 'button', class: 'btn', onclick: customize }, icon('sliders', 16), t('Customize')) : null), body);
  }

  fill(main, h('div', { class: 'muted' }, t('Loading…')));
  await load();
  draw();
};
