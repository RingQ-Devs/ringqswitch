'use strict';
// People pages: Team directory, User roles (drag and drop) and Contacts.

// presence: the colour and text of an extension's status.
function presence(d) {
  if (d.on_call) return ['amber', 'On a call'];
  if (d.dnd) return ['red', 'Do not disturb'];
  if (d.agent_status === 'On Break') return ['amber', 'Break' + (d.aux ? ': ' + d.aux : '')];
  return d.phone ? ['green', 'Available'] : ['red', 'Unregistered'];
}

function personAvatar(name, ext, color, cls) {
  return h('span', { class: 'avatar ' + (cls || '') }, initials(name && name !== ext ? name : ext), color !== undefined ? h('i', { class: 'presence ' + color }) : null);
}

function callNumber(num) {
  if (isExt() && S.me.webrtc && window.Phone) { Phone.toggle(true); Phone.dial(num); return; }
  toast('Sign in with an extension and start the dialer to call', true);
}

// modal opens a small dialog; onOK returns false (or throws) to keep it open.
// okLabel false: no OK button, only Close.
function modal(title, body, okLabel, onOK) {
  const back = h('div', { class: 'modal-back' });
  // Escape closes the window on top.
  const onKey = (e) => { if (e.key === 'Escape' && back.isConnected && back === $$('.modal-back').pop()) close(); };
  const close = () => { back.remove(); document.removeEventListener('keydown', onKey); };
  document.addEventListener('keydown', onKey);
  const ok = okLabel === false ? null : h('button', { class: 'btn primary', type: 'submit' }, okLabel || 'Save');
  const form = h('form', { class: 'modal', role: 'dialog', 'aria-label': title },
    h('h2', null, title), body,
    h('div', { class: 'form-actions' }, ok, h('button', { class: 'btn', type: 'button', onclick: close }, ok ? 'Cancel' : 'Close')));
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    if (!ok) return;
    ok.disabled = true;
    try { if ((await onOK()) !== false) close(); } catch (err) { fail(err); } finally { ok.disabled = false; }
  });
  back.addEventListener('click', (e) => { if (e.target === back) close(); });
  back.append(form);
  document.body.append(back);
  const first = form.querySelector('input, select, textarea');
  if (first) first.focus();
  return close;
}

// ---------------------------------------------------------------------------
// Team directory

PAGES.directory = async () => {
  const main = $('#main');
  const favKey = 'rq-favs-' + (S.me.extension || S.me.user) + '@' + S.domain;
  let favs = [];
  try { favs = JSON.parse(localStorage.getItem(favKey) || '[]'); } catch (e) { favs = []; }
  let view = 'grid';
  try { view = localStorage.getItem('rq-dir-view') || 'grid'; } catch (e) { /* private mode */ }
  const q = h('input', { type: 'search', class: 'search-box', placeholder: 'Search…', 'aria-label': 'Search people' });
  const filter = h('select', { class: 'filter-box', 'aria-label': 'Show' },
    [['all', 'All'], ['available', 'Available'], ['busy', 'On a call'], ['away', 'On break / do not disturb'], ['unregistered', 'Unregistered'], ['favs', 'Favourites']]
      .map(([v, l]) => h('option', { value: v }, l)));
  const viewBtn = h('button', { class: 'icon-btn view-btn', type: 'button' });
  const title = h('h1', null, 'Team Directory');
  const grid = h('div');
  let dir = [];
  const kindOf = (d) => {
    const [c] = presence(d);
    if (d.on_call) return 'busy';
    if (c === 'amber' || d.dnd) return 'away';
    return c === 'green' ? 'available' : 'unregistered';
  };
  const toggleFav = (ext) => {
    favs = favs.includes(ext) ? favs.filter((x) => x !== ext) : favs.concat(ext);
    try { localStorage.setItem(favKey, JSON.stringify(favs)); } catch (e) { /* private mode */ }
    draw();
  };
  const actions = (d) => {
    if (d.extension === S.me.extension) return h('span', { class: 'you-pill' }, 'YOU');
    return h('div', { class: 'tc-actions' },
      isExt() && S.me.webrtc ? h('button', { class: 'round-btn', title: 'Call ' + d.extension, 'aria-label': 'Call ' + d.extension, onclick: () => callNumber(d.extension) }, icon('phone', 16)) : null,
      isExt() ? h('a', { class: 'round-btn', href: '#/chat/' + encodeURIComponent(d.extension), title: 'Chat', 'aria-label': 'Chat with ' + d.extension }, icon('chat', 16)) : null,
      h('button', { class: 'round-btn' + (favs.includes(d.extension) ? ' fav' : ''), title: favs.includes(d.extension) ? 'Remove from favourites' : 'Add to favourites',
        'aria-pressed': String(favs.includes(d.extension)), onclick: () => toggleFav(d.extension) }, icon('star', 16)));
  };
  const draw = () => {
    fill(viewBtn, icon(view === 'grid' ? 'list' : 'apps', 18));
    viewBtn.title = view === 'grid' ? 'Show as a list' : 'Show as cards';
    const f = q.value.trim().toLowerCase();
    const fv = filter.value;
    const people = dir.filter((d) => (d.extension + ' ' + (d.name || '')).toLowerCase().includes(f) &&
      (fv === 'all' || (fv === 'favs' ? favs.includes(d.extension) : kindOf(d) === fv)))
      .sort((a, b) => (b.extension === S.me.extension) - (a.extension === S.me.extension) || favs.includes(b.extension) - favs.includes(a.extension) || extCompare(a.extension, b.extension));
    fill(title, 'Team Directory ', h('span', { class: 'muted title-count' }, '(' + dir.length + ')'));
    if (!people.length) { fill(grid, h('div', { class: 'empty panel' }, dir.length ? 'Nobody matches.' : 'No extensions yet.')); return; }
    if (view === 'list') {
      fill(grid, table([
        { label: 'Name', render: (d) => h('span', { class: 'cell-person' }, personAvatar(d.name, d.extension, presence(d)[0]), h('b', null, d.name || 'Ext ' + d.extension)) },
        { label: 'Extension', key: 'extension' },
        { label: 'Status', render: (d) => { const [c, t] = presence(d); return h('span', null, h('span', { class: 'dot ' + c }), t); } },
        { label: '', actions: 1, render: actions },
      ], people));
      return;
    }
    fill(grid, h('div', { class: 'team-grid' }, people.map((d) => {
      const [c, t] = presence(d);
      return h('div', { class: 'tcard ' + c + (d.extension === S.me.extension ? ' me' : '') },
        personAvatar(d.name, d.extension, c, 'lg'),
        h('div', { class: 'tc-info' }, h('b', { class: 'ellipsis', title: d.name || '' }, d.name && d.name !== d.extension ? d.name : 'Ext ' + d.extension),
          h('span', null, 'Ext ' + d.extension), h('span', { class: 'tc-status' }, t)),
        actions(d));
    })));
  };
  const load = async () => { dir = await api('GET', dpath('directory')); if (main.contains(grid)) draw(); };
  viewBtn.addEventListener('click', () => {
    view = view === 'grid' ? 'list' : 'grid';
    try { localStorage.setItem('rq-dir-view', view); } catch (e) { /* private mode */ }
    draw();
  });
  q.addEventListener('input', draw);
  filter.addEventListener('change', draw);
  fill(main, h('div', { class: 'page-head' }, title, h('span', { class: 'spacer' }), filter, h('label', { class: 'search-wrap' }, icon('search', 16), q), viewBtn), grid);
  await load();
  const onPresence = () => { if (!main.contains(grid)) { off('presence', onPresence); return; } load().catch(() => {}); };
  on('presence', onPresence);
  every(15000, () => load().catch(() => {}));
};

function extCompare(a, b) {
  return /^\d+$/.test(a) && /^\d+$/.test(b) ? Number(a) - Number(b) : String(a).localeCompare(String(b));
}

// ---------------------------------------------------------------------------
// User roles: drag people between User, Manager, Owner and No portal access.

const ROLE_GROUPS = [
  { role: 'user', title: 'User', icon: 'users', help: 'Dialer, chat, meetings, monitoring and their own reports.', empty: 'Drag someone here to make them a user.' },
  { role: 'owner', title: 'Owner', icon: 'shieldcheck', help: 'Everything in the company: settings, users and all reports.', empty: 'Drag someone here to make them an owner.' },
  { role: 'manager', title: 'Manager', icon: 'usercog', help: 'Supervisors: listen, whisper and barge in, every agent and all reports; no settings.', empty: 'Drag someone here to make them a manager.' },
  { role: '', title: 'No portal access', icon: 'userx', help: 'Cannot sign in to the portal. Their phones keep working.', empty: 'Everybody can sign in.' },
];
const ROLE_NAME = { user: 'a User', owner: 'an Owner', manager: 'a Manager', '': 'without portal access' };

PAGES.roles = async () => {
  const main = $('#main');
  let people = [];
  let wide = null; // the role of the enlarged group (null: none)
  const q = h('input', { type: 'search', class: 'search-box', placeholder: 'Search by name or extension', 'aria-label': 'Search by name or extension' });
  const board = h('div', { class: 'roles-board' });
  const load = async () => { people = await api('GET', dpath('roles')); draw(); };

  const setRole = async (p, role, password) => {
    await api('PUT', dpath('roles/' + p.extension_uuid), { role, password: password || '' });
    toast((p.name || p.extension) + ' is now ' + ROLE_NAME[role]);
    await load();
  };
  const move = (p, role) => {
    if (p.role === role) return;
    if (p.you) { toast('You cannot change your own role', true); return; }
    if (role === '') {
      modal('Remove portal access', h('p', null, h('b', null, p.name), ' (' + p.extension + ') will not be able to sign in to the portal any more. Their phones keep working.'),
        'Remove access', () => setRole(p, ''));
      return;
    }
    if (p.role === '' && !p.password_set) {
      const pw = h('input', { type: 'text', value: randomPassword(12), minlength: '8', required: true, autocomplete: 'new-password', 'aria-label': 'Web password' });
      modal('Give ' + (p.name || p.extension) + ' portal access',
        h('div', null,
          h('p', null, 'They sign in with extension ', h('b', null, p.extension), ' and this web password (not the SIP password). Write it down and give it to them.'),
          h('label', null, 'Web password', h('div', { class: 'multi-row' }, pw, h('button', { class: 'btn', type: 'button', onclick: () => { pw.value = randomPassword(12); } }, 'New')))),
        'Make ' + ROLE_NAME[role].replace(/^an? /, ''), () => setRole(p, role, pw.value));
      return;
    }
    setRole(p, role).catch(fail);
  };
  // Keyboard and touch: a menu instead of dragging.
  const chipMenu = (p, chip) => {
    $$('.chip-menu').forEach((m) => m.remove());
    if (p.you) { toast('You cannot change your own role', true); return; }
    const menu = h('div', { class: 'menu-pop chip-menu', role: 'menu' },
      h('div', { class: 'group-label' }, 'Move ' + (p.name || p.extension) + ' to'),
      ROLE_GROUPS.filter((g) => g.role !== p.role).map((g) => h('button', { type: 'button', role: 'menuitem', onclick: () => { menu.remove(); move(p, g.role); } }, icon(g.icon, 16), g.title)));
    chip.append(menu);
    setTimeout(() => document.addEventListener('click', () => menu.remove(), { once: true }), 0);
  };
  const chip = (p) => {
    const el = h('div', { class: 'role-chip' + (p.you ? ' locked' : ''), draggable: p.you ? 'false' : 'true', tabindex: '0', role: 'button',
      title: p.you ? 'This is you: another owner can change your role' : 'Drag to another group, or press Enter',
      'aria-label': (p.name || p.extension) + ', extension ' + p.extension + ', ' + (ROLE_GROUPS.find((g) => g.role === p.role) || {}).title },
    personAvatar(p.name, p.extension, p.registered ? 'green' : 'red'),
    h('div', { class: 'grow' }, h('div', { class: 'ellipsis rc-name' }, p.name), h('div', { class: 'muted small' }, p.extension)),
    p.you ? icon('lock', 15, 'muted') : null);
    el.addEventListener('dragstart', (e) => { e.dataTransfer.setData('text/plain', p.extension_uuid); e.dataTransfer.effectAllowed = 'move'; el.classList.add('dragging'); });
    el.addEventListener('dragend', () => el.classList.remove('dragging'));
    el.addEventListener('keydown', (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); chipMenu(p, el); } });
    el.addEventListener('click', (e) => { e.stopPropagation(); chipMenu(p, el); });
    return el;
  };
  const card = (g, shown) => {
    const all = people.filter((p) => p.role === g.role);
    const mine = shown.filter((p) => p.role === g.role);
    const c = h('section', { class: 'role-card' + (wide === g.role ? ' wide' : '') + (g.role === 'user' ? ' tall' : ''), 'data-role': g.role, 'aria-label': g.title },
      h('div', { class: 'role-head' }, h('span', { class: 'role-icon r-' + (g.role || 'none') }, icon(g.icon, 18)),
        h('div', { class: 'grow' }, h('div', { class: 'role-title' }, h('h2', null, g.title), h('span', { class: 'count-pill' }, String(all.length))),
          h('div', { class: 'muted small role-help' }, g.help)),
        g.role !== 'user' ? h('button', { class: 'icon-btn', type: 'button', title: wide === g.role ? 'Smaller' : 'Larger', 'aria-label': wide === g.role ? 'Smaller' : 'Larger',
          onclick: () => { wide = wide === g.role ? null : g.role; draw(); } }, icon(wide === g.role ? 'shrink' : 'expand', 16)) : null),
      h('div', { class: 'role-body' + (g.role === 'user' ? ' list' : '') }, mine.length ? mine.map(chip) : h('div', { class: 'role-empty' }, all.length ? 'Nobody matches the search.' : g.empty)));
    c.addEventListener('dragover', (e) => { e.preventDefault(); e.dataTransfer.dropEffect = 'move'; c.classList.add('drop'); });
    c.addEventListener('dragleave', (e) => { if (!c.contains(e.relatedTarget)) c.classList.remove('drop'); });
    c.addEventListener('drop', (e) => {
      e.preventDefault();
      c.classList.remove('drop');
      const p = people.find((x) => x.extension_uuid === e.dataTransfer.getData('text/plain'));
      if (p) move(p, g.role);
    });
    return c;
  };
  const draw = () => {
    const f = q.value.trim().toLowerCase();
    const shown = people.filter((p) => (p.extension + ' ' + p.name).toLowerCase().includes(f));
    fill(board, card(ROLE_GROUPS[0], shown), h('div', { class: 'roles-right' }, ROLE_GROUPS.slice(1).map((g) => card(g, shown))));
  };
  q.addEventListener('input', draw);
  fill(main,
    h('div', { class: 'page-head' }, h('h1', null, 'User roles ', h('span', { class: 'info-tip', title: 'Drag a person to another group to change what they can do in the portal. The SIP phones are not affected.' }, icon('info', 16))),
      h('span', { class: 'spacer' }), h('label', { class: 'search-wrap' }, icon('search', 16), q)),
    board);
  await load();
};

// ---------------------------------------------------------------------------
// Contacts: the company's shared address book.

PAGES.contacts = async () => {
  const main = $('#main');
  let page = 0, size = 10, data = { rows: [], total: 0, can_import: false };
  try { size = Number(localStorage.getItem('rq-contacts-size')) || 10; } catch (e) { /* private mode */ }
  const q = h('input', { type: 'search', class: 'search-box', placeholder: 'Search contacts', 'aria-label': 'Search contacts' });
  const count = h('span', { class: 'count-pill' }, '0');
  const body = h('div');
  const tools = h('div', { class: 'btn-group' });
  const load = async () => {
    data = await api('GET', dpath('contacts?limit=' + size + '&offset=' + page * size + '&q=' + encodeURIComponent(q.value.trim())));
    draw();
  };
  const edit = (c) => {
    const f = {};
    const field = (k, label, attrs) => { f[k] = h(k === 'notes' ? 'textarea' : 'input', Object.assign({ value: c ? c[k] : '' }, attrs || {})); if (k === 'notes') f[k].value = c ? c[k] : ''; return h('label', null, label, f[k]); };
    modal(c ? 'Edit contact' : 'New contact',
      h('div', { class: 'form' }, field('name', 'Name', { required: true, maxlength: '100' }), field('company', 'Company', { maxlength: '100' }),
        field('phone', 'Phone', { inputmode: 'tel', maxlength: '40' }), field('mobile', 'Mobile', { inputmode: 'tel', maxlength: '40' }),
        field('email', 'Email', { type: 'email', maxlength: '120' }), h('div', { class: 'field full' }, field('notes', 'Notes', { maxlength: '1000', rows: '3' }))),
      c ? 'Save' : 'Add contact',
      async () => {
        const body = Object.fromEntries(Object.entries(f).map(([k, el]) => [k, el.value]));
        if (c) await api('PUT', dpath('contacts/' + c.contact_uuid), body); else await api('POST', dpath('contacts'), body);
        toast(c ? 'Contact saved' : 'Contact added');
        await load();
      });
  };
  const del = (c) => modal('Delete contact', h('p', null, 'Delete ', h('b', null, c.name), ' from the address book?'), 'Delete', async () => {
    await api('DELETE', dpath('contacts/' + c.contact_uuid));
    toast('Contact deleted');
    if (data.rows.length === 1 && page > 0) page--;
    await load();
  });
  const importFile = h('input', { type: 'file', accept: '.csv,text/csv', class: 'hidden' });
  importFile.addEventListener('change', async () => {
    const file = importFile.files[0];
    importFile.value = '';
    if (!file) return;
    try {
      const r = await api('POST', dpath('contacts/import'), await file.arrayBuffer(), true);
      toast(r.added + ' contact(s) added' + (r.skipped ? ', ' + r.skipped + ' skipped (no name or number, or already there)' : ''));
      page = 0;
      await load();
    } catch (e) { fail(e); }
  });
  const pager = () => {
    const pages = Math.max(1, Math.ceil(data.total / size));
    const from = data.total ? page * size + 1 : 0, to = Math.min(data.total, (page + 1) * size);
    const go = (n) => { page = Math.min(Math.max(0, n), pages - 1); load().catch(fail); };
    const nums = [];
    for (let i = Math.max(0, page - 2); i < Math.min(pages, page + 3); i++) nums.push(h('button', { class: 'page-num' + (i === page ? ' on' : ''), type: 'button', onclick: () => go(i) }, String(i + 1)));
    const per = h('select', { 'aria-label': 'Rows per page' }, [10, 25, 50, 100].map((n) => h('option', { value: n, selected: n === size }, String(n))));
    per.addEventListener('change', () => { size = Number(per.value); page = 0; try { localStorage.setItem('rq-contacts-size', size); } catch (e) { /* private */ } load().catch(fail); });
    return h('div', { class: 'pager' }, h('span', { class: 'muted' }, 'Showing ' + from + '–' + to + ' of ' + data.total), h('span', { class: 'spacer' }),
      h('button', { class: 'btn small', type: 'button', disabled: page === 0, onclick: () => go(page - 1) }, 'Previous'), nums,
      h('button', { class: 'btn small', type: 'button', disabled: page >= pages - 1, onclick: () => go(page + 1) }, 'Next'),
      h('span', { class: 'spacer' }), h('label', { class: 'muted per-page' }, 'Rows per page ', per));
  };
  const draw = () => {
    count.textContent = String(data.total);
    fill(tools,
      data.can_import ? [
        h('button', { class: 'btn', type: 'button', title: 'CSV with a header row: name, company, phone, mobile, email, notes', onclick: () => importFile.click() }, icon('upload', 16), 'Import'),
        h('a', { class: 'btn', href: '/api/' + dpath('contacts/export') }, icon('download', 16), 'Export'),
      ] : null,
      h('button', { class: 'btn primary', type: 'button', onclick: () => edit(null) }, icon('plus', 16), 'New contact'));
    const cols = [
      { label: 'Name', render: (c) => h('span', { class: 'cell-person' }, h('span', { class: 'avatar sm' }, initials(c.name)), h('b', null, c.name)) },
      { label: 'Company', render: (c) => c.company || h('span', { class: 'muted' }, '—') },
      { label: 'Phone', render: (c) => h('div', null, c.phone || '', c.mobile ? h('div', { class: 'muted small' }, 'Mobile ' + c.mobile) : null) },
      { label: 'Email', render: (c) => c.email ? h('a', { href: 'mailto:' + c.email }, c.email) : h('span', { class: 'muted' }, '—') },
      { label: 'Actions', actions: 1, render: (c) => h('div', { class: 'btn-group' },
        h('button', { class: 'icon-btn', type: 'button', title: 'Call ' + (c.phone || c.mobile), 'aria-label': 'Call ' + c.name, onclick: () => callNumber((c.phone || c.mobile).replace(/[^0-9+*#]/g, '')) }, icon('phone', 17)),
        c.mobile && c.phone ? h('button', { class: 'icon-btn', type: 'button', title: 'Call mobile ' + c.mobile, 'aria-label': 'Call mobile of ' + c.name, onclick: () => callNumber(c.mobile.replace(/[^0-9+*#]/g, '')) }, icon('outgoing', 17)) : null,
        truthy(c.can_edit) ? h('button', { class: 'icon-btn', type: 'button', title: 'Edit', 'aria-label': 'Edit ' + c.name, onclick: () => edit(c) }, icon('edit', 17)) : null,
        truthy(c.can_edit) ? h('button', { class: 'icon-btn danger-icon', type: 'button', title: 'Delete', 'aria-label': 'Delete ' + c.name, onclick: () => del(c) }, icon('trash', 17)) : null) },
    ];
    fill(body, table(cols, data.rows, { empty: q.value ? 'No contact matches.' : 'No contacts yet. Add one, or import a CSV file.' }), data.total > 10 || page > 0 ? pager() : null);
  };
  let timer;
  q.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => { page = 0; load().catch(fail); }, 250); });
  fill(main,
    h('div', { class: 'page-head' }, h('div', null, h('h1', null, 'Contacts ', count), h('div', { class: 'muted page-sub' }, 'Everyone you can call: your company\'s shared address book.')),
      h('span', { class: 'spacer' }), h('label', { class: 'search-wrap' }, icon('search', 16), q), tools, importFile),
    body);
  await load();
};
