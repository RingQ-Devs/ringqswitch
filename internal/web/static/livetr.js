'use strict';
// The live transcript: while one of your calls goes on and live
// transcription is on (Settings → Call transcription), what both of you
// say is written down here as it is said: the other party on the left, you
// on the right; while someone speaks, their words so far show in the
// bubble. It slides out beside the dialer (on its left, or its right when
// the dialer was moved to the left edge) when the call is answered; the
// Transcript tab brings it back after it is closed. Nothing is saved: Copy
// keeps it.

const LiveTr = (() => {
  let sessions = {}; // id → { id, me, other, otherName, started, ended, lines, speaking, partial, nodes }
  let cur = null; // the session shown
  let open = false;
  let closedFor = ''; // the session whose panel was closed by hand: it does not open again by itself
  let unread = 0;
  let stick = true; // follow the newest line
  let panel = null;
  let tab = null;
  let list = null;
  let clock = null;
  const reduced = () => window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  function clockText(ms) {
    const s = Math.max(0, Math.floor(ms / 1000));
    return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0');
  }

  let inited = false;
  function init() {
    if (inited) return;
    inited = true;
    panel = h('section', { id: 'live-tr', class: 'lt lt-shut', 'aria-label': 'Live transcript', 'aria-hidden': 'true' });
    tab = h('button', { id: 'live-tr-tab', class: 'lt-tab hidden', type: 'button', title: 'Show the live transcript', 'aria-label': 'Show the live transcript', onclick: () => show(true) });
    $('#phone-btn').after(panel, tab);
    on('live.start', onStart);
    on('live.speak', onSpeak);
    on('live.partial', onPartial);
    on('live.text', onText);
    on('live.end', onEnd);
    on('ws.close', () => { // the server forgets nothing, but this page no longer hears: the calls' ends are not known
      Object.values(sessions).forEach((s) => { if (!s.ended) { s.ended = true; s.lost = true; s.speaking = {}; } });
      if (cur) draw();
    });
    document.addEventListener('keydown', (e) => { if (e.key === 'Escape' && open && panel.contains(document.activeElement)) show(false); });
    window.addEventListener('resize', place);
    place();
  }

  // place puts the panel beside the dialer, as tall as it and never under
  // the top bar: on its left, else on its right; in its place when the
  // dialer is closed. Small screens: a sheet over the page (CSS).
  function place() {
    if (!panel) return;
    const W = window.innerWidth, H = window.innerHeight;
    const sheet = W <= 760;
    panel.classList.toggle('lt-sheet', sheet);
    tab.classList.toggle('lt-sheet', sheet);
    if (sheet) {
      ['left', 'top', 'right', 'bottom', 'width', 'height'].forEach((k) => { panel.style[k] = ''; tab.style[k] = ''; });
      tab.classList.add('lt-tab-pill');
      return;
    }
    const ph = $('#phone');
    const dialer = ph && !ph.classList.contains('hidden') && !ph.classList.contains('ph-out');
    const b0 = window.Phone && Phone.box ? Phone.box() : { left: W - 364, top: H - 96 - Math.min(640, H - 176), right: W - 24, bottom: H - 96 };
    // Beyond the BLF / parked calls drawer when it is open beside the dialer.
    const ext = window.PhoneSide ? PhoneSide.extent() : { left: 0, right: 0 };
    const b = Object.assign({}, b0, { left: b0.left - ext.left, right: b0.right + ext.right });
    const area = window.Phone && Phone.bounds ? Phone.bounds() : { left: 8, top: 72 }; // below the top bar, right of the menu
    const minTop = area.top, minLeft = area.left;
    const width = 330, gap = 12;
    let left, side = 'left';
    if (!dialer) left = Math.min(Math.max(minLeft, b.right - width), W - width - 8);
    else if (b.left - gap - width >= minLeft) left = b.left - gap - width;
    else if (b.right + gap + width <= W - 8) { left = b.right + gap; side = 'right'; }
    else left = Math.max(minLeft, b.left - gap - width);
    const top = Math.max(minTop, b.top), bottom = Math.min(H - 8, b.bottom);
    Object.assign(panel.style, { left: left + 'px', top: top + 'px', width: width + 'px', height: Math.max(260, bottom - top) + 'px', right: 'auto', bottom: 'auto' });
    panel.dataset.side = side;
    if (dialer) { // a tab on the dialer's edge
      tab.classList.remove('lt-tab-pill');
      tab.dataset.side = side;
      Object.assign(tab.style, { left: (side === 'left' ? b.left - 34 : b.right) + 'px', top: (Math.max(minTop, b.top) + 72) + 'px', right: 'auto', bottom: 'auto' });
    } else { // a pill beside the round dialer button
      tab.classList.add('lt-tab-pill');
      const f = $('#phone-btn');
      const fr = f && f.getClientRects().length ? f.getBoundingClientRect() : null;
      Object.assign(tab.style, fr
        ? { left: 'auto', top: 'auto', right: (W - fr.left + 10) + 'px', bottom: (H - fr.bottom + (fr.height - 42) / 2) + 'px' }
        : { left: 'auto', top: 'auto', right: '24px', bottom: '30px' });
    }
  }

  function reset() {
    sessions = {};
    cur = null;
    closedFor = '';
    unread = 0;
    if (panel) { show(false); tab.classList.add('hidden'); }
  }

  function onStart(m) {
    let s = sessions[m.id];
    if (!s) {
      const other = m.me === 'a' ? 'b' : 'a';
      s = sessions[m.id] = { id: m.id, me: m.me, other, otherName: ((m.sides || {})[other] || {}).name || 'Caller', started: m.started || Date.now(),
        ended: false, lines: [], speaking: {}, partial: {}, nodes: {} };
    }
    s.ended = false;
    s.lost = false;
    // Keep only this call and those still going on.
    Object.keys(sessions).forEach((id) => { if (id !== m.id && sessions[id].ended) delete sessions[id]; });
    const changed = cur !== m.id;
    cur = m.id;
    if (changed) { unread = 0; stick = true; draw(); }
    if (closedFor !== m.id) show(true); else drawTab();
  }

  function onSpeak(m) {
    const s = sessions[m.id];
    if (!s || s.ended) return;
    if (m.on) s.speaking[m.side] = m.seq;
    else if (s.speaking[m.side] === m.seq) delete s.speaking[m.side];
    if (s.partial[m.side] && s.partial[m.side].seq !== s.speaking[m.side]) delete s.partial[m.side];
    if (m.id === cur) drawSpeaking(s);
  }

  // What someone said so far, while they go on speaking.
  function onPartial(m) {
    const s = sessions[m.id];
    if (!s || s.ended || !m.text || s.nodes[m.side + ':' + m.seq]) return;
    if (s.speaking[m.side] === undefined || s.speaking[m.side] < m.seq) s.speaking[m.side] = m.seq;
    if (s.speaking[m.side] !== m.seq) return;
    s.partial[m.side] = { seq: m.seq, text: String(m.text) };
    if (m.id === cur) drawSpeaking(s);
  }

  function onText(m) {
    const s = sessions[m.id];
    if (!s || !m.text) return;
    const key = m.side + ':' + m.seq;
    if (s.nodes[key]) return; // already here (a page opened during the call gets the start again)
    const line = { side: m.side, seq: m.seq, at: Number(m.at) || 0, text: String(m.text) };
    // In the order they were said: the two sides are recognised separately.
    let i = s.lines.length;
    while (i > 0 && s.lines[i - 1].at > line.at) i--;
    s.lines.splice(i, 0, line);
    if (s.speaking[m.side] !== undefined && s.speaking[m.side] <= m.seq) delete s.speaking[m.side];
    // The words already shown while they spoke stay; the rest is typed.
    const shown = s.partial[m.side] && s.partial[m.side].seq === m.seq ? commonPrefix(s.partial[m.side].text, line.text) : 0;
    if (s.partial[m.side] && s.partial[m.side].seq <= m.seq) delete s.partial[m.side];
    if (m.id !== cur) return;
    const node = lineNode(s, line);
    s.nodes[key] = node;
    const next = s.lines[i + 1] && s.nodes[s.lines[i + 1].side + ':' + s.lines[i + 1].seq];
    const empty = list.querySelector('.lt-empty');
    if (empty) empty.remove();
    list.insertBefore(node, next || list.querySelector('.lt-speaking'));
    typeOut(node.querySelector('.lt-text'), line.text, shown);
    drawSpeaking(s);
    if (!open) { unread++; drawTab(); }
    follow();
  }

  function onEnd(m) {
    const s = sessions[m.id];
    if (!s) return;
    s.ended = true;
    s.speaking = {};
    if (m.id === cur) draw();
  }

  function commonPrefix(a, b) {
    let n = 0;
    while (n < a.length && n < b.length && a[n] === b[n]) n++;
    return n;
  }

  function show(on) {
    if (on && !cur) return;
    if (on) place();
    open = on;
    if (on) { closedFor = ''; unread = 0; }
    else if (cur) closedFor = cur;
    panel.classList.toggle('lt-shut', !on);
    panel.setAttribute('aria-hidden', String(!on));
    panel.inert = !on;
    if (on) { stick = true; follow(); }
    drawTab();
    tickClock();
  }

  function drawTab() {
    const s = sessions[cur];
    tab.classList.toggle('hidden', open || !s);
    if (!s) return;
    fill(tab, icon('transcript', 16), h('span', { class: 'lt-tab-label' }, 'Transcript'),
      !s.ended ? h('i', { class: 'lt-tab-live', title: 'Live' }) : null,
      unread ? h('b', { class: 'lt-tab-badge' }, String(unread > 99 ? '99+' : unread)) : null);
  }

  function nameOf(s, side) { return side === s.me ? 'You' : s.otherName; }

  function lineNode(s, line) {
    const mine = line.side === s.me;
    return h('div', { class: 'lt-line ' + (mine ? 'me' : 'them') },
      h('div', { class: 'lt-who' }, h('b', null, nameOf(s, line.side)), h('span', null, clockText(line.at))),
      h('div', { class: 'lt-text' }));
  }

  // typeOut writes the text a few letters at a time, as if typed (from
  // letter "from": what was already shown).
  function typeOut(el, text, from) {
    if (reduced() || !open || from >= text.length) { el.textContent = text; return; }
    el.classList.add('typing');
    let n = from || 0;
    el.textContent = text.slice(0, n);
    const step = Math.max(1, Math.ceil((text.length - n) / 30)); // about half a second whatever the length
    const t = setInterval(() => {
      n = Math.min(text.length, n + step);
      el.textContent = text.slice(0, n);
      if (n >= text.length) { clearInterval(t); el.classList.remove('typing'); }
      follow();
    }, 16);
  }

  function drawSpeaking(s) {
    let box = list.querySelector('.lt-speaking');
    const sides = [s.other, s.me].filter((x) => s.speaking[x] !== undefined); // as the bubbles: them, then you
    if (!sides.length) { if (box) box.remove(); return; }
    if (!box) { box = h('div', { class: 'lt-speaking' }); list.append(box); }
    const empty = list.querySelector('.lt-empty');
    if (empty) empty.remove();
    fill(box, sides.map((side) => {
      const p = s.partial[side] && s.partial[side].seq === s.speaking[side] ? s.partial[side].text : '';
      return h('div', { class: 'lt-line ' + (side === s.me ? 'me' : 'them') + ' lt-dots', 'aria-label': nameOf(s, side) + ' is speaking' },
        h('div', { class: 'lt-who' }, h('b', null, nameOf(s, side))),
        p ? h('div', { class: 'lt-text lt-partial' }, p) : null,
        h('span', { class: 'lt-wave' }, h('i'), h('i'), h('i')));
    }));
    follow();
  }

  function follow() {
    if (stick && list) list.scrollTop = list.scrollHeight;
  }

  function transcriptText(s) {
    return s.lines.map((l) => '[' + clockText(l.at) + '] ' + t(nameOf(s, l.side)) + ': ' + l.text).join('\n');
  }

  function draw() {
    const s = sessions[cur];
    if (!s) return;
    s.nodes = {};
    list = h('div', { class: 'lt-list', role: 'log', 'aria-live': 'polite', 'aria-label': 'Conversation' });
    list.addEventListener('scroll', () => { stick = list.scrollHeight - list.scrollTop - list.clientHeight < 40; });
    s.lines.forEach((line) => {
      const node = lineNode(s, line);
      node.querySelector('.lt-text').textContent = line.text;
      s.nodes[line.side + ':' + line.seq] = node;
      list.append(node);
    });
    if (!s.lines.length) {
      list.append(h('div', { class: 'lt-empty' }, h('span', { class: 'lt-empty-ic' }, icon('transcript', 26)),
        h('b', null, s.ended ? 'Nothing was said' : 'Listening…'),
        h('span', null, s.ended ? 'No speech was heard on this call.' : 'What you and ' + s.otherName + ' say appears here as you speak.')));
    }
    const status = s.ended
      ? h('span', { class: 'lt-state ended' }, s.lost ? 'Disconnected' : 'Call ended')
      : h('span', { class: 'lt-state' }, h('i'), 'Live');
    const copy = () => {
      const text = transcriptText(s);
      if (!text) { toast('Nothing to copy yet'); return; }
      (navigator.clipboard ? navigator.clipboard.writeText(text) : Promise.reject(new Error('no clipboard'))).then(() => toast('Copied'), () => toast('Copy failed', true));
    };
    fill(panel,
      h('div', { class: 'lt-head' },
        h('div', { class: 'lt-title' },
          h('div', { class: 'lt-title-row' }, h('b', null, 'Live transcript'), status),
          h('div', { class: 'lt-sub' }, h('span', { class: 'ellipsis' }, 'with ' + s.otherName), h('span', { class: 'lt-clock' }))),
        h('button', { class: 'icon-btn', type: 'button', title: 'Copy the transcript', 'aria-label': 'Copy the transcript', onclick: copy }, icon('copy', 16)),
        h('button', { class: 'icon-btn', type: 'button', title: 'Close', 'aria-label': 'Close the live transcript', onclick: () => show(false) }, icon('x', 18))),
      h('div', { class: 'lt-legend' },
        h('span', null, h('i', { class: 'lt-sw them' }), s.otherName), h('span', null, h('i', { class: 'lt-sw me' }), 'You')),
      list,
      h('div', { class: 'lt-foot' }, icon('info', 13), s.ended ? 'Not saved: copy it to keep it.' : 'Written by the speech recogniser: words may be misheard.'));
    drawSpeaking(s);
    drawTab();
    tickClock();
    follow();
  }

  // The call's time in the header, while the panel is open.
  function tickClock() {
    const s = sessions[cur];
    const el = panel && panel.querySelector('.lt-clock');
    if (el && s) el.textContent = clockText(Date.now() - S.clockSkew * 1000 - s.started);
    const want = open && s && !s.ended;
    if (want && !clock) clock = setInterval(tickClock, 1000);
    if (!want && clock) { clearInterval(clock); clock = null; }
  }

  return { init, reset, show, place, sessions: () => sessions };
})();
window.LiveTr = LiveTr;
