'use strict';
// BLF and parked calls beside the dialer (as in the RingQ apps): a small
// handle on the dialer's left edge opens a strip of tabs, BLF and Parked
// (with how many calls are parked); a tab slides its list out to the left.
// It goes on the dialer's right when the dialer was moved to the left edge,
// and over the dialer on small screens. The keys come from the
// extension's Phone keys (and the company's); their lamps follow calls
// live. During a call an extension key transfers it and a parking slot
// parks it; a ringing extension is picked up; a lit slot gives the parked
// call back.

const PhoneSide = (() => {
  let root = null; // the strip and the drawer
  let handle = null; // < / > on the edge
  let data = null; // GET /api/keys
  let view = ''; // '', 'blf' or 'park'
  let expanded = false; // the strip of tabs is shown
  let live = null; // the call being talked on
  let timer = null;
  let side = 'left';
  const STRIP = 44;
  const WIDTH = { blf: 236, park: 196 };
  try { expanded = localStorage.getItem('rq-dialer-side') === '1'; view = localStorage.getItem('rq-dialer-side-view') || ''; } catch (e) { /* private mode */ }
  if (!WIDTH[view]) view = '';

  function save() {
    try {
      localStorage.setItem('rq-dialer-side', expanded ? '1' : '0');
      localStorage.setItem('rq-dialer-side-view', view);
    } catch (e) { /* private mode */ }
  }

  function init() {
    if (root) return;
    root = h('aside', { id: 'ph-side', class: 'phs hidden', 'aria-label': t('BLF and parked calls') });
    handle = h('button', { id: 'ph-side-handle', class: 'phs-handle hidden', type: 'button', onclick: () => setExpanded(!expanded) });
    $('#phone').after(root, handle);
    // How long calls have been parked or on a call.
    setInterval(() => {
      if (root.classList.contains('hidden')) return;
      const now = Date.now() / 1000 - S.clockSkew;
      $$('#ph-side [data-since]').forEach((el) => { el.textContent = fmtDur(now - Number(el.dataset.since)); });
    }, 1000);
    on('blf', () => soon());
    on('keys', () => soon());
    on('presence', () => soon());
    on('ws.open', () => soon());
  }

  const dialerOpen = () => window.Phone && Phone.isOpen && Phone.isOpen() && !$('#phone').classList.contains('ph-out');

  function soon() {
    if (!dialerOpen()) return;
    clearTimeout(timer);
    timer = setTimeout(load, 150);
  }

  async function load() {
    try { data = await api('GET', 'keys'); } catch (e) { data = { keys: [], parks: [], error: e.message }; }
    draw();
  }

  function setExpanded(on) {
    expanded = on;
    if (!on) view = '';
    save();
    place();
    draw();
    if (on && !data) load();
    if (window.LiveTr) LiveTr.place(); // the transcript goes beyond the drawer
  }

  function setView(v) {
    view = view === v ? '' : v;
    save();
    place();
    draw();
    if (!data) load();
    if (window.LiveTr) LiveTr.place();
  }

  // width is what the strip and drawer take beside the dialer.
  const width = () => (expanded ? STRIP : 0) + (expanded && view ? WIDTH[view] : 0);

  // extent is the room taken on each side of the dialer (the transcript
  // panel goes beyond it).
  function extent() {
    if (!root || root.classList.contains('hidden') || window.innerWidth <= 760) return { left: 0, right: 0 };
    const w = width() + 14; // and the handle
    return side === 'left' ? { left: w, right: 0 } : { left: 0, right: w };
  }

  // place keeps the strip and drawer against the dialer.
  function place() {
    if (!root) return;
    const ph = $('#phone');
    if (!dialerOpen()) {
      root.classList.add('hidden');
      handle.classList.add('hidden');
      ph.classList.remove('ph-joined-left', 'ph-joined-right');
      return;
    }
    if (root.classList.contains('hidden') && !data) load();
    const W = window.innerWidth;
    const b = Phone.box();
    const area = Phone.bounds ? Phone.bounds() : { left: 8, top: 72 };
    const narrow = W <= 760;
    const full = STRIP + (view ? WIDTH[view] : 0);
    const was = side;
    side = narrow || b.left - full >= area.left || b.right + full > W - 8 ? 'left' : 'right';
    root.dataset.side = handle.dataset.side = side;
    if (was !== side) draw();
    root.classList.toggle('phs-narrow', narrow);
    root.classList.remove('hidden');
    handle.classList.remove('hidden');
    const w = width();
    const box = { top: b.top + 'px', height: b.height + 'px', width: w + 'px' };
    if (narrow) Object.assign(root.style, box, { left: b.left + 'px', right: 'auto' });
    else if (side === 'left') Object.assign(root.style, box, { right: (W - b.left) + 'px', left: 'auto' });
    else Object.assign(root.style, box, { left: b.right + 'px', right: 'auto' });
    root.classList.toggle('phs-open', expanded);
    ph.classList.toggle('ph-joined-left', expanded && side === 'left' && !narrow);
    ph.classList.toggle('ph-joined-right', expanded && side === 'right' && !narrow);
    const hx = narrow ? b.left + w : side === 'left' ? b.left - w - 14 : b.right + w;
    Object.assign(handle.style, { left: hx + 'px', top: (b.top + b.height / 2 - 24) + 'px' });
    const openIc = side === 'left' ? 'left' : 'right', closeIc = side === 'left' ? 'right' : 'left';
    fill(handle, icon(expanded ? closeIc : openIc, 14));
    handle.title = t(expanded ? 'Hide BLF and parked calls' : 'Show BLF and parked calls');
    handle.setAttribute('aria-label', handle.title);
    handle.setAttribute('aria-expanded', String(expanded));
  }

  // calls is told by the dialer when its calls change.
  function calls(list) {
    const now = (list || []).find((c) => c.state === 'answered' && !c.held) || null;
    if ((now && now.id) !== (live && live.id)) { live = now; draw(); }
    if (!root) return;
    if (dialerOpen() && root.classList.contains('hidden')) place();
  }

  async function press(k) {
    const call = Phone.liveCall();
    switch (k.type) {
      case 'blf':
        if (k.state === 'ringing') { toast(t('Picking up the call ringing') + ' ' + k.value); Phone.dial('*8' + k.value); return; }
        if (call) {
          const r = await Phone.transfer(call.id, k.value);
          if (r !== null) toast(t('Transferred to') + ' ' + k.label);
          return;
        }
        Phone.dial(k.value);
        return;
      case 'speed_dial':
        Phone.dial(k.value);
        return;
      case 'park':
        if (call) {
          const r = await Phone.transfer(call.id, k.dial);
          if (r !== null) toast(t('Parked on') + ' P' + k.value + ' (' + k.dial + ')');
          soon();
          return;
        }
        if (k.state === 'busy') { Phone.dial(k.dial); return; }
        toast('P' + k.value + ' ' + t('is empty'));
        return;
      case 'agent_status':
      case 'change_status': {
        if (!Phone.isAgent()) { toast(t('Your extension is not a queue agent'), true); return; }
        const aux = (data && data.aux) || [];
        const name = k.type === 'agent_status' ? k.value : (k.value === 'IN' ? 'Available' : aux[1]);
        if (!name) return;
        if (name.toLowerCase() === 'available') await Phone.setStatus('Available'); else await Phone.setStatus('On Break', name);
        soon();
      }
    }
  }

  // since is a timer counting from a time (seconds), kept going by init.
  const since = (at, tag) => h(tag, { 'data-since': at }, fmtDur(Date.now() / 1000 - S.clockSkew - at));

  function lamp(k) {
    switch (k.type) {
      case 'blf': return k.state === 'ringing' ? ['ringing', t('Ringing') + (k.number ? ' · ' + k.number : '')] : k.state === 'busy' ? ['busy', t('On a call')] : ['idle', t('Idle')];
      case 'park': return k.state === 'busy' ? ['parked', k.name && k.name !== k.number ? k.name : k.number || t('Parked')] : ['empty', t('Empty')];
      case 'agent_status': case 'change_status': return [k.state === 'on' ? 'on' : 'off', k.dial || ''];
      case 'speed_dial': return ['none', t('Speed dial')];
    }
    return ['none', ''];
  }

  function blfRow(k) {
    if (k.type === 'blank') return null;
    const [l, sub] = lamp(k);
    const hint = k.type === 'blf' ? (k.state === 'ringing' ? t('Pick up') : live ? t('Transfer the call to') + ' ' + k.value : t('Call') + ' ' + k.value)
      : k.type === 'speed_dial' ? t('Call') + ' ' + k.value : t('Set') + ' ' + k.label;
    return h('button', { type: 'button', class: 'phs-key l-' + l + ' t-' + k.type, title: hint, 'aria-label': k.label + ': ' + sub + '. ' + hint, onclick: () => press(k) },
      h('span', { class: 'phs-lamp' }),
      h('span', { class: 'phs-key-txt' }, h('b', null, k.label), h('small', null, sub,
        k.since && k.state === 'busy' ? [' · ', since(k.since, 'span')] : null)));
  }

  function parkRow(k) {
    const [l, sub] = lamp(k);
    const hint = live ? t('Park the call on') + ' P' + k.value : k.state === 'busy' ? t('Take the parked call') : t('Empty');
    return h('button', { type: 'button', class: 'phs-park l-' + l, title: hint + ' (' + k.dial + ')', 'aria-label': 'P' + k.value + ': ' + sub + '. ' + hint,
      onclick: () => press(k) },
    h('b', null, 'P' + k.value),
    h('span', { class: 'phs-park-txt' }, h('span', { class: 'ellipsis' }, sub),
      k.since && k.state === 'busy' ? since(k.since, 'small') : null));
  }

  function draw() {
    if (!root) return;
    const parks = (data && data.parks) || [];
    const parked = parks.filter((p) => p.state === 'busy').length;
    const keys = (data && data.keys) || [];
    const ringing = keys.some((k) => k.state === 'ringing');
    const tabBtn = (v, label, badge) => h('button', { type: 'button', class: 'phs-tab' + (view === v ? ' on' : ''), role: 'tab', 'aria-selected': String(view === v),
      onclick: () => setView(v) }, h('span', { class: 'phs-tab-label' }, t(label)), badge);
    const strip = h('div', { class: 'phs-strip', role: 'tablist', 'aria-orientation': 'vertical' },
      tabBtn('blf', 'BLF', ringing ? h('i', { class: 'phs-ring', title: t('A key is ringing') }) : null),
      tabBtn('park', 'Parked', h('b', { class: 'phs-count' + (parked ? ' some' : '') }, String(parked))));
    let body = null;
    if (view) {
      const mine = keys.filter((k) => k.group === 'mine');
      const company = keys.filter((k) => k.group === 'company');
      const close = h('button', { type: 'button', class: 'icon-btn', title: t('Close'), 'aria-label': t('Close'), onclick: () => setView(view) }, icon('x', 15));
      body = h('div', { class: 'phs-drawer', style: 'width:' + WIDTH[view] + 'px' },
        h('div', { class: 'phs-head' }, h('b', null, t(view === 'blf' ? 'BLF' : 'Parked Calls')), h('span', { class: 'spacer' }), close),
        live ? h('div', { class: 'phs-hint' }, t(view === 'blf' ? 'Click an extension to transfer the call.' : 'Click a slot to park the call.')) : null,
        data && data.error ? h('div', { class: 'error-text small phs-hint' }, data.error) : null,
        !data ? h('div', { class: 'muted small phs-hint' }, t('Loading…')) : view === 'blf'
          ? h('div', { class: 'phs-list' },
            mine.length ? mine.map(blfRow) : h('div', { class: 'muted small phs-none' }, t('No BLF keys yet. They are set on your extension\'s page (Phone keys).')),
            company.length ? [h('div', { class: 'phs-sub' }, t('Company')), company.map(blfRow)] : null)
          : h('div', { class: 'phs-list' }, parks.map(parkRow),
            h('div', { class: 'muted small phs-none' }, t('Slots *591 to *595, shared with desk phones.'))));
    }
    // The drawer is on the outside, the strip against the dialer.
    fill(root, side === 'left' ? [body, strip] : [strip, body]);
  }

  return { init, place, calls, extent, reload: load };
})();
window.PhoneSide = PhoneSide;
