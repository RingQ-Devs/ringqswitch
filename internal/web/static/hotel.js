'use strict';
// Hotel front desk (like RingQ's): every extension is a room. Check a guest
// in and out, and schedule wake-up calls (the switch rings the room at the
// time and plays the wake-up sound; it tries 3 times, 5 minutes apart).

const HOTEL_WAKE = {
  scheduled: ['Scheduled', 'blue'], calling: ['Ringing the room now', 'amber'], answered: ['Answered', 'green'],
  no_answer: ['Not answered', 'red'], cancelled: ['Cancelled', ''],
};

function hotelWhen(ts) {
  const d = new Date(ts);
  if (isNaN(d)) return ts || '';
  const day = new Date(); day.setHours(0, 0, 0, 0);
  const diff = Math.round((new Date(d).setHours(0, 0, 0, 0) - day) / 86400000);
  const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  if (diff === 0) return t('Today') + ' ' + time;
  if (diff === 1) return t('Tomorrow') + ' ' + time;
  return d.toLocaleDateString([], { weekday: 'short', day: 'numeric', month: 'short' }) + ' ' + time;
}

const pad2 = (n) => String(n).padStart(2, '0');
const dateValue = (d) => d.getFullYear() + '-' + pad2(d.getMonth() + 1) + '-' + pad2(d.getDate());

PAGES.hotel = async () => {
  const main = $('#main');
  let rooms = [];
  let counts = { occupied: 0, vacant: 0 };
  let tab = 'all';
  let open = ''; // the room shown in the drawer
  const q = h('input', { type: 'search', class: 'search-box', placeholder: 'Search rooms or guests', 'aria-label': 'Search rooms or guests' });
  const tabs = h('div', { class: 'ht-tabs', role: 'tablist' });
  const grid = h('div', { class: 'ht-grid' });
  const drawer = h('div', { class: 'ht-drawer-back hidden' });

  async function load() {
    const r = await api('GET', dpath('hotel'));
    rooms = r.rooms || [];
    counts = { occupied: r.occupied || 0, vacant: r.vacant || 0 };
  }
  const reload = async () => { await load(); draw(); };
  const guestName = (g) => ((g.first_name || '') + ' ' + (g.last_name || '')).trim();
  const roomOf = (ext) => rooms.find((r) => r.extension === ext);
  const roomLabel = (r) => (r.name || '—') + ' (' + t('ext.') + ' ' + r.extension + ')';

  // ---- windows
  function guestForm(r, g) {
    const first = h('input', { value: g ? g.first_name : '', placeholder: 'e.g. Sarah', maxlength: '60', autocomplete: 'off' });
    const last = h('input', { value: g ? g.last_name : '', placeholder: 'e.g. Bennett', maxlength: '60', autocomplete: 'off' });
    const body = h('div', { class: 'form ht-form' },
      h('div', { class: 'field full' }, h('label', null, t('SIP UID / Room')), h('input', { value: roomLabel(r), readonly: true, class: 'ht-readonly', tabindex: '-1' })),
      h('div', { class: 'field' }, h('label', { class: 'req' }, t('Guest first name')), first),
      h('div', { class: 'field' }, h('label', null, t('Guest last name')), last));
    modal(g ? t('Edit guest') : t('Check in a guest'), body, g ? 'Save' : 'Check in', async () => {
      const v = { extension: r.extension, first_name: first.value.trim(), last_name: last.value.trim() };
      if (!v.first_name && !v.last_name) { toast(t('Type the guest\'s name'), true); first.focus(); return false; }
      if (g) await api('PUT', dpath('hotel/stays/' + g.stay_uuid), v);
      else await api('POST', dpath('hotel/checkin'), v);
      toast(g ? t('Saved') : guestName(v) + ' ' + t('checked in to room') + ' ' + r.extension);
      await reload();
    });
    setTimeout(() => first.focus(), 0);
  }

  async function checkout(r) {
    if (!confirm(t('Check out') + ' ' + guestName(r.guest) + ' (' + t('room') + ' ' + r.extension + ')?' + (r.wakeup && r.wakeup.status === 'scheduled' ? ' ' + t('The wake-up call is cancelled too.') : ''))) return;
    try {
      await api('POST', dpath('hotel/stays/' + r.guest.stay_uuid + '/checkout'));
      toast(guestName(r.guest) + ' ' + t('checked out'));
      await reload();
    } catch (e) { fail(e); }
  }

  function wakeForm(r) {
    const now = new Date();
    const cur = r.wakeup && r.wakeup.status === 'scheduled' ? new Date(r.wakeup.call_at) : null;
    let def = cur;
    if (!def) { def = new Date(now); def.setHours(7, 0, 0, 0); if (def <= now) def.setDate(def.getDate() + 1); }
    const date = h('input', { type: 'date', value: dateValue(def), min: dateValue(now), required: true });
    const time = h('input', { type: 'time', value: pad2(def.getHours()) + ':' + pad2(def.getMinutes()), required: true });
    const sound = selectOf((S.meta ? S.meta.prompts : []).map((p) => [p, p.replace(/^prompts\//, '')]), cur ? r.wakeup.sound : '', t('Beeps (default)'));
    const body = h('div', { class: 'form ht-form' },
      h('div', { class: 'field full' }, h('label', null, t('Room')), h('input', { value: roomLabel(r) + ' · ' + guestName(r.guest), readonly: true, class: 'ht-readonly', tabindex: '-1' })),
      h('div', { class: 'field' }, h('label', { class: 'req' }, t('Date')), date),
      h('div', { class: 'field' }, h('label', { class: 'req' }, t('Time')), time),
      h('div', { class: 'field full' }, h('label', null, t('Wake-up sound')), sound,
        h('div', { class: 'help' }, t('The room rings at this time. Not answered, it rings again 5 minutes later (3 tries).'))));
    modal(cur ? t('Change wake-up call') : t('Schedule wake-up'), body, cur ? 'Save' : 'Schedule', async () => {
      const at = new Date(date.value + 'T' + time.value);
      if (isNaN(at)) { toast(t('Choose a date and time'), true); return false; }
      if (at < new Date(Date.now() - 60000)) { toast(t('That time has passed'), true); return false; }
      await api('POST', dpath('hotel/wakeups'), { extension: r.extension, at: at.toISOString(), sound: sound.value });
      toast(t('Wake-up call') + ': ' + t('room') + ' ' + r.extension + ', ' + hotelWhen(at));
      await reload();
    });
  }

  async function cancelWake(r) {
    if (!confirm(t('Cancel the wake-up call of room') + ' ' + r.extension + '?')) return;
    try { await api('DELETE', dpath('hotel/wakeups/' + r.wakeup.wakeup_uuid)); toast(t('Wake-up call cancelled')); await reload(); } catch (e) { fail(e); }
  }

  // ---- drawing
  function pill(r) {
    return h('span', { class: 'ht-pill ' + r.status }, h('i'), t(r.status === 'occupied' ? 'Occupied' : 'Vacant'));
  }

  function roomCard(r) {
    const w = r.wakeup && r.wakeup.status === 'scheduled' ? r.wakeup : null;
    return h('button', { type: 'button', class: 'ht-room ' + r.status + (open === r.extension ? ' on' : ''), onclick: () => { open = r.extension; draw(); } },
      h('div', { class: 'ht-room-top' }, h('b', null, r.extension), w ? h('span', { class: 'ht-bell', title: t('Wake-up call') + ' ' + hotelWhen(w.call_at) }, icon('bell', 14), hotelWhen(w.call_at).replace(/^.* (?=\d)/, '')) : null),
      h('span', { class: 'ht-ext', title: r.name || '' }, t('Ext') + ' ' + r.extension),
      h('span', { class: 'ht-state' }, h('i', { class: 'dot ' + (r.status === 'occupied' ? 'amber' : 'green') }),
        r.status === 'occupied' ? guestName(r.guest) : t('Vacant')));
  }

  function wakeSection(r) {
    const w = r.wakeup;
    if (r.status !== 'occupied') return h('p', { class: 'muted ht-note' }, t('Check a guest in to schedule a wake-up call.'));
    const planned = w && (w.status === 'scheduled' || w.status === 'calling');
    const [label, tone] = w ? (HOTEL_WAKE[w.status] || [w.status, '']) : ['', ''];
    const last = w && !planned ? h('div', { class: 'ht-last' }, t('Last wake-up call') + ': ' + hotelWhen(w.call_at) + ' · ', badge(t(label), tone),
      w.status === 'no_answer' && w.last_result ? h('span', { class: 'muted small' }, ' ' + w.last_result) : null) : null;
    if (!planned) {
      return h('div', null, h('p', { class: 'muted ht-note' }, t('No wake-up call is scheduled.')), last,
        h('button', { type: 'button', class: 'btn primary', onclick: () => wakeForm(r) }, icon('bell', 16), t('Schedule wake-up')));
    }
    return h('div', null,
      h('div', { class: 'ht-wake' }, h('span', { class: 'ht-wake-icon' }, icon('bell', 20)),
        h('div', null, h('b', null, hotelWhen(w.call_at)),
          h('div', { class: 'small' }, badge(t(label), tone), Number(w.attempts) > 0 ? h('span', { class: 'muted' }, ' ' + t('try') + ' ' + (Number(w.attempts) + (w.status === 'scheduled' ? 1 : 0)) + ' / 3') : null,
            w.sound ? h('span', { class: 'muted' }, ' · ' + w.sound.replace(/^prompts\//, '')) : null))),
      w.status === 'scheduled' ? h('div', { class: 'ht-btns' },
        h('button', { type: 'button', class: 'btn', onclick: () => wakeForm(r) }, icon('edit', 15), t('Change')),
        h('button', { type: 'button', class: 'btn danger', onclick: () => cancelWake(r) }, icon('x', 15), t('Cancel wake-up'))) : null);
  }

  function drawDrawer() {
    const r = open && roomOf(open);
    if (!r) { drawer.classList.add('hidden'); fill(drawer); return; }
    const close = () => { open = ''; draw(); };
    const g = r.guest;
    const panel = h('aside', { class: 'ht-drawer', role: 'dialog', 'aria-label': t('Room') + ' ' + r.extension },
      h('div', { class: 'ht-d-head' },
        h('div', null, h('h2', null, t('Room') + ' ' + r.extension), h('span', { class: 'muted' }, t('Extension') + ' ' + r.extension + (r.name ? ' · ' + r.name : ''))),
        h('span', { class: 'spacer' }), pill(r),
        h('button', { type: 'button', class: 'icon-btn', title: t('Close'), 'aria-label': t('Close'), onclick: close }, icon('x', 18))),
      h('section', { class: 'ht-sec' }, h('h3', null, icon('user', 16), t('Guest')),
        g ? h('div', null,
          h('div', { class: 'ht-guest' }, h('span', { class: 'avatar big' }, initials(guestName(g))),
            h('div', null, h('b', null, guestName(g)), h('span', { class: 'muted small' }, t('Checked in') + ' ' + hotelWhen(g.checked_in_at)))),
          h('div', { class: 'ht-btns' },
            h('button', { type: 'button', class: 'btn', onclick: () => guestForm(r, g) }, icon('edit', 15), t('Edit guest')),
            h('button', { type: 'button', class: 'btn danger-fill', onclick: () => checkout(r) }, icon('logout', 15), t('Check out'))))
          : h('div', null, h('p', { class: 'muted ht-note' }, t('No guest is checked in.')),
            h('button', { type: 'button', class: 'btn primary', onclick: () => guestForm(r, null) }, icon('enter', 16), t('Check in a guest')))),
      h('section', { class: 'ht-sec' }, h('h3', null, icon('bell', 16), t('Wake-up call')), wakeSection(r)),
      h('section', { class: 'ht-sec' }, h('h3', null, icon('phone', 16), t('Phone')),
        h('p', { class: 'ht-note' }, h('i', { class: 'dot ' + (r.registered ? 'green' : 'red') }), t(r.registered ? 'The room phone is registered.' : 'The room phone is not registered.')),
        r.registered && isExt() && S.me.webrtc ? h('button', { type: 'button', class: 'btn', onclick: () => callNumber(r.extension) }, icon('phone', 15), t('Call the room')) : null));
    fill(drawer, panel);
    drawer.classList.remove('hidden');
  }
  drawer.addEventListener('click', (e) => { if (e.target === drawer) { open = ''; draw(); } });

  function draw() {
    const all = rooms.length;
    fill(tabs, [['all', 'All rooms', all], ['occupied', 'Occupied', counts.occupied], ['vacant', 'Vacant', counts.vacant]].map(([k, l, n]) =>
      h('button', { type: 'button', role: 'tab', 'aria-selected': String(tab === k), class: tab === k ? 'on' : '', onclick: () => { tab = k; draw(); } }, t(l) + ' (' + n + ')')));
    const f = q.value.trim().toLowerCase();
    const shown = rooms.filter((r) => (tab === 'all' || r.status === tab) &&
      (!f || (r.extension + ' ' + (r.name || '') + ' ' + (r.guest ? guestName(r.guest) : '')).toLowerCase().includes(f)));
    fill(grid, shown.length ? shown.map(roomCard) : h('div', { class: 'muted ht-empty' }, all ? t('No room matches.') : t('No rooms yet: every extension is a room (Accounts → Extensions).')));
    drawDrawer();
  }
  q.addEventListener('input', draw);
  const onKey = (e) => { if (e.key === 'Escape' && open && !$('.modal-back')) { open = ''; draw(); } };
  document.addEventListener('keydown', onKey);

  fill(main, h('div', { class: 'muted' }, t('Loading…')));
  await Promise.all([load(), loadMeta()]);
  fill(main, h('div', { class: 'page-head' }, h('h1', null, t('Hotel front desk'), h('span', { class: 'info-dot', title: t('Every extension is a room: check guests in and out and schedule wake-up calls') }, icon('info', 15))),
    h('div', { class: 'spacer' }), q), tabs, grid, drawer);
  draw();
  every(15000, async () => {
    if (!document.body.contains(grid)) { document.removeEventListener('keydown', onKey); return; }
    try { await load(); if (!$('.modal-back')) draw(); } catch (e) { /* next time */ }
  });
};
