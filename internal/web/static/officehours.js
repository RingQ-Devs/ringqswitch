'use strict';
// Language & office hours (as in the RingQ portal): the company's language
// and time zone, its office hours and breaks with what happens to calls
// outside them, holidays with their greetings, and the IVRs, queues and
// agents that stay open on holidays. Changes are saved as they are made.

const DAY_ORDER = [1, 2, 3, 4, 5, 6, 0]; // Monday first (index 0 is Sunday)
const DAY_NAMES = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const DAY_FULL = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];

// ampm turns "18:01" into "6:01 PM".
function ampm(hm) {
  const [hh, mm] = String(hm || '0:0').split(':').map(Number);
  return ((hh % 12) || 12) + ':' + String(mm || 0).padStart(2, '0') + ' ' + (hh < 12 || hh === 24 ? 'AM' : 'PM');
}

function timeZones() {
  try { return Intl.supportedValuesOf('timeZone'); } catch (e) {
    return ['UTC', 'Asia/Singapore', 'Asia/Kuala_Lumpur', 'Asia/Shanghai', 'Asia/Hong_Kong', 'Asia/Kolkata', 'Asia/Dubai', 'Asia/Tokyo',
      'Australia/Sydney', 'Europe/London', 'Europe/Paris', 'America/New_York', 'America/Chicago', 'America/Los_Angeles'];
  }
}

// soundPicker: a select of the prompts with an Upload button (WAV or MP3,
// converted on the server). onChange gets the chosen sound ("" = none).
function soundPicker(value, sounds, blank, onChange) {
  const sel = selectOf(sounds.map((s) => [s, s.replace(/^prompts\//, '')]), value, blank);
  sel.addEventListener('change', () => onChange(sel.value));
  const file = h('input', { type: 'file', accept: '.wav,.mp3,audio/wav,audio/mpeg', class: 'hidden' });
  const up = h('button', { type: 'button', class: 'btn small', title: t('Upload a sound (MP3 or WAV, up to 10 MB)') }, icon('upload', 14), t('Upload'));
  up.addEventListener('click', () => file.click());
  file.addEventListener('change', async () => {
    const f = file.files[0];
    file.value = '';
    if (!f) return;
    if (f.size > 10 * 1024 * 1024) { toast(t('The file is larger than 10 MB'), true); return; }
    const name = f.name.replace(/\.(mp3|wav)$/i, '').replace(/[^A-Za-z0-9 ._-]/g, '_').slice(0, 60) + '.wav';
    up.disabled = true;
    try {
      const res = await fetch('/api/sounds?path=' + encodeURIComponent('prompts/' + name), { method: 'POST', headers: { 'X-RQ': '1' }, body: f });
      const out = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(out.error || res.statusText);
      const path = 'prompts/' + name;
      if (!sounds.includes(path)) { sounds.push(path); sel.append(h('option', { value: path }, name)); }
      sel.value = path;
      toast(t('Uploaded') + ' ' + name + ' (' + out.seconds + ' s)');
      onChange(path);
    } catch (e) { fail(e); } finally { up.disabled = false; }
  });
  return h('div', { class: 'oh-sound' }, h('span', { class: 'oh-ic' }, icon('music', 15)), sel, up, file);
}

PAGES.officehours = async () => {
  const main = $('#main');
  await loadMeta();
  const d = await api('GET', dpath('officehours'));
  const status = h('span', { class: 'oh-saved muted small', 'aria-live': 'polite' });
  let saveTimer = null;
  const save = () => {
    clearTimeout(saveTimer);
    status.textContent = t('Saving…');
    saveTimer = setTimeout(async () => {
      try {
        await api('PUT', dpath('officehours'), { language: d.language, timezone: d.timezone, work: d.work, work_action: d.work_action,
          break: d.break, break_action: d.break_action, holiday_sound: d.holiday_sound, exempt: d.exempt });
        status.textContent = t('All changes saved');
      } catch (e) { status.textContent = ''; fail(e); }
    }, 350);
  };

  // Language & time zone, and the time there now.
  const langs = d.languages.map((l) => [l.code, l.name]);
  if (!langs.some((l) => l[0] === d.language)) langs.push([d.language, d.language + ' (' + t('not installed') + ')']);
  const lang = selectOf(langs, d.language);
  lang.addEventListener('change', () => { d.language = lang.value; save(); });
  const tz = selectOf(timeZones().map((z) => [z, z.replace(/_/g, ' ')]), d.timezone, t('Server time zone') + (d.server_timezone ? ' (' + d.server_timezone + ')' : ''));
  const clock = h('span');
  const tick = () => {
    const now = new Date((Date.now() / 1000 - S.clockSkew) * 1000);
    try {
      clock.textContent = new Intl.DateTimeFormat('en-US', { timeZone: d.timezone || undefined, month: 'short', day: 'numeric', year: 'numeric',
        hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: true }).format(now).replace(',', '').replace(/, (\d+:)/, ' $1');
    } catch (e) { clock.textContent = now.toLocaleString(); }
  };
  tz.addEventListener('change', () => { d.timezone = tz.value; tick(); save(); });
  tick();
  every(1000, tick);
  const field = (label, ic, el) => h('label', { class: 'oh-field' }, h('span', { class: 'oh-label' }, t(label)), h('div', { class: 'oh-input' }, h('span', { class: 'oh-ic' }, icon(ic, 15)), el));

  // A week of day cards; click a day to change it.
  const week = (key, office) => {
    const box = h('div', { class: 'oh-week' });
    const draw = () => fill(box, DAY_ORDER.map((i) => {
      const day = d[key][i];
      const kind = day.mode === 'hours' ? 'hours' : office ? day.mode : 'none';
      const txt = kind === 'hours' ? [h('span', null, ampm(day.from)), h('span', null, ampm(day.to))]
        : [h('span', null, t({ open: 'Open', closed: 'Closed', none: 'No break' }[kind]))];
      return h('button', { type: 'button', class: 'oh-day k-' + kind, title: t('Change') + ' ' + t(DAY_FULL[i]),
        onclick: () => editDay(i, key, office, draw) }, h('b', null, t(DAY_NAMES[i])), h('div', { class: 'oh-times' }, txt));
    }));
    draw();
    return box;
  };
  const editDay = (i, key, office, redraw) => {
    const day = Object.assign({}, d[key][i]);
    const choices = office ? [['open', 'Open all day'], ['hours', 'Open these hours'], ['closed', 'Closed all day']]
      : [['none', 'No break'], ['hours', 'Break at these times']];
    const from = h('input', { type: 'time', value: day.from || (office ? '09:00' : '12:00'), required: true });
    const to = h('input', { type: 'time', value: day.to || (office ? '18:00' : '13:00'), required: true });
    const times = h('div', { class: 'oh-range' }, h('label', null, t('From'), from), h('label', null, t('To'), to));
    const all = h('label', { class: 'switch' }, h('input', { type: 'checkbox' }), t('The same for every day'));
    let mode = day.mode === 'hours' ? 'hours' : (office ? day.mode || 'open' : 'none');
    const radios = choices.map(([v, l]) => {
      const r = h('input', { type: 'radio', name: 'oh-mode', value: v, checked: v === mode });
      r.addEventListener('change', () => { mode = v; times.classList.toggle('hidden', v !== 'hours'); });
      return h('label', { class: 'oh-choice' }, r, t(l));
    });
    times.classList.toggle('hidden', mode !== 'hours');
    modal((office ? t('Office hours') : t('Break')) + ': ' + t(DAY_FULL[i]), h('div', { class: 'oh-edit' }, radios, times, all), 'Apply', () => {
      if (mode === 'hours' && (!from.value || !to.value || from.value === to.value)) { toast(t('Give different start and end times'), true); return false; }
      const v = mode === 'hours' ? { mode, from: from.value, to: to.value } : { mode };
      if (all.querySelector('input').checked) d[key] = d[key].map(() => Object.assign({}, v)); else d[key][i] = v;
      redraw();
      save();
      return true;
    });
  };
  const actionSel = (key) => {
    const sel = destOptions(d[key]);
    sel.options[0].textContent = t('None (calls go through)');
    sel.addEventListener('change', () => { d[key] = sel.value; save(); });
    return sel;
  };

  // Holidays.
  const holBox = h('div', { class: 'oh-holidays' });
  const fmtDay = (iso, rec) => {
    const [y, m, dd] = iso.split('-').map(Number);
    const dt = new Date(Date.UTC(y, m - 1, dd));
    return dt.toLocaleDateString('en-US', rec ? { month: 'short', day: 'numeric', timeZone: 'UTC' } : { month: 'short', day: 'numeric', year: 'numeric', timeZone: 'UTC' });
  };
  const drawHolidays = () => {
    if (!d.holidays.length) {
      fill(holBox, h('div', { class: 'oh-empty' }, h('span', { class: 'oh-empty-ic' }, icon('calendar', 20)), h('b', null, t('No holidays yet')),
        h('p', null, t('Add public holidays and closures so callers hear your holiday greeting on those dates.')),
        h('button', { type: 'button', class: 'btn', onclick: () => editHoliday(null) }, icon('plus', 15), t('Create holiday'))));
      return;
    }
    fill(holBox,
      h('div', { class: 'oh-hol-list' }, d.holidays.map((x) => h('div', { class: 'oh-hol' },
        h('span', { class: 'oh-hol-ic' }, icon('calendar', 16)),
        h('div', { class: 'oh-hol-txt' }, h('b', null, x.name),
          h('small', null, fmtDay(x.start, x.recurring) + (x.end !== x.start ? ' – ' + fmtDay(x.end, x.recurring) : '') +
            (x.recurring ? ' · ' + t('every year') : '') + (x.from ? ' · ' + ampm(x.from) + ' – ' + ampm(x.to) : ' · ' + t('all day')) +
            (x.greeting ? ' · ' + x.greeting.replace(/^prompts\//, '') : '') + (x.only && x.only.length ? ' · ' + t('only') + ' ' + x.only.join(', ') : ''))),
        h('button', { type: 'button', class: 'icon-btn', title: t('Edit'), 'aria-label': t('Edit') + ' ' + x.name, onclick: () => editHoliday(x) }, icon('edit', 15)),
        h('button', { type: 'button', class: 'icon-btn', title: t('Delete'), 'aria-label': t('Delete') + ' ' + x.name, onclick: async () => {
          if (!confirm(t('Delete the holiday') + ' ' + x.name + '?')) return;
          try { await api('DELETE', dpath('officehours/holidays/' + x.id)); d.holidays = d.holidays.filter((y) => y !== x); drawHolidays(); toast(t('Deleted')); } catch (e) { fail(e); }
        } }, icon('trash', 15))))),
      h('button', { type: 'button', class: 'btn small oh-add', onclick: () => editHoliday(null) }, icon('plus', 14), t('Create holiday')));
  };
  const editHoliday = (x) => {
    const today = new Date().toISOString().slice(0, 10);
    const f = {
      name: h('input', { value: x ? x.name : '', placeholder: t('e.g. Christmas Day'), maxlength: 80, required: true }),
      start: h('input', { type: 'date', value: x ? x.start : today, required: true }),
      end: h('input', { type: 'date', value: x ? x.end : '' }),
      recurring: h('input', { type: 'checkbox', checked: x ? x.recurring : true }),
      allday: h('input', { type: 'checkbox', checked: x ? !x.from : true }),
      from: h('input', { type: 'time', value: x && x.from ? x.from : '13:00' }),
      to: h('input', { type: 'time', value: x && x.to ? x.to : '18:00' }),
    };
    let greeting = x ? x.greeting : '';
    const hours = h('div', { class: 'oh-range' }, h('label', null, t('From'), f.from), h('label', null, t('To'), f.to));
    hours.classList.toggle('hidden', f.allday.checked);
    f.allday.addEventListener('change', () => hours.classList.toggle('hidden', f.allday.checked));
    const fld = (l, el, help) => h('div', { class: 'field' }, h('label', null, t(l)), el, help ? h('div', { class: 'help' }, t(help)) : null);
    modal(x ? t('Edit holiday') : t('Create holiday'), h('div', { class: 'form oh-hol-form' },
      fld('Name', f.name),
      fld('Date', f.start), fld('Until (for several days)', f.end, 'Leave empty for one day.'),
      h('label', { class: 'switch' }, f.recurring, t('Every year on this date')),
      h('label', { class: 'switch' }, f.allday, t('All day')), hours,
      fld('Greeting', soundPicker(greeting, d.sounds, t('The holiday sound default'), (v) => { greeting = v; }),
        'What callers hear before the call ends. Empty: the holiday sound default.')), 'Save', async () => {
      const body = { name: f.name.value.trim(), start: f.start.value, end: f.end.value || f.start.value, recurring: f.recurring.checked,
        from: f.allday.checked ? '' : f.from.value, to: f.allday.checked ? '' : f.to.value, greeting };
      const r = await api(x ? 'PUT' : 'POST', dpath('officehours/holidays' + (x ? '/' + x.id : '')), body);
      const row = Object.assign({ id: Number(r.id) }, body);
      if (x) Object.assign(x, row); else d.holidays.push(row);
      d.holidays.sort((a, b) => a.start.localeCompare(b.start));
      drawHolidays();
      toast(t('Holiday saved'));
    });
  };
  drawHolidays();

  // Numbers that stay open on holidays.
  const exemptBox = h('div', { class: 'oh-exempt' });
  const drawExempt = () => {
    const label = (n) => { const c = d.exempt_choices.find((x) => x.value === n); return c ? c.group + ' ' + c.label : n; };
    const chips = d.exempt.map((n) => h('span', { class: 'chip' }, label(n), h('button', { type: 'button', title: t('Remove'), 'aria-label': t('Remove') + ' ' + n,
      onclick: (e) => { e.stopPropagation(); d.exempt = d.exempt.filter((y) => y !== n); drawExempt(); save(); } }, '×')));
    const open = h('button', { type: 'button', class: 'oh-exempt-box', 'aria-haspopup': 'listbox' },
      chips.length ? chips : h('span', { class: 'muted' }, t('Select IVRs, queues, or agents to exempt')), h('span', { class: 'oh-caret' }, icon('down', 14)));
    open.addEventListener('click', () => {
      const boxes = {};
      const groups = {};
      for (const c of d.exempt_choices) (groups[c.group] = groups[c.group] || []).push(c);
      const body = Object.keys(groups).length ? Object.entries(groups).map(([g, list]) => h('div', { class: 'oh-pick-group' }, h('div', { class: 'oh-sub' }, t(g === 'IVR' ? 'IVR menus' : g + 's')),
        h('div', { class: 'chip-pick' }, list.map((c) => {
          boxes[c.value] = h('input', { type: 'checkbox', checked: d.exempt.includes(c.value) });
          return h('label', { class: 'pick' }, boxes[c.value], c.label);
        }))))
        : h('p', { class: 'muted' }, t('No IVR menus, queues or agents yet.'));
      modal(t('Exempt from holidays'), h('div', null, h('p', { class: 'muted small' }, t('These stay open on holidays: calls to them go through as usual.')), body), 'Apply', () => {
        d.exempt = d.exempt.filter((n) => !(n in boxes)).concat(Object.keys(boxes).filter((n) => boxes[n].checked));
        drawExempt();
        save();
      });
    });
    fill(exemptBox, open);
  };
  drawExempt();

  const side = (label, el, help) => h('div', { class: 'oh-side' }, h('span', { class: 'oh-label' }, t(label)), el, h('div', { class: 'help' }, t(help)));
  // Limits set in the RingQ portal (kept as they are).
  const only = (list) => list && list.length ? h('p', { class: 'help oh-only' }, icon('info', 13), ' ' + t('Applies only to calls to') + ' ' + list.join(', ') + ' (' + t('set in RingQ') + ').') : null;
  fill(main,
    h('div', { class: 'page-head' }, h('h1', null, t('Language & office hours'), ' ',
      h('span', { class: 'info-dot', title: t('When your company is open, and what callers from outside hear when it is not') }, icon('info', 15))),
    h('div', { class: 'spacer' }), status,
    h('button', { type: 'button', class: 'btn', onclick: () => history.back() }, icon('left', 15), t('Back'))),
    h('section', { class: 'panel oh-panel' }, h('h2', null, t('Language & timezone')),
      h('div', { class: 'oh-grid3' }, field('Language', 'globe', lang), field('Timezone', 'clock', tz),
        h('div', { class: 'oh-field' }, h('span', { class: 'oh-label' }, t('Current time')), h('div', { class: 'oh-input oh-readonly' }, h('span', { class: 'oh-ic' }, icon('clock', 15)), clock))),
      h('p', { class: 'help' }, t('Office hours, breaks, holidays and time conditions follow this time zone. The language picks the voice prompts callers hear (when that language is installed).'))),
    h('section', { class: 'panel oh-panel' }, h('h2', null, t('Global office hours')),
      h('div', { class: 'oh-row' }, week('work', true), side('Non-office hours action', actionSel('work_action'), 'Triggers after office hours.')), only(d.work_only)),
    h('section', { class: 'panel oh-panel' }, h('h2', null, t('Global break hours')),
      h('div', { class: 'oh-row' }, week('break', false), side('Break time action', actionSel('break_action'), 'Triggers during break times.')), only(d.break_only)),
    h('section', { class: 'panel oh-panel' }, h('h2', null, t('Global holidays')),
      h('div', { class: 'oh-row' }, holBox, side('Holiday sound default', soundPicker(d.holiday_sound, d.sounds, t('Choose or upload a sound'), (v) => { d.holiday_sound = v; save(); }),
        'Plays during holiday times, then the call ends. MP3 or WAV, up to 10 MB.')), only(d.holiday_only)),
    h('section', { class: 'panel oh-panel' }, h('h2', null, t('Exempt from holidays')), exemptBox),
    h('p', { class: 'help' }, t('These apply to calls from outside (your DIDs), before they reach the DID\'s destination. Calls between extensions are not affected.')));
};
