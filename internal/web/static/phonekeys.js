'use strict';
// Phone keys (busy lamp fields): the programmable keys of an extension's
// desk phone and dialer, kept as RingQ keeps them (one row set per
// extension). Row 1 is key 2 on the desk phone (key 1 stays its own line).
// Settings → Phone keys holds the company's keys, shown in every dialer.

const KEY_TYPES = [['blf', 'Extension (BLF)'], ['speed_dial', 'Speed Dial'], ['park', 'Shared Parking'],
  ['agent_status', 'Agent Status'], ['change_status', 'Change Status'], ['blank', 'Blank']];
const KEY_HELP = {
  blf: 'The lamp shows if that extension is idle, ringing or on a call. Press while it rings to pick the call up, when idle to call it.',
  speed_dial: 'Calls a number with one press.',
  park: 'Press during a call to park it on the slot (the lamp lights); press while lit to take the parked call.',
  agent_status: 'Sets your agent status in all your queues (an aux code, or Available).',
  change_status: 'IN: Available. OUT: the first aux code.',
  blank: 'An empty key.',
};

// keysEditor edits a list of keys: { el, get() }.
function keysEditor(keys, aux) {
  const body = h('tbody');
  const valueWidget = (type, value) => {
    switch (type) {
      case 'blf': {
        const [inp, dl] = datalistInput(value, (S.meta ? S.meta.extensions : []).map((e) => e.extension));
        inp.placeholder = 'e.g. 1045';
        return { el: h('div', null, inp, dl), get: () => inp.value.trim() };
      }
      case 'speed_dial': {
        const inp = h('input', { type: 'text', value: value || '', placeholder: 'Number or extension' });
        inp.value = value || '';
        return { el: inp, get: () => inp.value.trim() };
      }
      case 'park': {
        const sel = selectOf([1, 2, 3, 4, 5].map((n) => [String(n), 'Parking Slot ' + n + ' (*59' + n + ')']), value || '1');
        return { el: sel, get: () => sel.value };
      }
      case 'agent_status': {
        const sel = selectOf((aux || []).map((a, i) => [a, a + ' (*26' + (i + 1) + ')']), value || '', '— choose —');
        return { el: sel, get: () => sel.value };
      }
      case 'change_status': {
        const sel = selectOf([['IN', 'IN (Available)'], ['OUT', 'OUT' + (aux && aux[1] ? ' (' + aux[1] + ')' : '')]], value || 'IN');
        return { el: sel, get: () => sel.value };
      }
    }
    return { el: h('span', { class: 'muted small' }, '—'), get: () => '' };
  };
  const renumber = () => $$('tr', body).forEach((tr, i) => { $('.pk-num', tr).textContent = String(i + 2); });
  const addRow = (k) => {
    k = k || { type: 'blf', value: '' };
    const type = selectOf(KEY_TYPES, k.type || 'blank');
    type.setAttribute('aria-label', 'Key type');
    const cell = h('td', { class: 'pk-value' });
    const help = h('div', { class: 'help pk-help' });
    let w = valueWidget(k.type, k.value);
    const show = () => { fill(cell, w.el, help); help.textContent = t(KEY_HELP[type.value] || ''); };
    type.addEventListener('change', () => { w = valueWidget(type.value, ''); show(); });
    const move = (d) => {
      const sib = d < 0 ? tr.previousElementSibling : tr.nextElementSibling;
      if (!sib) return;
      if (d < 0) body.insertBefore(tr, sib); else body.insertBefore(sib, tr);
      renumber();
    };
    const tr = h('tr', null,
      h('td', { class: 'pk-num' }),
      h('td', null, type), cell,
      h('td', { class: 'pk-acts' },
        h('button', { type: 'button', class: 'icon-btn', title: t('Move up'), 'aria-label': t('Move up'), onclick: () => move(-1) }, icon('up', 15)),
        h('button', { type: 'button', class: 'icon-btn', title: t('Move down'), 'aria-label': t('Move down'), onclick: () => move(1) }, icon('down', 15)),
        h('button', { type: 'button', class: 'icon-btn', title: t('Remove'), 'aria-label': t('Remove'), onclick: () => { tr.remove(); renumber(); } }, icon('trash', 15))));
    tr._get = () => ({ type: type.value, value: w.get() });
    show();
    body.append(tr);
    renumber();
  };
  (keys || []).forEach(addRow);
  const el = h('div', { class: 'pk-editor' },
    h('div', { class: 'table-wrap' }, h('table', { class: 'child-table pk-table' },
      h('thead', null, h('tr', null, h('th', { title: t('Key 1 is the phone\'s own line') }, t('Key')), h('th', null, t('Type')), h('th', null, t('Value')), h('th'))),
      body)),
    h('button', { type: 'button', class: 'btn small', onclick: () => addRow() }, icon('plus', 14), t('Add a key')));
  return { el, get: () => $$('tr', body).map((tr) => tr._get()) };
}

// phoneKeysPanel is the Phone keys section of an extension's page.
function phoneKeysPanel(ext) {
  const box = h('div', { class: 'panel pk-panel' }, h('div', { class: 'muted' }, t('Loading…')));
  (async () => {
    let data;
    try { data = await api('GET', dpath('phonekeys/' + encodeURIComponent(ext))); } catch (e) { fill(box, h('p', { class: 'error-text' }, e.message)); return; }
    const ed = keysEditor(data.keys, data.aux);
    const err = h('div', { class: 'error-text ai-err' });
    const save = h('button', { type: 'button', class: 'btn primary' }, icon('check', 15), t('Save keys'));
    save.addEventListener('click', async () => {
      err.textContent = '';
      save.disabled = true;
      try { await api('PUT', dpath('phonekeys/' + encodeURIComponent(ext)), { keys: ed.get() }); toast(t('Phone keys saved. Reboot the phone so it fetches them.')); } catch (e) { err.textContent = e.message; } finally { save.disabled = false; }
    });
    const reboot = h('button', { type: 'button', class: 'btn' }, icon('refresh', 15), t('Reboot phone'));
    reboot.addEventListener('click', async () => {
      if (!confirm(t('Restart the desk phones of extension') + ' ' + ext + '? ' + t('A call in progress on them ends.'))) return;
      try {
        const r = await api('POST', dpath('phonekeys/' + encodeURIComponent(ext) + '/reboot'));
        toast(r.phones ? t('Reboot sent to') + ' ' + r.phones + ' ' + t('phone(s)') : t('No phone of this extension is registered'), !r.phones);
      } catch (e) { fail(e); }
    });
    fill(box,
      h('div', { class: 'panel-head' }, h('h2', null, t('Phone keys (BLF)')), h('span', { class: 'spacer' }), reboot),
      h('p', { class: 'muted small' }, t('Each row is one programmable key on the desk phone, in order: row 1 is key 2 (key 1 stays the phone\'s own line). The browser dialer shows the same keys with their lamps. After saving, reboot the phone so it fetches its new keys.')),
      ed.el, err, h('div', { class: 'form-actions' }, save),
      data.company && data.company.length ? h('p', { class: 'muted small' }, t('The company\'s keys (Settings → Phone keys) come after these in the dialer.')) : null);
  })();
  return box;
}

// Settings → Phone keys: the keys every dialer of the company shows.
PAGES.phonekeys = async () => {
  const main = $('#main');
  await loadMeta();
  const data = await api('GET', dpath('phonekeys'));
  const ed = keysEditor(data.keys, data.aux);
  const show = h('input', { type: 'checkbox', checked: data.show_dialer !== false });
  const err = h('div', { class: 'error-text ai-err' });
  const save = h('button', { type: 'button', class: 'btn primary' }, icon('check', 15), t('Save'));
  save.addEventListener('click', async () => {
    err.textContent = '';
    save.disabled = true;
    try { await api('PUT', dpath('phonekeys'), { keys: ed.get(), show_dialer: show.checked }); toast('Saved'); } catch (e) { err.textContent = e.message; } finally { save.disabled = false; }
  });
  fill(main, pageHead('Phone keys'),
    h('div', { class: 'panel' },
      h('h2', null, t('Company keys')),
      h('p', { class: 'muted' }, t('Keys every dialer of the company shows after the extension\'s own keys: reception, the parking slots, a sales speed dial... An extension\'s own keys are set on its page (Extensions → the extension → Phone keys).')),
      h('label', { class: 'switch' }, show, t('Show in every dialer')),
      ed.el, err, h('div', { class: 'form-actions' }, save)),
    h('div', { class: 'panel' }, h('h2', null, t('Dial codes')),
      table([{ label: 'Dial', render: (r) => h('code', null, r[0]) }, { label: 'Does', key: 1, render: (r) => r[1] }], [
        ['*8 + extension', t('Picks up a call ringing on that extension (BLF keys use it while the lamp blinks).')],
        ['*8', t('Picks up a call ringing in your pickup group.')],
        ['*591 … *595', t('Parking slots 1 to 5: during a call, transfer to it to park; when idle, dial it to take the parked call.')],
        ['*26 + number', t('Sets your agent status (the numbers are on the Aux codes page; *261 is Available).')],
        ['*78 / *79 / *77', t('Do not disturb on / off / on-off (the same setting as in the apps).')],
        ['Your own extension', t('Your voicemail (the phone\'s Message key).')],
        ['*99 + extension', t('Leave a message in that extension\'s voicemail (transfer a caller to it).')],
        ['*88 + team', t('Leave a message in a team (group) voicemail box.')],
        ['*69', t('Calls back the last caller.')],
        ['*870', t('Redials the last number.')],
      ])));
};
