'use strict';
// Security settings (as in the RingQ portal): countries callers may call,
// PIN-based dialing, who may open the web interface (and its auto logout),
// anti-hacking address rules, and SIP Guard (protections that ban
// addresses after repeated failures). Owners see their company's tabs; the
// server-wide tabs need server access.

// flagOf is a country's flag (drawn with the embedded flag font where the
// system has no flag emoji).
function flagOf(iso) {
  if (!/^[A-Z]{2}$/.test(iso)) return h('span', { class: 'sec-flag sec-flag-ic' }, icon('globe', 15));
  return h('span', { class: 'sec-flag', 'aria-hidden': 'true' }, String.fromCodePoint(...[...iso].map((c) => 0x1F1E6 + c.charCodeAt(0) - 65)));
}

// span is a duration as RingQ writes it: 45s, 5m, 2h, 1d.
function span(sec) {
  sec = Number(sec) || 0;
  if (!sec) return '—';
  if (sec % 86400 === 0) return sec / 86400 + 'd';
  if (sec % 3600 === 0) return sec / 3600 + 'h';
  if (sec % 60 === 0) return sec / 60 + 'm';
  return sec + 's';
}

// durationInput edits seconds as a number and a unit.
function durationInput(sec, units) {
  units = units || [['60', 'minutes'], ['3600', 'hours'], ['86400', 'days']];
  let unit = units[0][0];
  for (const [u] of units) if (sec % Number(u) === 0) unit = u;
  const num = h('input', { type: 'number', min: '1', value: String(Math.max(1, Math.round(sec / Number(unit)))), class: 'sec-num' });
  const sel = selectOf(units.map(([u, l]) => [u, t(l)]), unit);
  return { el: h('div', { class: 'sec-dur' }, num, sel), get: () => Number(num.value) * Number(sel.value) };
}

PAGES.security = async (parts) => {
  const main = $('#main');
  const server = !!S.me.server_access;
  const tabs = [['countries', 'Allowed countries'], ['pin', 'PIN-based dialing'], ['webapp', 'Webapp & mobile restriction']];
  if (server) tabs.push(['antihacking', 'Anti-hacking', 'shield'], ['sipguard', 'SIP Guard', 'shieldcheck']);
  let tab = tabs.some(([k]) => k === (parts && parts[0])) ? parts[0] : 'countries';
  const bar = h('div', { class: 'ai-tabs sec-tabs', role: 'tablist' });
  const body = h('div', { class: 'sec-body' });
  const show = async (k) => {
    tab = k;
    history.replaceState(null, '', '#/security/' + k);
    fill(bar, tabs.map(([key, label, ic]) => h('button', { type: 'button', role: 'tab', class: key === tab ? 'on' : '', 'aria-selected': String(key === tab),
      onclick: () => show(key) }, ic ? icon(ic, 14) : null, t(label))));
    fill(body, h('div', { class: 'muted' }, t('Loading…')));
    try { await SEC_TABS[k](body); } catch (e) { fill(body, h('div', { class: 'panel' }, h('p', { class: 'error-text' }, e.message))); }
  };
  fill(main,
    h('div', { class: 'page-head' }, h('h1', null, t('Security settings'), ' ',
      h('span', { class: 'info-dot', title: t('Toll fraud protection, PINs, web access and SIP Guard') }, icon('info', 15))),
    h('div', { class: 'spacer' }), h('button', { type: 'button', class: 'btn', onclick: () => history.back() }, icon('left', 15), t('Back'))),
    bar, body);
  await show(tab);
};

const SEC_TABS = {};

// ---------------------------------------------------------------------------
// Allowed countries

SEC_TABS.countries = async (body) => {
  const d = await api('GET', dpath('dialsecurity'));
  let blocked = new Set(d.blocked);
  const saved = () => JSON.stringify([...blocked].sort()) === JSON.stringify([...d.blocked].sort());
  let cont = d.continents[0];
  const subtabs = h('div', { class: 'sec-seg', role: 'tablist' });
  const grid = h('div', { class: 'sec-countries' });
  const all = h('div', { class: 'sec-all' });
  const save = h('button', { type: 'button', class: 'btn primary', disabled: true }, t('Save'));
  const cancel = h('button', { type: 'button', class: 'btn', disabled: true }, t('Cancel'));
  const draw = () => {
    fill(subtabs, d.continents.map((c) => h('button', { type: 'button', role: 'tab', class: c === cont ? 'on' : '', 'aria-selected': String(c === cont),
      onclick: () => { cont = c; draw(); } }, t(c))));
    const list = d.countries.filter((c) => c.continent === cont);
    const allowedN = list.filter((c) => !blocked.has(c.iso)).length;
    const cb = h('input', { type: 'checkbox', checked: allowedN === list.length, 'aria-label': t('Select all') + ' (' + t(cont) + ')' });
    cb.indeterminate = allowedN > 0 && allowedN < list.length;
    cb.addEventListener('change', () => { for (const c of list) if (cb.checked) blocked.delete(c.iso); else blocked.add(c.iso); draw(); });
    fill(all, h('div', { class: 'sec-all-top' }, h('span', { class: 'sec-all-ic' }, icon('globe', 18)), cb),
      h('b', null, t('Select all') + ' (' + t(cont) + ')'), h('small', null, allowedN + ' ' + t('of') + ' ' + list.length + ' ' + t('allowed')));
    fill(grid, list.map((c) => {
      const box = h('input', { type: 'checkbox', checked: !blocked.has(c.iso), 'aria-label': c.name });
      box.addEventListener('change', () => { if (box.checked) blocked.delete(c.iso); else blocked.add(c.iso); draw(); });
      return h('label', { class: 'sec-country' + (blocked.has(c.iso) ? ' off' : '') }, flagOf(c.iso),
        h('span', { class: 'sec-country-txt' }, h('span', null, c.name), h('small', null, c.code)), box);
    }));
    save.disabled = cancel.disabled = saved();
  };
  save.addEventListener('click', async () => {
    save.disabled = true;
    try {
      const r = await api('PUT', dpath('dialsecurity/countries'), { blocked: [...blocked] });
      d.blocked = [...blocked];
      toast(r.blocked ? r.blocked + ' ' + t('countries blocked') : t('All countries allowed'));
    } catch (e) { fail(e); }
    draw();
  });
  cancel.addEventListener('click', () => { blocked = new Set(d.blocked); draw(); });
  draw();
  fill(body, subtabs, h('div', { class: 'sec-country-wrap' }, all, grid),
    h('p', { class: 'help' }, t('Calls out through your trunks to a country that is not ticked are refused (the caller hears that calls to that country are not allowed). Numbers dialled with + or 00 (011 in North America) are checked; local numbers always go through.')),
    h('div', { class: 'form-actions sec-actions' }, cancel, save));
};

// ---------------------------------------------------------------------------
// PIN-based dialing

SEC_TABS.pin = async (body) => {
  await loadMeta();
  const d = await api('GET', dpath('dialsecurity'));
  const pin = d.pin;
  let mode = pin.mode || '';
  const prefixes = h('input', { value: (pin.prefixes || ['00']).join(', '), placeholder: '00', 'aria-label': t('Prefixes') });
  const modes = [['', 'No PIN', 'Calls out go through without a PIN.'], ['prefix', 'Numbers starting with', 'Only calls to numbers that start with these prefixes need a PIN (00: international calls; + is treated as 00).'],
    ['all', 'Every call out', 'Every call through your trunks needs a PIN.']];
  const cards = h('div', { class: 'sec-modes' });
  const drawModes = () => fill(cards, modes.map(([v, l, help]) => {
    const r = h('input', { type: 'radio', name: 'pin-mode', value: v, checked: v === mode });
    r.addEventListener('change', () => { mode = v; drawModes(); });
    return h('label', { class: 'sec-mode' + (v === mode ? ' on' : '') }, h('span', { class: 'sec-mode-top' }, r, h('b', null, t(l))),
      h('small', { class: 'muted' }, t(help)), v === 'prefix' && v === mode ? prefixes : null);
  }));
  drawModes();
  let trusted = new Set(pin.trusted || []);
  const exts = (S.meta ? S.meta.extensions : []);
  const trustBox = h('div', { class: 'chip-pick sec-trust' }, exts.length ? exts.map((e) => {
    const cb = h('input', { type: 'checkbox', checked: trusted.has(e.extension) });
    cb.addEventListener('change', () => { if (cb.checked) trusted.add(e.extension); else trusted.delete(e.extension); });
    return h('label', { class: 'pick' }, cb, e.extension + (e.name ? ' ' + e.name : ''));
  }) : h('span', { class: 'muted small' }, t('No extensions yet.')));
  const saveRules = h('button', { type: 'button', class: 'btn primary' }, icon('check', 15), t('Save'));
  saveRules.addEventListener('click', async () => {
    if (mode && !d.pins.some((p) => truthy(p.enabled)) && !confirm(t('There is no PIN yet: calls that need one will not go through until you add a PIN below. Save anyway?'))) return;
    try {
      await api('PUT', dpath('dialsecurity/pin'), { mode, prefixes: prefixes.value.split(/[ ,;]+/).filter(Boolean), trusted: [...trusted] });
      toast(t('PIN-based dialing saved'));
    } catch (e) { fail(e); }
  });
  // The PINs.
  const list = h('div');
  const drawPins = () => fill(list, table([
    { label: 'PIN', render: (p) => h('code', null, p.pin) },
    { label: 'Account code', key: 'accountcode' },
    { label: 'Description', key: 'description', wrap: 1 },
    { label: 'Enabled', render: (p) => {
      const cb = h('input', { type: 'checkbox', checked: truthy(p.enabled), 'aria-label': t('Enabled') });
      cb.addEventListener('change', async () => {
        try { await api('PUT', dpath('dialsecurity/pins/' + p.uuid), { pin: p.pin, accountcode: p.accountcode, description: p.description, enabled: cb.checked }); p.enabled = String(cb.checked); } catch (e) { fail(e); cb.checked = !cb.checked; }
      });
      return cb;
    } },
    { label: '', actions: 1, render: (p) => h('button', { type: 'button', class: 'icon-btn', title: t('Delete'), 'aria-label': t('Delete') + ' ' + p.pin, onclick: async () => {
      if (!confirm(t('Delete the PIN') + ' ' + p.pin + '?')) return;
      try { await api('DELETE', dpath('dialsecurity/pins/' + p.uuid)); d.pins = d.pins.filter((x) => x !== p); drawPins(); } catch (e) { fail(e); }
    } }, icon('trash', 15)) },
  ], d.pins, { empty: t('No PINs yet.') }));
  drawPins();
  const np = h('input', { inputmode: 'numeric', placeholder: t('e.g. 4821'), maxlength: 16, 'aria-label': 'PIN' });
  const na = h('input', { placeholder: t('Optional'), maxlength: 40, 'aria-label': t('Account code') });
  const nd = h('input', { placeholder: t('e.g. Sales team'), maxlength: 64, 'aria-label': t('Description') });
  const add = h('button', { type: 'button', class: 'btn primary' }, icon('plus', 15), t('Add'));
  add.addEventListener('click', async () => {
    try {
      const r = await api('POST', dpath('dialsecurity/pins'), { pin: np.value.trim(), accountcode: na.value.trim(), description: nd.value.trim(), enabled: true });
      d.pins.push({ uuid: r.uuid, pin: np.value.trim(), accountcode: na.value.trim(), description: nd.value.trim(), enabled: 'true' });
      d.pins.sort((a, b) => a.pin.localeCompare(b.pin));
      np.value = na.value = nd.value = '';
      drawPins();
    } catch (e) { fail(e); }
  });
  fill(body,
    h('section', { class: 'panel' }, h('h2', null, t('Ask for a PIN')), cards,
      h('h3', { class: 'sec-h3' }, t('Extensions that never need a PIN')), trustBox,
      h('div', { class: 'form-actions' }, saveRules)),
    h('section', { class: 'panel' }, h('div', { class: 'panel-head' }, h('h2', null, t('PINs'))),
      h('div', { class: 'sec-addrow' }, h('label', null, h('span', null, 'PIN'), np), h('label', null, h('span', null, t('Account code')), na),
        h('label', { class: 'grow' }, h('span', null, t('Description')), nd), add),
      list,
      h('p', { class: 'help' }, t('The caller hears "Please enter your PIN, followed by the pound key" (three tries). The PIN and its account code are kept with the call in Call history.'))));
};

// ---------------------------------------------------------------------------
// Webapp & mobile restriction

// ipRules is an address list with Allow / Deny rules of two types.
function ipRules(d, types, opts) {
  const [allowT, denyT] = types;
  const box = h('div');
  let q = '';
  const action = selectOf([[denyT, t('Deny')], [allowT, t('Allow')]], denyT);
  action.setAttribute('aria-label', t('Action'));
  const ip = h('input', { placeholder: t('IP or CIDR (e.g. 192.168.1.0/24)'), 'aria-label': t('IP address') });
  const desc = h('input', { placeholder: t('Enter description'), maxlength: 64, 'aria-label': t('Description') });
  const count = h('small', { class: 'sec-count' }, '0/64');
  desc.addEventListener('input', () => { count.textContent = desc.value.length + '/64'; });
  const add = h('button', { type: 'button', class: 'btn primary' }, icon('plus', 15), t('Add'));
  add.addEventListener('click', async () => {
    try {
      await api('POST', 'security/rules', { type: action.value, value: ip.value.trim(), description: desc.value.trim() });
      ip.value = desc.value = '';
      count.textContent = '0/64';
      toast(t('Added'));
      await opts.reload();
    } catch (e) { fail(e); }
  });
  const search = h('input', { type: 'search', class: 'search-box sec-search', placeholder: t('Search IP'), 'aria-label': t('Search IP') });
  search.addEventListener('input', () => { q = search.value.trim().toLowerCase(); draw(); });
  const selected = new Set();
  const bulk = h('button', { type: 'button', class: 'btn small danger hidden' }, icon('trash', 14), t('Delete selected'));
  bulk.addEventListener('click', async () => {
    if (!confirm(t('Delete') + ' ' + selected.size + ' ' + t('rule(s)') + '?')) return;
    for (const id of selected) { try { await api('DELETE', 'security/rules/' + id); } catch (e) { fail(e); } }
    selected.clear();
    await opts.reload();
  });
  const draw = () => {
    const rows = d.rules.filter((r) => (r.type === allowT || r.type === denyT) && (!q || r.value.toLowerCase().includes(q) || (r.description || '').toLowerCase().includes(q)));
    bulk.classList.toggle('hidden', !selected.size);
    const head = h('input', { type: 'checkbox', 'aria-label': t('Select all'), checked: rows.length > 0 && rows.every((r) => selected.has(r.uuid)) });
    head.addEventListener('change', () => { rows.forEach((r) => (head.checked ? selected.add(r.uuid) : selected.delete(r.uuid))); draw(); });
    fill(box, h('div', { class: 'table-wrap sec-table' }, h('table', null,
      h('thead', null, h('tr', null, h('th', { class: 'sec-cb' }, head), h('th', null, t('IP address')), h('th', null, t('Status')), h('th', null, t('Description')), h('th', { class: 'num-cell' }, t('Action')))),
      h('tbody', null, rows.length ? rows.map((r) => {
        const cb = h('input', { type: 'checkbox', checked: selected.has(r.uuid), 'aria-label': r.value });
        cb.addEventListener('change', () => { if (cb.checked) selected.add(r.uuid); else selected.delete(r.uuid); draw(); });
        const allow = r.type === allowT;
        return h('tr', { class: truthy(r.enabled) ? '' : 'sec-off' }, h('td', { class: 'sec-cb' }, cb), h('td', null, h('b', null, r.value)),
          h('td', null, h('span', { class: 'sec-status ' + (allow ? 'allow' : 'deny') }, h('i'), t(allow ? 'Allow' : 'Deny'))),
          h('td', { class: 'wrap' }, r.description || '-'),
          h('td', { class: 'num-cell' }, h('button', { type: 'button', class: 'icon-btn', title: t('Delete'), 'aria-label': t('Delete') + ' ' + r.value, onclick: async () => {
            if (!confirm(t('Delete the rule for') + ' ' + r.value + '?')) return;
            try { await api('DELETE', 'security/rules/' + r.uuid); await opts.reload(); } catch (e) { fail(e); }
          } }, icon('trash', 15))));
      }) : h('tr', null, h('td', { colspan: 5, class: 'empty' }, t('No records found.')))))));
  };
  draw();
  return h('div', null,
    h('div', { class: 'panel sec-form' }, h('label', null, h('span', null, t('Action')), action), h('label', { class: 'grow' }, h('span', null, t('IP address')), ip),
      h('label', { class: 'grow' }, h('span', null, t('Description')), desc, count), add),
    h('div', { class: 'sec-tools' }, search, bulk), box, opts.help ? h('p', { class: 'help' }, opts.help) : null);
}

SEC_TABS.webapp = async (body) => {
  const server = !!S.me.server_access;
  let sub = server ? 'ip' : 'logout';
  const seg = h('div', { class: 'sec-seg' });
  const inner = h('div');
  const drawSub = async () => {
    fill(seg, (server ? [['ip', 'IP'], ['logout', 'Webapp auto logout']] : [['logout', 'Webapp auto logout']]).map(([k, l]) =>
      h('button', { type: 'button', class: k === sub ? 'on' : '', onclick: () => { sub = k; drawSub(); } }, t(l))),
    h('span', { class: 'info-dot', title: t('Who may open the web interface, the dialer and the API, and when idle sessions end') }, icon('info', 14)));
    if (sub === 'ip') {
      const reload = async () => { const d = await api('GET', 'security'); fill(inner, ipRules(d, ['web-allow', 'web-deny'], { reload,
        help: t('Deny: these addresses cannot open the web interface (nor the browser dialer and its API). Allow: when the list has Allow addresses, only they can open it. Your address now: ') + d.your_ip }));
      };
      await reload();
      return;
    }
    const d = await api('GET', dpath('dialsecurity'));
    const sel = selectOf(d.idle_choices.map((m) => [String(m), m ? (m < 60 ? m + ' ' + t('minutes') : m / 60 + ' ' + t(m === 60 ? 'hour' : 'hours')) : t('Off (stay signed in)')]), String(d.idle_logout));
    const save = h('button', { type: 'button', class: 'btn primary' }, icon('check', 15), t('Save'));
    save.addEventListener('click', async () => {
      try { await api('PUT', dpath('dialsecurity/autologout'), { minutes: Number(sel.value) }); S.me.idle_logout = Number(sel.value); IdleLogout.start(); toast(t('Saved')); } catch (e) { fail(e); }
    });
    fill(inner, h('div', { class: 'panel' }, h('h2', null, t('Webapp auto logout')),
      h('p', { class: 'muted' }, t('Sign people out of the web interface after this long without using it (no mouse, keyboard or touch). A minute before, they are asked whether they are still there. Nobody is signed out during a call.')),
      h('div', { class: 'sec-addrow' }, h('label', null, h('span', null, t('Sign out after')), sel), save)));
  };
  fill(body, seg, inner);
  await drawSub();
};

// ---------------------------------------------------------------------------
// Anti-hacking

SEC_TABS.antihacking = async (body) => {
  const reload = async () => {
    const d = await api('GET', 'security');
    const toggle = h('input', { type: 'checkbox', checked: d.restrict_sip, role: 'switch' });
    toggle.addEventListener('change', async () => {
      if (toggle.checked && !confirm(t('Only the addresses allowed below, your trunks and trusted carriers will be able to send SIP: phones registering from anywhere else stop working. Turn the global IP restriction on?'))) { toggle.checked = false; return; }
      try { await api('PUT', 'security/restrict', { on: toggle.checked }); toast(t(toggle.checked ? 'Global IP restriction on' : 'Global IP restriction off')); } catch (e) { fail(e); toggle.checked = !toggle.checked; }
    });
    const aclIP = h('input', { placeholder: t('Carrier address, e.g. 198.51.100.10'), 'aria-label': t('Carrier address') });
    const aclDesc = h('input', { placeholder: t('e.g. MyCarrier'), 'aria-label': t('Description') });
    const ua = h('input', { placeholder: t('e.g. friendly-scanner'), 'aria-label': t('User-Agent') });
    fill(body,
      h('div', { class: 'panel sec-toggle' }, h('label', { class: 'switch sec-switch' }, toggle, h('b', null, t('Enable global IP restriction'))),
        h('span', { class: 'info-dot', title: t('On: only the Allow addresses below, your trunks and trusted carriers may send SIP to this server.') }, icon('info', 14))),
      ipRules(d, ['ip-allow', 'ip-deny'], { reload,
        help: t('Deny: SIP from these addresses is ignored. Allow: never banned by SIP Guard, and allowed when the global IP restriction is on (your office, your phones). Your address now: ') + d.your_ip }),
      h('details', { class: 'panel sec-more' }, h('summary', null, h('b', null, t('Trusted carriers (calls without a password)')), ' ', h('span', { class: 'muted small' }, '(' + d.acl.length + ')')),
        h('p', { class: 'muted small' }, t('Calls from these addresses are accepted without authentication (carriers that send your DIDs by IP). Your gateways\' addresses are trusted automatically.')),
        h('div', { class: 'sec-addrow' }, h('label', { class: 'grow' }, h('span', null, t('Address')), aclIP), h('label', { class: 'grow' }, h('span', null, t('Description')), aclDesc),
          h('button', { type: 'button', class: 'btn', onclick: async () => {
            try { await api('POST', 'security/acl', { cidr: aclIP.value, description: aclDesc.value }); toast(t('Carrier trusted')); await reload(); } catch (e) { fail(e); }
          } }, icon('plus', 15), t('Add'))),
        table([{ label: 'Address', key: 'cidr' }, { label: 'Description', key: 'description' },
          { label: '', actions: 1, render: (n) => h('button', { class: 'icon-btn', title: t('Delete'), onclick: async () => {
            if (!confirm(t('Stop trusting') + ' ' + n.cidr + '?')) return;
            try { await api('DELETE', 'security/acl/' + n.uuid); await reload(); } catch (e) { fail(e); }
          } }, icon('trash', 15)) }], d.acl, { empty: t('No trusted carrier addresses.') })),
      h('details', { class: 'panel sec-more' }, h('summary', null, h('b', null, t('SIP scanners (User-Agent)')), ' ', h('span', { class: 'muted small' }, '(' + d.rules.filter((r) => r.type === 'ua-deny').length + ')')),
        h('p', { class: 'muted small' }, t('Requests whose User-Agent contains one of these are ignored and their address banned.')),
        h('div', { class: 'sec-addrow' }, h('label', { class: 'grow' }, h('span', null, 'User-Agent'), ua),
          h('button', { type: 'button', class: 'btn', onclick: async () => {
            try { await api('POST', 'security/rules', { type: 'ua-deny', value: ua.value, description: '' }); await reload(); } catch (e) { fail(e); }
          } }, icon('plus', 15), t('Add'))),
        table([{ label: 'User-Agent contains', key: 'value' }, { label: 'Matches', key: 'hits', num: 1 },
          { label: '', actions: 1, render: (r) => h('button', { class: 'icon-btn', title: t('Delete'), onclick: async () => {
            try { await api('DELETE', 'security/rules/' + r.uuid); await reload(); } catch (e) { fail(e); }
          } }, icon('trash', 15)) }], d.rules.filter((r) => r.type === 'ua-deny'), { empty: t('None.') })));
  };
  await reload();
};

// ---------------------------------------------------------------------------
// SIP Guard

SEC_TABS.sipguard = async (body) => {
  let sub = 'live';
  let q = '';
  const selected = new Set(); // "ip|jail"
  let g = (await api('GET', 'security')).guard;
  const head = h('div', { class: 'panel sec-gbar' });
  const seg = h('div', { class: 'sec-seg' });
  const inner = h('div');
  const refresh = async () => { g = (await api('GET', 'security')).guard; draw(); };
  const drawHead = () => {
    const loaded = g.jails.length + (g.ssh && g.ssh.installed ? 1 : 0);
    const sw = h('input', { type: 'checkbox', checked: g.enabled, role: 'switch', 'aria-label': 'SIP Guard' });
    sw.addEventListener('change', async () => {
      if (!sw.checked && !confirm(t('Turn SIP Guard off? Every ban is lifted and nothing is banned until it is on again.'))) { sw.checked = true; return; }
      try { g = await api('PUT', 'security/guard', { enabled: sw.checked, jails: {} }); draw(); } catch (e) { fail(e); sw.checked = !sw.checked; }
    });
    fill(head, h('b', null, loaded + ' ' + t('jail(s) loaded') + ' · ' + g.banned_ips + ' ' + t('currently banned IP(s)')),
      h('span', { class: 'info-dot', title: t('Each protection bans an address that fails too often within its window, for its ban time.') }, icon('info', 14)),
      h('span', { class: 'spacer' }), h('label', { class: 'switch sec-switch small' }, h('span', { class: 'muted small' }, 'SIP Guard'), sw),
      h('button', { type: 'button', class: 'icon-btn', title: t('Refresh'), 'aria-label': t('Refresh'), onclick: refresh }, icon('refresh', 15)));
  };
  const editJail = (j) => {
    const en = h('input', { type: 'checkbox', checked: j.enabled });
    const max = h('input', { type: 'number', min: '1', value: String(j.max), class: 'sec-num' });
    const win = durationInput(j.window, [['1', 'seconds'], ['60', 'minutes'], ['3600', 'hours']]);
    const ban = durationInput(j.ban);
    modal(j.title, h('div', { class: 'form' }, h('p', { class: 'muted small full' }, j.description),
      h('label', { class: 'switch' }, en, t('Enabled')),
      h('div', { class: 'field' }, h('label', null, t('Failures')), max, h('div', { class: 'help' }, t('How many failures ban the address.'))),
      h('div', { class: 'field' }, h('label', null, t('Window')), win.el, h('div', { class: 'help' }, t('The time the failures are counted over.'))),
      h('div', { class: 'field' }, h('label', null, t('Ban')), ban.el, h('div', { class: 'help' }, t('How long the address stays banned.')))), 'Save', async () => {
      const jails = {};
      jails[j.name] = { enabled: en.checked, max: Number(max.value), window: win.get(), ban: ban.get() };
      g = await api('PUT', 'security/guard', { enabled: g.enabled, jails });
      draw();
      toast(t('Saved'));
    });
  };
  const chips = (j, locked) => h('div', { class: 'sec-chips' },
    h('span', { class: 'sec-chip ' + (j.enabled ? 'on' : 'off') }, icon(j.enabled ? 'check' : 'x', 12), t(j.enabled ? 'Enabled' : 'Disabled')),
    h('span', { class: 'sec-chip' }, icon('list', 12), t('Failures') + ': ' + (j.max || '—')),
    h('span', { class: 'sec-chip' }, icon('clock', 12), t('Window') + ': ' + span(j.window)),
    h('span', { class: 'sec-chip' }, icon('lock', 12), t('Ban') + ': ' + span(j.ban)),
    locked ? h('span', { class: 'sec-chip' }, icon('server', 12), t('Server')) : null);
  const jailCard = (j) => {
    const banned = j.banned.filter((b) => !q || b.ip.includes(q));
    const pill = h('span', { class: 'sec-pill' + (j.banned.length ? ' red' : '') }, t('Currently banned') + ': ' + j.banned.length);
    return h('div', { class: 'panel sec-jail' },
      h('div', { class: 'sec-jail-head' }, h('div', null, h('b', null, t(j.title)), h('p', { class: 'muted small' }, t(j.description))),
        h('span', { class: 'spacer' }), pill,
        h('button', { type: 'button', class: 'icon-btn', title: t('Edit'), 'aria-label': t('Edit') + ' ' + j.title, onclick: () => editJail(j) }, icon('edit', 15))),
      chips(j),
      banned.length ? h('div', { class: 'sec-bans' }, banned.map((b) => {
        const key = b.ip + '|' + j.name;
        const cb = h('input', { type: 'checkbox', checked: selected.has(key), 'aria-label': t('Select') + ' ' + b.ip });
        cb.addEventListener('change', () => { if (cb.checked) selected.add(key); else selected.delete(key); drawBulk(); });
        const until = new Date(b.until * 1000);
        return h('span', { class: 'sec-ban' }, cb, h('b', null, b.ip),
          h('span', { class: 'info-dot', title: (b.reason || '') + ' · ' + t('until') + ' ' + until.toLocaleString() + (b.source === 'manual' ? ' · ' + t('banned by hand') : '') }, icon('info', 13)),
          h('button', { type: 'button', class: 'icon-btn', title: t('Unban'), 'aria-label': t('Unban') + ' ' + b.ip, onclick: async () => {
            try { g = await api('POST', 'security/unban', { ip: b.ip, jail: j.name }); selected.delete(key); draw(); toast(b.ip + ' ' + t('unbanned')); } catch (e) { fail(e); }
          } }, icon('trash', 14)));
      })) : h('div', { class: 'muted small sec-none' }, t(q && j.banned.length ? 'No match' : 'No banned IPs')));
  };
  const sshCard = () => {
    const s = g.ssh || {};
    const j = { enabled: !!s.enabled, max: s.max, window: s.window, ban: s.ban };
    return h('div', { class: 'panel sec-jail' },
      h('div', { class: 'sec-jail-head' }, h('div', null, h('b', null, t('Remote Server Access Protection')),
        h('p', { class: 'muted small' }, t('Blocks IPs failing remote server (sshd) authentication — secures the server\'s SSH daemon from credential stuffing.'))),
      h('span', { class: 'spacer' }), h('span', { class: 'icon-btn sec-lock', title: t('Managed by the server\'s SSH protection') }, icon('lock', 15))),
      s.installed ? chips(j, true) : h('div', { class: 'sec-chips' }, h('span', { class: 'sec-chip off' }, icon('x', 12), t('SSH protection is not installed'))),
      h('div', { class: 'muted small sec-none' }, s.installed ? t('SSH bans are made by the server\'s SSH protection, outside RingQswitch: list them with fail2ban-client status sshd.')
        : t('SSH logins are not watched: install the SSH protection on the server (apt install fail2ban) to protect them.')));
  };
  const bulkBtn = h('button', { type: 'button', class: 'btn small danger hidden' }, icon('trash', 14), t('Unban selected'));
  const drawBulk = () => bulkBtn.classList.toggle('hidden', !selected.size);
  bulkBtn.addEventListener('click', async () => {
    for (const key of [...selected]) {
      const [ip, jail] = key.split('|');
      try { g = await api('POST', 'security/unban', { ip, jail }); } catch (e) { fail(e); }
      selected.delete(key);
    }
    draw();
  });
  const live = () => {
    const area = selectOf(g.jails.map((j) => [j.name, t(j.title)]), '', t('Select area'));
    area.setAttribute('aria-label', t('Protection area'));
    const ip = h('input', { placeholder: t('IP or CIDR (e.g. 192.168.1.0/24)'), 'aria-label': t('IP address') });
    const add = h('button', { type: 'button', class: 'btn primary' }, icon('plus', 15), t('Add'));
    add.addEventListener('click', async () => {
      if (!area.value) { toast(t('Choose the protection area'), true); return; }
      try { g = await api('POST', 'security/ban', { ip: ip.value.trim(), jail: area.value }); ip.value = ''; draw(); toast(t('Banned')); } catch (e) { fail(e); }
    });
    const search = h('input', { type: 'search', class: 'search-box sec-search', placeholder: t('Search IP, jail, or protection name'), value: q, 'aria-label': t('Search') });
    const cards = h('div');
    const drawCards = () => {
      const ql = q.toLowerCase();
      const list = g.jails.filter((j) => !q || j.title.toLowerCase().includes(ql) || j.name.includes(ql) || j.banned.some((b) => b.ip.includes(q)));
      fill(cards, list.map(jailCard), !q || 'remote server access protection ssh'.includes(ql) ? sshCard() : null);
    };
    search.addEventListener('input', () => { q = search.value.trim(); drawCards(); });
    drawCards();
    return h('div', null,
      h('div', { class: 'panel sec-form' }, h('label', null, h('span', null, t('Protection area')), area), h('label', { class: 'grow' }, h('span', null, t('IP address')), ip), add),
      h('div', { class: 'sec-tools' }, search, bulkBtn), cards);
  };
  const histTab = async () => {
    let days = 7;
    const box = h('div');
    const period = selectOf([['1', t('Last 24 hours')], ['7', t('Last 7 days')], ['30', t('Last 30 days')], ['90', t('Last 90 days')]], '7');
    const load = async () => {
      const s = await api('GET', 'security/history?days=' + days);
      const title = (n) => { const j = g.jails.find((x) => x.name === n); return j ? t(j.title) : n; };
      const top = Object.entries(s.by_jail).sort((a, b) => b[1] - a[1])[0];
      const tile = (v, l) => h('div', { class: 'sec-tile' }, h('b', null, String(v)), h('span', null, t(l)));
      fill(box,
        h('div', { class: 'sec-tiles' }, tile(s.bans, days === 1 ? 'Bans in 24 hours' : 'Bans in ' + days + ' days'), tile(s.bans_24h, 'Bans in the last 24 hours'),
          tile(s.unique_ips, 'Different addresses'), tile(top ? title(top[0]) : '—', 'Most active protection')),
        h('div', { class: 'sec-hist' },
          h('section', { class: 'panel' }, h('h2', null, t('Bans by protection')),
            table([{ label: 'Protection', render: (r) => title(r[0]) }, { label: 'Bans', num: 1, render: (r) => r[1] }],
              Object.entries(s.by_jail).sort((a, b) => b[1] - a[1]), { empty: t('No bans in this period.') })),
          h('section', { class: 'panel' }, h('h2', null, t('Most banned addresses')),
            table([{ label: 'IP address', render: (r) => h('b', null, r.ip) }, { label: 'Bans', key: 'bans', num: 1 },
              { label: 'Protections', render: (r) => (r.jails || '').split(', ').map(title).join(', '), wrap: 1 },
              { label: 'Last', render: (r) => new Date(r.last * 1000).toLocaleString() }], s.top_ips, { empty: t('No bans in this period.') }))),
        h('section', { class: 'panel' }, h('h2', null, t('Recent events')),
          table([{ label: 'Time', render: (e) => new Date(e.time * 1000).toLocaleString() },
            { label: 'Event', render: (e) => h('span', { class: 'sec-ev ' + e.action }, t({ ban: 'Banned', unban: 'Unbanned', expire: 'Ban ended' }[e.action] || e.action)) },
            { label: 'IP address', render: (e) => h('b', null, e.ip) }, { label: 'Protection', render: (e) => title(e.jail) },
            { label: 'Reason', key: 'reason', wrap: 1 }, { label: 'By', key: 'actor' }], s.events, { empty: t('Nothing yet.') })));
    };
    period.addEventListener('change', async () => { days = Number(period.value); await load(); });
    await load();
    return h('div', null, h('div', { class: 'sec-tools' }, h('label', { class: 'sec-period' }, h('span', { class: 'muted small' }, t('Period')), period)), box);
  };
  const draw = async () => {
    drawHead();
    fill(seg, [['live', 'Live bans', 'shield'], ['history', 'History & stats', 'history']].map(([k, l, ic]) =>
      h('button', { type: 'button', class: k === sub ? 'on' : '', onclick: () => { sub = k; draw(); } }, icon(ic, 13), t(l))));
    fill(inner, sub === 'live' ? live() : await histTab());
    drawBulk();
  };
  fill(body, head, seg, inner);
  await draw();
};

// ---------------------------------------------------------------------------
// Web auto logout: after the company's time without mouse, keyboard or
// touch the page signs out (a minute's warning first; never during a call).
// The server also ends sessions that send nothing for that long.

const IdleLogout = (() => {
  let last = Date.now();
  let timer = null;
  let warning = null; // closes the warning
  let bound = false;
  const touch = () => {
    last = Date.now();
    if (warning) { warning(); warning = null; }
  };
  const minutes = () => (S.me && S.me.idle_logout) || 0;
  function check() {
    const m = minutes();
    if (!m || !S.me) return;
    if (window.Phone && Phone.hasCalls && Phone.hasCalls()) { last = Date.now(); return; }
    const idle = Date.now() - last;
    if (idle >= m * 60000) {
      if (warning) { warning(); warning = null; }
      clearInterval(timer);
      timer = null;
      signOut().then(() => toast(t('Signed out after') + ' ' + m + ' ' + t('minutes without activity')));
      return;
    }
    if (idle >= m * 60000 - 60000 && !warning) {
      warning = modal(t('Are you still there?'), h('p', null, t('You will be signed out in a minute because nobody has used this page for a while.')), 'Stay signed in', () => { touch(); });
    }
  }
  function start() {
    clearInterval(timer);
    timer = null;
    last = Date.now();
    if (!minutes()) return;
    if (!bound) {
      for (const ev of ['mousemove', 'mousedown', 'keydown', 'touchstart', 'wheel']) window.addEventListener(ev, touch, { passive: true, capture: true });
      window.addEventListener('rq-logout', () => { clearInterval(timer); timer = null; });
      bound = true;
    }
    timer = setInterval(check, 5000);
  }
  return { start };
})();
