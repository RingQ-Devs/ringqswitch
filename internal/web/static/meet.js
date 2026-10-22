'use strict';
// Meetings, RingQ style. In the portal the camera button in the top bar
// opens the "Meet" window: start a meeting, or join one with a link or
// code. Meeting links (/meet/m/<code>) open the meeting page, where guests
// give their name and ask to join. Inside: camera, microphone, effects,
// screen sharing, presenting a file, a whiteboard, live transcription,
// chat, reactions, raise hand, the waiting room, host controls and
// recording (in this browser).

const Meet = (() => {
  // ------------------------------------------------------------ helpers
  // This file also runs on the meeting page, without the portal's app.js.
  function h(tag, attrs, ...kids) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v === null || v === undefined || v === false) continue;
      if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
      else if (k === 'class') el.className = v;
      else if (k === 'value') el.value = v;
      else if (k === 'checked') el.checked = !!v;
      else if (k === 'style') el.style.cssText = v;
      else el.setAttribute(k, v === true ? '' : v);
    }
    for (const kid of kids.flat(Infinity)) {
      if (kid === null || kid === undefined || kid === false) continue;
      el.append(kid instanceof Node ? kid : String(kid));
    }
    return el;
  }
  function fill(el, ...kids) { el.replaceChildren(...kids.flat(Infinity).filter((k) => k !== null && k !== undefined && k !== false)); return el; }
  const ic = (name, size) => (typeof icon === 'function' ? icon(name, size || 18) : null);
  const pref = (k, d) => { try { const v = localStorage.getItem('rq-meet-' + k); return v === null ? d : v; } catch (e) { return d; } };
  const setPref = (k, v) => { try { localStorage.setItem('rq-meet-' + k, String(v)); } catch (e) { /* private mode */ } };
  // As in the portal: "Ext 1001" -> "01", "Ravi Tan" -> "RT".
  function initials(name) {
    const words = String(name || '').trim().split(/\s+/).filter(Boolean);
    if (!words.length) return '?';
    const last = words[words.length - 1];
    if (/^\+?\d+$/.test(last)) return last.slice(-2);
    if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
    return (words[0][0] + last[0]).toUpperCase();
  }
  const PALETTE = ['#1d4ed8', '#0f6e8c', '#7c3aed', '#be185d', '#b45309', '#15803d', '#4d7c0f', '#0369a1'];
  function colorOf(name) { let n = 0; for (const c of String(name || '')) n = (n * 31 + c.charCodeAt(0)) >>> 0; return PALETTE[n % PALETTE.length]; }
  const pad = (n) => String(n).padStart(2, '0');
  function clock(ms) {
    const s = Math.max(0, Math.floor(ms / 1000));
    return (s >= 3600 ? Math.floor(s / 3600) + ':' : '') + pad(Math.floor(s / 60) % 60) + ':' + pad(s % 60);
  }
  const timeOfDay = (t) => new Date(t).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' }).replace(' ', '').toLowerCase();
  // A code from a link or a typed code: 46ak-c6kg-hvcz (older ones: abc-defg-hij).
  function codeOf(text) {
    const t = String(text || '').trim().toLowerCase();
    const m = t.match(/[a-z0-9]{3,4}-[a-z0-9]{4}-[a-z0-9]{3,4}/);
    return m ? m[0] : t.replace(/^.*[#/]/, '');
  }
  let toastTimer;
  function say(msg, bad) {
    let t = document.getElementById('mt-toast');
    if (!t) { t = h('div', { id: 'mt-toast', class: 'mt-toast', role: 'status' }); document.body.append(t); }
    t.textContent = msg;
    t.classList.toggle('bad', !!bad);
    t.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => t.classList.remove('show'), 3500);
  }
  async function request(method, path, body, raw) {
    const opt = { method, credentials: 'same-origin', headers: {} };
    if (method !== 'GET') opt.headers['X-RQ'] = '1';
    if (raw) opt.body = raw;
    else if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
    const r = await fetch('/api/' + path, opt);
    const d = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(d.error || r.statusText);
    return d;
  }
  // The portal's state (app.js); a top-level const is not on window.
  const portal = () => (typeof S !== 'undefined' && S.me ? S : null);
  const portalUser = () => (portal() && S.me.kind === 'extension' ? S.me : null);

  // ------------------------------------------------------------ state
  let root = null; // where the screens are drawn
  let mode = ''; // 'window' (portal) or 'page'
  let view = ''; // prejoin, waiting, room, ended
  const st = {
    code: '', info: null, ws: null, me: null, host: false, title: '', link: '', token: '', started: 0, skew: 0,
    pc: null, ownMids: null, local: null, mic: null, camRaw: null, cam: null, screen: null,
    micOn: pref('mic-on', '1') === '1', camOn: pref('cam-on', '0') === '1', hand: false,
    peers: [], remote: {}, lobby: [], locked: false, lobbyOn: false,
    chat: [], unread: 0, side: '', speaking: {}, analysers: {},
    transcribing: false, transcript: [], captions: {}, present: null, board: false, strokes: [],
    rec: null, recStart: 0, popup: '', joinName: '', joinEmail: '', passcode: '',
  };
  const videos = new Map(); // stream id -> <video>, reused across redraws
  let audioCtx = null;
  const ctx = () => { try { audioCtx = audioCtx || new AudioContext(); } catch (e) { /* no Web Audio */ } return audioCtx; };
  document.addEventListener('pointerdown', () => { if (audioCtx && audioCtx.state === 'suspended') audioCtx.resume().catch(() => {}); }, true);

  // ------------------------------------------------------------ devices
  const EFFECTS = [
    { id: '', name: 'None' },
    { id: 'blur', name: 'Blur background', native: true },
    { id: 'soft', name: 'Soft focus', filter: 'blur(2.5px)' },
    { id: 'bright', name: 'Brighten', filter: 'brightness(1.35) contrast(1.06) saturate(1.1)' },
    { id: 'warm', name: 'Warm', filter: 'sepia(.22) saturate(1.35) brightness(1.04)' },
    { id: 'bw', name: 'Black and white', filter: 'grayscale(1) contrast(1.1)' },
  ];
  let effect = pref('effect', '');
  let fx = null; // the canvas pipeline of a filter effect

  async function openDevices() {
    const audio = { echoCancellation: true, noiseSuppression: true, autoGainControl: true };
    const video = { width: { ideal: 960 }, height: { ideal: 540 }, frameRate: { ideal: 24, max: 30 } };
    if (pref('mic', '')) audio.deviceId = { ideal: pref('mic', '') };
    if (pref('cam', '')) video.deviceId = { ideal: pref('cam', '') };
    let s = null;
    try { s = await navigator.mediaDevices.getUserMedia({ audio, video }); } catch (e) {
      try { s = await navigator.mediaDevices.getUserMedia({ audio }); } catch (e2) {
        try { s = await navigator.mediaDevices.getUserMedia({ video }); } catch (e3) { s = new MediaStream(); }
      }
    }
    st.local = s;
    st.mic = s.getAudioTracks()[0] || null;
    st.camRaw = s.getVideoTracks()[0] || null;
    if (!st.mic) st.micOn = false;
    if (!st.camRaw) st.camOn = false;
    await applyEffect();
    applyLocal();
    watchMic();
  }
  function stopDevices() {
    if (st.local) st.local.getTracks().forEach((t) => t.stop());
    if (fx) fx.stop();
    fx = null;
    if (st.screen) st.screen.stop();
    st.local = st.mic = st.camRaw = st.cam = st.screen = null;
    if (meter) { clearInterval(meter.timer); meter = null; }
  }
  async function changeDevice(kind, id) {
    setPref(kind, id);
    const want = kind === 'mic' ? { audio: { deviceId: { exact: id }, echoCancellation: true, noiseSuppression: true, autoGainControl: true } }
      : { video: { deviceId: { exact: id }, width: { ideal: 960 }, height: { ideal: 540 }, frameRate: { ideal: 24, max: 30 } } };
    try {
      const s = await navigator.mediaDevices.getUserMedia(want);
      const t = kind === 'mic' ? s.getAudioTracks()[0] : s.getVideoTracks()[0];
      const old = kind === 'mic' ? st.mic : st.camRaw;
      if (old) { st.local.removeTrack(old); old.stop(); }
      st.local.addTrack(t);
      if (kind === 'mic') { st.mic = t; watchMic(); await replaceOwn(0, t); } else { st.camRaw = t; await applyEffect(); }
      applyLocal();
      redraw();
    } catch (e) { say('Could not open that device: ' + e.message, true); }
  }
  function setSpeaker(id) {
    setPref('spk', id);
    document.querySelectorAll('audio.mt-remote').forEach((a) => { if (a.setSinkId) a.setSinkId(id).catch(() => {}); });
  }
  async function deviceList() {
    try { return await navigator.mediaDevices.enumerateDevices(); } catch (e) { return []; }
  }
  function deviceSelect(kind, list) {
    const map = { mic: 'audioinput', cam: 'videoinput', spk: 'audiooutput' };
    const cur = kind === 'mic' && st.mic ? st.mic.getSettings().deviceId : kind === 'cam' && st.camRaw ? st.camRaw.getSettings().deviceId : pref(kind, '');
    const sel = h('select', { class: 'mt-select', 'aria-label': { mic: 'Microphone', cam: 'Camera', spk: 'Speaker' }[kind] },
      kind === 'spk' ? h('option', { value: '' }, 'System default') : null,
      list.filter((d) => d.kind === map[kind] && d.deviceId !== 'communications' && (kind !== 'spk' || d.deviceId !== 'default'))
        .map((d, i) => h('option', { value: d.deviceId, selected: d.deviceId === cur }, d.label || ({ mic: 'Microphone ', cam: 'Camera ', spk: 'Speaker ' }[kind] + (i + 1)))));
    sel.addEventListener('change', () => (kind === 'spk' ? setSpeaker(sel.value) : changeDevice(kind, sel.value)));
    if (sel.options.length <= (kind === 'spk' ? 1 : 0)) sel.disabled = true;
    return sel;
  }

  // Effects: background blur where the camera or system offers it, else
  // a picture filter drawn through a canvas.
  async function applyEffect() {
    if (fx) { fx.stop(); fx = null; }
    const raw = st.camRaw;
    if (!raw) { st.cam = null; await replaceOwn(1, null); return; }
    const e = EFFECTS.find((x) => x.id === effect) || EFFECTS[0];
    const caps = raw.getCapabilities ? raw.getCapabilities() : {};
    if (caps.backgroundBlur) raw.applyConstraints({ backgroundBlur: !!e.native }).catch(() => {});
    if (e.native && !caps.backgroundBlur) say('This camera has no background blur; try a browser and computer that offer it.', true);
    if (!e.filter) { st.cam = raw; await replaceOwn(1, raw); return; }
    const v = h('video', { autoplay: true, playsinline: true, muted: true });
    v.srcObject = new MediaStream([raw]);
    v.play().catch(() => {});
    const set = raw.getSettings();
    const c = h('canvas', { width: set.width || 960, height: set.height || 540 });
    const g = c.getContext('2d');
    let on = true;
    const draw = () => {
      if (!on) return;
      if (v.videoWidth) {
        if (c.width !== v.videoWidth) { c.width = v.videoWidth; c.height = v.videoHeight; }
        g.filter = e.filter;
        g.drawImage(v, 0, 0, c.width, c.height);
      }
    };
    const timer = setInterval(draw, 1000 / 24);
    const out = c.captureStream(24).getVideoTracks()[0];
    fx = { stop: () => { on = false; clearInterval(timer); out.stop(); v.srcObject = null; } };
    out.enabled = st.camOn;
    st.cam = out;
    await replaceOwn(1, out);
  }

  // The microphone level, for the meter and the "not picking anything up" note.
  let meter = null;
  function watchMic() {
    if (meter) { clearInterval(meter.timer); meter = null; }
    const a = ctx();
    if (!a || !st.mic) return;
    const src = a.createMediaStreamSource(new MediaStream([st.mic]));
    const an = a.createAnalyser();
    an.fftSize = 512;
    src.connect(an);
    const buf = new Uint8Array(an.fftSize);
    meter = { level: 0, quietSince: Date.now(), warn: false, timer: null };
    meter.timer = setInterval(() => {
      if (!st.mic || a.state !== 'running') return;
      an.getByteTimeDomainData(buf);
      let sum = 0;
      for (const x of buf) sum += (x - 128) * (x - 128);
      const level = Math.min(100, Math.sqrt(sum / buf.length) * 6);
      meter.level = level;
      if (!st.micOn || level > 3) meter.quietSince = Date.now();
      const warn = st.micOn && Date.now() - meter.quietSince > 5000;
      document.querySelectorAll('.mt-level > i').forEach((i) => { i.style.width = (st.micOn ? level : 0).toFixed(0) + '%'; });
      if (warn !== meter.warn) { meter.warn = warn; document.querySelectorAll('.mt-micwarn').forEach((x) => x.classList.toggle('hidden', !warn)); }
    }, 150);
  }

  function applyLocal() {
    if (st.mic) st.mic.enabled = st.micOn;
    if (st.cam) st.cam.enabled = st.camOn;
    if (st.camRaw) st.camRaw.enabled = st.camOn;
    setPref('mic-on', st.micOn ? '1' : '0');
    setPref('cam-on', st.camOn ? '1' : '0');
    sendState();
    if (recog && !st.micOn) recog.stop();
    if (st.transcribing && st.micOn) startRecognition();
  }
  function toggleMic() { if (!st.mic) { say('No microphone found', true); return; } st.micOn = !st.micOn; applyLocal(); redraw(); }
  function toggleCam() { if (!st.camRaw) { say('No camera found', true); return; } st.camOn = !st.camOn; applyLocal(); redraw(); }

  // ------------------------------------------------------------ the portal window
  let shell = null;
  function openWindow(code) {
    const me = portalUser();
    if (shell) {
      shell.overlay.classList.remove('hidden');
      shell.mini.classList.add('hidden');
      if (code && view === 'prejoin') { joinTab = 'join'; joinCode = code; redraw(); }
      return;
    }
    const overlay = h('div', { class: 'mt-overlay' });
    const win = h('div', { class: 'mt-win', role: 'dialog', 'aria-label': 'Meet' });
    const body = h('div', { class: 'mt-body' });
    const maxBtn = h('button', { class: 'mt-wbtn', type: 'button', title: 'Maximise', 'aria-label': 'Maximise', onclick: () => {
      win.classList.toggle('max');
      fill(maxBtn, ic(win.classList.contains('max') ? 'squares' : 'square', 16));
    } }, ic('square', 16));
    win.append(h('div', { class: 'mt-whead' },
      h('span', { class: 'mt-wicon' }, ic('video', 18)),
      h('div', { class: 'mt-wtitle' }, h('b', null, 'Meet'), h('span', null, 'Start a video meeting or join one with a link.')),
      h('span', { class: 'mt-grow' }),
      h('button', { class: 'mt-wbtn', type: 'button', title: 'Minimise', 'aria-label': 'Minimise', onclick: minimise }, ic('minus', 16)),
      maxBtn,
      h('button', { class: 'mt-wbtn', type: 'button', title: 'Close', 'aria-label': 'Close', onclick: closeWindow }, ic('x', 16))), body);
    overlay.append(win);
    const mini = h('div', { class: 'mt-mini hidden' });
    document.body.append(overlay, mini);
    shell = { overlay, win, body, mini };
    root = body;
    mode = 'window';
    if (!me) { fill(body, h('div', { class: 'mt-center' }, h('h2', null, 'Meetings need an extension'), h('p', { class: 'mt-muted' }, 'Sign in with an extension and its web password to start or join meetings.'))); return; }
    joinTab = code ? 'join' : 'start';
    joinCode = code || '';
    prejoin();
  }
  function minimise() {
    if (!shell) return;
    if (view !== 'room' && view !== 'waiting') { closeWindow(); return; }
    shell.overlay.classList.add('hidden');
    shell.mini.classList.remove('hidden');
    drawMini();
  }
  function drawMini() {
    if (!shell || shell.mini.classList.contains('hidden')) return;
    fill(shell.mini,
      h('span', { class: 'mt-dot' + (view === 'room' ? ' live' : '') }),
      h('div', { class: 'mt-mini-txt' }, h('b', null, st.title || 'Meeting'), h('span', { 'data-clock': '1' }, view === 'room' ? clock(Date.now() - st.started) : 'Waiting to be let in')),
      view === 'room' ? h('button', { class: 'mt-round sm' + (st.micOn ? '' : ' off'), type: 'button', title: st.micOn ? 'Mute' : 'Unmute', onclick: () => { toggleMic(); drawMini(); } }, ic(st.micOn ? 'mic' : 'micoff', 16)) : null,
      h('button', { class: 'mt-round sm', type: 'button', title: 'Open the meeting', onclick: () => openWindow() }, ic('expand', 16)),
      h('button', { class: 'mt-round sm red', type: 'button', title: 'Leave', onclick: () => leave() }, ic('hangup', 16)));
  }
  function closeWindow() {
    if (view === 'room' && !confirm('Leave the meeting?')) return;
    if (view === 'room' || view === 'waiting') leave(true);
    teardown();
    if (shell) { shell.overlay.remove(); shell.mini.remove(); shell = null; }
    root = null;
    view = '';
  }
  // Signing out of the portal ends the meeting too.
  window.addEventListener('rq-logout', () => { if (shell) { if (view === 'room' || view === 'waiting') leave(true); teardown(); shell.overlay.remove(); shell.mini.remove(); shell = null; view = ''; } });

  // ------------------------------------------------------------ the meeting page
  async function page() {
    try { const t = localStorage.getItem('rq-theme'); if (t) document.documentElement.setAttribute('data-theme', t); } catch (e) { /* private mode */ }
    mode = 'page';
    root = document.getElementById('screen');
    const m = location.pathname.match(/\/meet\/m\/([^/?#]+)/);
    st.code = codeOf(m ? decodeURIComponent(m[1]) : decodeURIComponent(location.hash.slice(1)));
    if (st.code) {
      try { st.info = await request('GET', 'guest/meeting/' + encodeURIComponent(st.code)); } catch (e) { st.info = null; st.infoError = e.message; }
    }
    if (st.info) document.title = st.info.title + ' · Meeting';
    prejoin();
  }

  // ------------------------------------------------------------ pre-join
  let joinTab = 'start';
  let joinCode = '';
  let devicesOpen = false;
  let controlsOpen = false;
  let recent = null; // recent meeting content, once loaded
  let recentOpen = null;
  let planned = null;
  const opts = { lobby: true, guests: true, passcode: '' };

  async function prejoin() {
    view = 'prejoin';
    redraw();
    if (!st.local) { await openDevices(); redraw(); }
  }

  function previewCard(name) {
    const show = st.camOn && st.cam;
    const card = h('div', { class: 'mt-preview' + (show ? ' video' : '') });
    if (show) {
      const v = videoFor('self-preview', videoSelfStream());
      v.classList.add('mirror');
      card.append(v);
    } else {
      card.append(h('div', { class: 'mt-pv-who' }, h('span', { class: 'mt-av big', style: 'background:' + (mode === 'page' && !(st.info && st.info.signed_in) ? '#15803d' : '#1d4ed8') }, initials(name)), h('b', null, name)));
    }
    card.append(h('span', { class: 'mt-chip' }, ic(st.micOn ? 'mic' : 'micoff', 14), name));
    return card;
  }
  function roundToggle(label, iconName, on, fn, off) {
    return h('div', { class: 'mt-rt' }, h('button', { class: 'mt-round' + (off ? ' red' : ''), type: 'button', 'aria-pressed': String(!!on), title: label, onclick: fn }, ic(iconName, 18)), h('span', null, label));
  }
  function effectsPicker() {
    return h('div', { class: 'mt-pop mt-effects' }, h('b', null, 'Effects'),
      EFFECTS.map((e) => h('button', { class: 'mt-effect' + (effect === e.id ? ' on' : ''), type: 'button', onclick: async () => {
        effect = e.id; setPref('effect', e.id); st.popup = ''; await applyEffect(); applyLocal(); redraw();
      } }, h('span', { class: 'mt-fx mt-fx-' + (e.id || 'none') }), e.name)),
      h('p', { class: 'mt-muted small' }, 'Blur background uses your camera or system (Windows Studio Effects, macOS); the others work everywhere.'));
  }
  function devicesCard() {
    const label = st.mic ? (st.mic.label || 'Microphone') : 'No microphone';
    const box = h('div', { class: 'mt-card mt-devices' + (devicesOpen ? ' open' : '') },
      h('button', { class: 'mt-collapse', type: 'button', 'aria-expanded': String(devicesOpen), onclick: () => { devicesOpen = !devicesOpen; redraw(); } },
        h('div', null, h('b', null, 'Devices and audio'), h('span', { class: 'mt-muted' }, 'Microphone (' + label.replace(/^Default - /, '') + ')')), ic(devicesOpen ? 'up' : 'down', 18)));
    if (devicesOpen) {
      const rows = h('div', { class: 'mt-dev-rows' }, h('div', { class: 'mt-muted small' }, 'Loading…'));
      deviceList().then((list) => {
        const hasSink = 'setSinkId' in HTMLMediaElement.prototype;
        fill(rows,
          h('label', null, 'Microphone', deviceSelect('mic', list)),
          h('div', { class: 'mt-level', title: 'Your voice level' }, h('i')),
          h('label', null, 'Camera', deviceSelect('cam', list)),
          hasSink ? h('label', null, 'Speaker', deviceSelect('spk', list)) : null);
      });
      box.append(rows);
    }
    return box;
  }
  function micWarning() {
    return h('div', { class: 'mt-micwarn' + (meter && meter.warn ? '' : ' hidden') }, ic('activity', 18),
      h('span', null, 'Your microphone is on but is not picking anything up. Say something to check the level, or choose a different microphone below.'));
  }
  function leftColumn(name) {
    return h('div', { class: 'mt-left' },
      previewCard(name),
      h('div', { class: 'mt-rts' },
        roundToggle('Mic', st.micOn ? 'mic' : 'micoff', st.micOn, toggleMic, !st.micOn),
        roundToggle('Camera', st.camOn ? 'video' : 'videooff', st.camOn, toggleCam, !st.camOn),
        h('div', { class: 'mt-rt mt-anchor' }, h('button', { class: 'mt-round' + (effect ? ' on' : ''), type: 'button', title: 'Effects', onclick: () => { st.popup = st.popup === 'effects' ? '' : 'effects'; redraw(); } }, ic('sparkles', 18)), h('span', null, 'Effects'),
          st.popup === 'effects' ? effectsPicker() : null)),
      micWarning(),
      devicesCard());
  }

  function memberPrejoin() {
    const me = portalUser();
    const name = (me.name && me.name !== me.extension ? me.name : 'Ext ' + me.extension);
    const seg = h('div', { class: 'mt-seg' }, ['start', 'join'].map((k) => h('button', { class: joinTab === k ? 'on' : '', type: 'button', onclick: () => { joinTab = k; redraw(); } }, k === 'start' ? 'Start' : 'Join')));
    let panel;
    if (joinTab === 'start') {
      const title = h('input', { class: 'mt-input', placeholder: 'Untitled meeting', maxlength: '120', value: st.titleDraft || '' });
      title.addEventListener('input', () => { st.titleDraft = title.value; });
      const sub = (opts.lobby ? 'Everyone waits until you let them in. ' : 'Everyone with the link joins straight away. ') + (opts.guests ? '' : 'Only people with an extension can join. ') + 'Everyone can use their camera and microphone, and names are visible.';
      panel = [
        h('h2', null, 'Start a meeting'),
        h('div', { class: 'mt-host' }, h('span', { class: 'mt-av', style: 'background:#1d4ed8' }, initials(name)), h('div', null, h('b', null, name), h('span', { class: 'mt-muted' }, 'Joining as host'))),
        h('label', { class: 'mt-label' }, 'Meeting title (optional)', title),
        h('button', { class: 'mt-btn primary wide', type: 'button', onclick: async (e) => {
          e.currentTarget.disabled = true;
          try {
            const r = await request('POST', 'meetings', { title: title.value.trim() || 'Untitled meeting', instant: true, lobby: opts.lobby, allow_guests: opts.guests, passcode: opts.passcode });
            st.code = r.code;
            join();
          } catch (err) { say(err.message, true); redraw(); }
        } }, ic('video', 18), 'Start meeting'),
        h('div', { class: 'mt-sep' }),
        h('button', { class: 'mt-collapse', type: 'button', 'aria-expanded': String(controlsOpen), onclick: () => { controlsOpen = !controlsOpen; redraw(); } },
          h('div', null, h('b', null, 'Meeting controls'), h('span', { class: 'mt-muted' }, sub)), ic(controlsOpen ? 'up' : 'down', 18)),
        controlsOpen ? h('div', { class: 'mt-opts' },
          switchRow('Waiting room', 'People ask to join and you let each one in', opts.lobby, (v) => { opts.lobby = v; redraw(); }),
          switchRow('Allow guests', 'People without an extension can join with the link', opts.guests, (v) => { opts.guests = v; redraw(); }),
          (() => { const i = h('input', { class: 'mt-input', inputmode: 'numeric', maxlength: '12', placeholder: 'No passcode', value: opts.passcode }); i.addEventListener('input', () => { opts.passcode = i.value.replace(/\D/g, ''); }); return h('label', { class: 'mt-label' }, 'Passcode for guests (optional)', i); })()) : null,
      ];
    } else {
      const input = h('input', { class: 'mt-input', placeholder: 'e.g. 46ak-c6kg-hvcz or a meeting link', value: joinCode });
      input.addEventListener('input', () => { joinCode = input.value; });
      const go = () => { const c = codeOf(input.value); if (!c) { say('Enter a meeting link or code', true); return; } st.code = c; join(); };
      input.addEventListener('keydown', (e) => { if (e.key === 'Enter') go(); });
      if (planned === null) { planned = []; request('GET', 'meetings').then((r) => { planned = r.slice(0, 6); redraw(); }).catch(() => {}); }
      panel = [
        h('h2', null, 'Join a meeting'),
        h('label', { class: 'mt-label' }, 'Meeting link or code', input),
        h('button', { class: 'mt-btn primary wide', type: 'button', onclick: go }, ic('enter', 18), 'Join'),
        planned.length ? [h('div', { class: 'mt-sep' }), h('b', { class: 'mt-small-title' }, 'Planned meetings'),
          planned.map((p) => h('button', { class: 'mt-planned', type: 'button', onclick: () => { st.code = p.code; join(); } },
            h('span', { class: 'mt-av sm', style: 'background:' + colorOf(p.title) }, initials(p.title)),
            h('div', null, h('b', null, p.title), h('span', { class: 'mt-muted' }, (p.scheduled_at ? new Date(p.scheduled_at).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) + ' · ' : '') + p.code)),
            p.participants ? h('span', { class: 'mt-pill green' }, p.participants + ' in') : null))] : null,
      ];
    }
    return h('div', { class: 'mt-split' },
      leftColumn(name),
      h('div', { class: 'mt-right' },
        h('div', { class: 'mt-card mt-pad' }, seg, panel),
        recentCard()));
  }

  function switchRow(label, help, on, fn) {
    const i = h('input', { type: 'checkbox', checked: on });
    i.addEventListener('change', () => fn(i.checked));
    return h('label', { class: 'mt-switch-row' }, h('div', null, h('b', null, label), h('span', { class: 'mt-muted' }, help)), h('span', { class: 'mt-switch' }, i, h('span')));
  }

  function recentCard() {
    const box = h('div', { class: 'mt-card mt-pad mt-recent' }, h('b', { class: 'mt-card-title' }, 'Recent meeting content'));
    if (recent === null) {
      box.append(h('button', { class: 'mt-link', type: 'button', onclick: async () => {
        recent = 'loading'; redraw();
        try { recent = await request('GET', 'meetings/history'); } catch (e) { recent = null; say(e.message, true); }
        redraw();
      } }, ic('history', 16), 'Load recent meeting content'));
      return box;
    }
    if (recent === 'loading') { box.append(h('div', { class: 'mt-muted' }, 'Loading…')); return box; }
    if (!recent.length) { box.append(h('div', { class: 'mt-muted' }, 'Nothing yet. After a meeting, who came, its chat and its transcript are kept here.')); return box; }
    box.append(h('div', { class: 'mt-recent-list' }, recent.map((r) => {
      const open = recentOpen && recentOpen.uuid === r.uuid;
      const people = (r.participants || []).map((p) => p.name);
      return h('div', { class: 'mt-recent-item' + (open ? ' open' : '') },
        h('button', { class: 'mt-recent-head', type: 'button', onclick: async () => {
          if (open) { recentOpen = null; redraw(); return; }
          try { recentOpen = await request('GET', 'meetings/history/' + r.uuid); } catch (e) { say(e.message, true); }
          redraw();
        } },
        h('div', null, h('b', null, r.title || r.code),
          h('span', { class: 'mt-muted' }, new Date(r.started).toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' }) + ' · ' + clock(r.ended - r.started) + ' · ' + people.length + (people.length === 1 ? ' person' : ' people'))),
        h('span', { class: 'mt-muted small' }, [r.transcript_count ? r.transcript_count + (r.transcript_count === 1 ? ' transcript line' : ' transcript lines') : '',
          r.chat_count ? r.chat_count + (r.chat_count === 1 ? ' message' : ' messages') : ''].filter(Boolean).join(' · ')), ic(open ? 'up' : 'down', 16)),
        open ? historyDetail(recentOpen) : null);
    })));
    return box;
  }
  function historyDetail(d) {
    const lines = (list) => list.map((m) => '[' + new Date(m.time).toLocaleTimeString() + '] ' + m.name + ': ' + m.text).join('\n');
    const dl = (name, text) => { const a = h('a', { href: URL.createObjectURL(new Blob([text], { type: 'text/plain' })), download: name }); document.body.append(a); a.click(); a.remove(); };
    const base = (d.title || d.code).replace(/[^\w.-]+/g, '-');
    return h('div', { class: 'mt-recent-body' },
      h('div', null, h('b', null, 'Who came: '), (d.participants || []).map((p) => p.name + (p.guest ? ' (guest' + (p.email ? ', ' + p.email : '') + ')' : '')).join(', ')),
      d.files && d.files.length ? h('div', null, h('b', null, 'Presented: '), d.files.join(', ')) : null,
      h('div', { class: 'mt-recent-btns' },
        d.transcript.length ? h('button', { class: 'mt-btn sm', type: 'button', onclick: () => dl(base + '-transcript.txt', lines(d.transcript)) }, ic('download', 14), 'Transcript') : null,
        d.chat.length ? h('button', { class: 'mt-btn sm', type: 'button', onclick: () => dl(base + '-chat.txt', lines(d.chat)) }, ic('download', 14), 'Chat') : null),
      d.transcript.length ? h('div', { class: 'mt-recent-text' }, d.transcript.slice(0, 12).map((m) => h('div', null, h('b', null, m.name + ': '), m.text)), d.transcript.length > 12 ? h('div', { class: 'mt-muted' }, '…') : null) : null);
  }

  function guestPrejoin() {
    const info = st.info;
    const signed = info && info.signed_in;
    const name = signed ? info.name : (st.joinName || pref('name', '') || 'Guest');
    const right = h('div', { class: 'mt-card mt-pad mt-ready' });
    if (view === 'waiting') {
      fill(right,
        h('h2', null, 'Ready to join?'),
        h('div', { class: 'mt-waiting' }, h('span', { class: 'mt-spin' }), h('b', null, st.waitMsg || 'Waiting for the organizer to let you in')),
        h('p', { class: 'mt-muted' }, 'You\'ll join automatically as soon as you\'re admitted.'),
        h('div', { class: 'mt-host' }, h('span', { class: 'mt-av', style: 'background:' + (signed ? '#1d4ed8' : '#3f6212') }, initials(name)), h('div', null, h('b', null, name), h('span', { class: 'mt-muted' }, 'Your request has been sent'))),
        h('button', { class: 'mt-btn wide', type: 'button', onclick: () => { leave(true); view = 'prejoin'; redraw(); } }, ic('x', 16), 'Cancel and leave'),
        h('p', { class: 'mt-powered' }, 'Powered by ', h('b', null, brand())));
      return h('div', { class: 'mt-split page' }, leftColumn(name), h('div', { class: 'mt-right' }, right));
    }
    const nameIn = h('input', { class: 'mt-input', placeholder: 'Enter your name', maxlength: '60', value: st.joinName || pref('name', ''), autocomplete: 'name' });
    const emailIn = h('input', { class: 'mt-input', type: 'email', placeholder: 'you@example.com', maxlength: '120', value: st.joinEmail || pref('email', ''), autocomplete: 'email' });
    const codeIn = h('input', { class: 'mt-input', placeholder: 'e.g. 46ak-c6kg-hvcz', value: st.code });
    const passIn = info && info.passcode ? h('input', { class: 'mt-input', inputmode: 'numeric', placeholder: 'Passcode', value: st.passcode }) : null;
    const err = h('div', { class: 'mt-error' }, st.joinError || (st.code && !info ? (st.infoError || 'Meeting not found') : ''));
    const btn = h('button', { class: 'mt-btn primary wide', type: 'button' }, ic('enter', 18), info && info.lobby && !info.organizer ? 'Ask to join' : 'Join now');
    const sync = () => {
      st.joinName = nameIn.value; st.joinEmail = emailIn.value;
      btn.disabled = !signed && !nameIn.value.trim();
      const pv = root.querySelector('.mt-pv-who b'); if (pv) pv.textContent = nameIn.value.trim() || 'Guest';
      const ch = root.querySelector('.mt-preview .mt-chip'); if (ch) fill(ch, ic(st.micOn ? 'mic' : 'micoff', 14), nameIn.value.trim() || 'Guest');
      const av = root.querySelector('.mt-pv-who .mt-av'); if (av) av.textContent = initials(nameIn.value.trim() || 'Guest');
    };
    nameIn.addEventListener('input', sync);
    emailIn.addEventListener('input', sync);
    btn.addEventListener('click', async () => {
      const c = codeOf(codeIn.value);
      if (c !== st.code) { // another meeting: look it up first
        location.href = '/meet/m/' + encodeURIComponent(c);
        return;
      }
      if (!info) return;
      if (!signed) { setPref('name', nameIn.value.trim()); setPref('email', emailIn.value.trim()); }
      st.joinName = nameIn.value.trim(); st.joinEmail = emailIn.value.trim(); st.passcode = passIn ? passIn.value.trim() : '';
      st.joinError = '';
      join();
    });
    codeIn.addEventListener('keydown', (e) => { if (e.key === 'Enter') btn.click(); });
    sync();
    fill(right,
      h('h2', null, 'Ready to join?'),
      h('div', { class: 'mt-muted' }, timeOfDay(Date.now())),
      st.code ? h('div', { class: 'mt-muted small' }, 'Meeting code ' + st.code) : null,
      info ? h('div', { class: 'mt-meet-title' }, info.title, info.host ? h('span', { class: 'mt-muted' }, ' · hosted by ' + info.host) : null) : null,
      signed ? h('p', null, 'Joining as ', h('b', null, info.name)) : [h('label', { class: 'mt-label' }, 'Your name', nameIn), h('label', { class: 'mt-label' }, 'Your email', emailIn)],
      h('label', { class: 'mt-label' }, 'Meeting link or code', codeIn),
      passIn ? h('label', { class: 'mt-label' }, 'Passcode', passIn) : null,
      info && info.lobby && !info.organizer ? h('div', { class: 'mt-note' }, ic('lock', 16), 'You\'ll enter the meeting when the organizer admits you.') : null,
      info && !info.guests && !signed ? h('div', { class: 'mt-note' }, ic('lock', 16), 'This meeting is for people with an extension. Sign in to the portal first.') : null,
      err, btn,
      h('p', { class: 'mt-powered' }, 'Powered by ', h('b', null, brand())));
    setTimeout(() => { if (!signed && !nameIn.value) nameIn.focus(); }, 0);
    return h('div', { class: 'mt-split page' }, leftColumn(signed ? name : (nameIn.value.trim() || 'Guest')), h('div', { class: 'mt-right' }, right));
  }
  const brand = () => (st.info && st.info.brand) || (portal() && S.me.brand) || 'RingQswitch';

  // ------------------------------------------------------------ signalling
  function send(m) { if (st.ws && st.ws.readyState === 1) st.ws.send(JSON.stringify(m)); }
  function sendState() { send({ t: 'meet.state', data: { audio: st.micOn, video: st.camOn, screen: !!st.screen, hand: st.hand } }); }

  function join() {
    const signed = mode === 'window' || (st.info && st.info.signed_in);
    const proto = location.protocol === 'https:' ? 'wss://' : 'ws://';
    const url = signed ? proto + location.host + '/api/ws'
      : proto + location.host + '/api/guest/ws?code=' + encodeURIComponent(st.code) + '&name=' + encodeURIComponent(st.joinName) +
        '&email=' + encodeURIComponent(st.joinEmail || '') + '&passcode=' + encodeURIComponent(st.passcode || '');
    if (st.ws) { st.ws.onclose = null; st.ws.close(); }
    const ws = new WebSocket(url);
    st.ws = ws;
    let opened = false;
    ws.onopen = () => { opened = true; send({ t: 'meet.join', room: st.code }); };
    ws.onclose = () => {
      if (st.ws !== ws) return;
      if (!opened) {
        // The server refused before the upgrade: ask why.
        fetch(url.replace(/^ws/, 'http'), { credentials: 'same-origin' }).then((r) => r.json()).then((d) => { joinFailed(d.error || 'Could not join'); }).catch(() => joinFailed('Could not join'));
        return;
      }
      if (view === 'room' || view === 'waiting') ended('The connection to the server was lost.');
    };
    ws.onmessage = (ev) => { let m; try { m = JSON.parse(ev.data); } catch (e) { return; } handle(m); };
  }
  function joinFailed(msg) {
    st.joinError = msg;
    if (mode === 'window') say(msg, true);
    view = 'prejoin';
    redraw();
  }

  async function handle(m) {
    switch (m.t) {
      case 'error':
        if (view === 'prejoin' || view === 'waiting') joinFailed(m.error); else say(m.error, true);
        break;
      case 'meet.joined':
        st.me = m.me; st.host = m.host; st.title = m.title; st.link = m.link; st.token = m.token;
        st.skew = m.now ? Date.now() - m.now : 0;
        st.started = m.started + st.skew;
        if (m.waiting) { view = 'waiting'; redraw(); }
        break;
      case 'meet.waiting': st.waitMsg = m.message.replace(/…$/, ''); view = 'waiting'; redraw(); break;
      case 'meet.admitted':
        st.chat = m.chat || []; st.transcript = m.transcript || []; st.transcribing = !!m.transcribing;
        st.present = m.present || null; st.board = !!m.board; st.strokes = (m.strokes || []).slice();
        if (m.started) st.started = m.started + st.skew;
        view = 'room';
        st.side = '';
        redraw();
        sendState();
        if (st.transcribing) startRecognition();
        break;
      case 'meet.offer': await onOffer(m.sdp); break;
      case 'meet.ice': if (st.pc) st.pc.addIceCandidate(m.candidate).catch(() => {}); break;
      case 'meet.peers': {
        const before = new Set(st.peers.map((p) => p.id));
        st.peers = m.peers; st.locked = m.locked; st.lobbyOn = !!m.lobby;
        for (const id of Object.keys(st.remote)) if (!m.peers.some((p) => p.id === id)) { delete st.remote[id]; stopAnalyser(id); }
        if (view === 'room' && before.size) for (const p of m.peers) if (!before.has(p.id) && p.id !== st.me) say(p.name + ' joined');
        redraw();
        break;
      }
      case 'meet.chat':
        st.chat.push(m.message);
        if (st.side !== 'chat' && m.message.from !== st.me) { st.unread++; say(m.message.name + ': ' + m.message.text.slice(0, 80)); }
        redraw();
        break;
      case 'meet.lobby': {
        const known = new Set(st.lobby.map((w) => w.id));
        if (m.waiting.length > st.lobby.length) chime();
        st.lobby = m.waiting;
        // Somebody asks to join: the organizer also gets a notification at
        // the top right when the meeting is minimised or the tab is hidden.
        const away = document.hidden || (shell && shell.overlay.classList.contains('hidden'));
        if (typeof Notes !== 'undefined') {
          m.waiting.filter((w) => !known.has(w.id)).forEach((w) => {
            if (!away) return;
            Notes.show({ key: 'knock-' + w.id, icon: 'video', tone: 'violet', title: w.name + ' wants to join', body: st.title || 'Your meeting', sticky: true,
              actions: [{ label: 'Decline', fn: () => send({ t: 'meet.deny', peer: w.id }) },
                { label: 'Admit', kind: 'primary', fn: () => send({ t: 'meet.admit', peer: w.id }) }] });
          });
          // Let in or gone: its notification closes.
          const now = new Set(m.waiting.map((w) => w.id));
          known.forEach((id) => { if (!now.has(id)) Notes.close('knock-' + id); });
        }
        redraw();
        break;
      }
      case 'meet.mute': st.micOn = false; applyLocal(); say('You were muted by ' + m.by); redraw(); break;
      case 'meet.reaction': showReaction(m.peer, m.emoji); break;
      case 'meet.media': if (m.state === 'failed') say('The meeting\'s audio and video could not connect: is UDP to the server blocked?', true); break;
      case 'meet.transcribe':
        st.transcribing = m.on;
        say(m.on ? m.by + ' turned on transcription' : 'Transcription stopped');
        if (m.on) startRecognition(); else if (recog) recog.stop();
        redraw();
        break;
      case 'meet.caption':
        st.captions[m.line.from] = { name: m.line.name, text: m.line.text, at: Date.now() };
        if (m.final) st.transcript.push(m.line);
        drawCaptions();
        if (st.side === 'transcript' && m.final) redraw();
        break;
      case 'meet.present':
        st.present = m.present;
        redraw();
        break;
      case 'meet.board':
        st.board = m.on;
        if (m.on) say(m.by + ' opened the whiteboard');
        redraw();
        break;
      case 'meet.draw': // our own strokes are drawn and kept as they are made
        if (m.peer !== st.me) { st.strokes.push(m.stroke); drawStroke(m.stroke); }
        break;
      case 'meet.clear': st.strokes = []; redrawBoard(); break;
      case 'meet.ended': ended(m.reason); break;
    }
  }
  function chime() {
    const a = ctx();
    if (!a) return;
    const g = a.createGain(); g.gain.value = 0.06; g.connect(a.destination);
    [660, 880].forEach((f, i) => { const o = a.createOscillator(); o.frequency.value = f; o.connect(g); o.start(a.currentTime + i * 0.18); o.stop(a.currentTime + i * 0.18 + 0.16); });
  }

  async function onOffer(sdp) {
    if (!st.pc) {
      st.pc = new RTCPeerConnection({ bundlePolicy: 'max-bundle' });
      st.pc.onicecandidate = (e) => { if (e.candidate) send({ t: 'meet.ice', candidate: e.candidate.toJSON() }); };
      st.pc.ontrack = (e) => {
        const sid = e.streams[0] ? e.streams[0].id : '';
        const [peer, kind] = sid.split('|');
        if (!peer || !kind) return;
        const r = st.remote[peer] = st.remote[peer] || {};
        r[kind] = e.streams[0];
        if (kind === 'audio') startAnalyser(peer, e.streams[0]);
        e.track.onunmute = () => redraw();
        redraw();
      };
    }
    await st.pc.setRemoteDescription(sdp);
    const trs = st.pc.getTransceivers();
    if (!st.ownMids) st.ownMids = trs.slice(0, 3).map((t) => t.mid);
    const own = st.ownMids.map((mid) => trs.find((t) => t.mid === mid));
    const tracks = [st.mic, st.cam, st.screen];
    own.forEach((t, i) => {
      if (!t) return;
      t.direction = 'sendonly';
      if (t.sender.track !== tracks[i]) t.sender.replaceTrack(tracks[i] || null).catch(() => {});
    });
    const answer = await st.pc.createAnswer();
    await st.pc.setLocalDescription(answer);
    send({ t: 'meet.answer', sdp: st.pc.localDescription });
  }
  async function replaceOwn(i, track) {
    if (!st.pc || !st.ownMids) return;
    const t = st.pc.getTransceivers().find((x) => x.mid === st.ownMids[i]);
    if (t) await t.sender.replaceTrack(track).catch(() => {});
  }

  // Who is talking (a ring around the tile), and their sound.
  function startAnalyser(peer, stream) {
    stopAnalyser(peer);
    const el = h('audio', { autoplay: true, class: 'mt-remote', 'data-peer': peer });
    el.srcObject = stream;
    if (pref('spk', '') && el.setSinkId) el.setSinkId(pref('spk', '')).catch(() => {});
    document.body.append(el);
    const a = ctx();
    if (!a) return;
    try {
      const src = a.createMediaStreamSource(stream);
      const an = a.createAnalyser();
      an.fftSize = 512;
      src.connect(an);
      st.analysers[peer] = { src, an, buf: new Uint8Array(an.fftSize) };
    } catch (e) { /* no levels */ }
  }
  function stopAnalyser(peer) {
    const a = st.analysers[peer];
    if (a) { try { a.src.disconnect(); } catch (e) { /* gone */ } delete st.analysers[peer]; }
    document.querySelectorAll('audio.mt-remote[data-peer="' + peer + '"]').forEach((el) => el.remove());
  }
  setInterval(() => {
    if (view !== 'room') return;
    const levels = Object.entries(st.analysers);
    if (meter && st.me) levels.push([st.me, null]);
    for (const [peer, a] of levels) {
      let on;
      if (a) {
        a.an.getByteTimeDomainData(a.buf);
        let sum = 0;
        for (const v of a.buf) sum += (v - 128) * (v - 128);
        on = Math.sqrt(sum / a.buf.length) > 6;
      } else on = st.micOn && meter.level > 12;
      if (!!st.speaking[peer] !== on) {
        st.speaking[peer] = on;
        document.querySelectorAll('.mt-tile[data-peer="' + peer + '"]').forEach((t) => t.classList.toggle('speaking', on));
      }
    }
  }, 250);
  // The meeting clock and the captions fade.
  setInterval(() => {
    if (view !== 'room') return;
    const t = clock(Date.now() - st.started);
    document.querySelectorAll('[data-clock]').forEach((el) => { el.textContent = t; });
    const rec = document.querySelector('[data-rec-clock]');
    if (rec && st.rec) rec.textContent = clock(Date.now() - st.recStart);
    drawCaptions();
  }, 1000);

  // ------------------------------------------------------------ room
  function videoFor(key, stream) {
    let v = videos.get(key);
    if (!v) { v = h('video', { autoplay: true, playsinline: true, muted: true }); videos.set(key, v); }
    if (v.srcObject !== stream) { v.srcObject = stream; v.play().catch(() => {}); }
    return v;
  }
  function peerName(id) { const p = st.peers.find((x) => x.id === id); return p ? p.name : ''; }

  function tile(p, self) {
    const stream = self ? (st.cam ? new MediaStream([st.cam]) : null) : (st.remote[p.id] || {}).camera;
    const showVideo = self ? st.camOn && st.cam : p.video && stream;
    const t = h('div', { class: 'mt-tile' + (st.speaking[p.id] ? ' speaking' : '') + (showVideo ? ' video' : ''), 'data-peer': p.id });
    if (showVideo) {
      const v = videoFor(self ? 'self' : p.id + '|camera', self ? videoSelfStream() : stream);
      v.classList.toggle('mirror', !!self && pref('mirror', '1') === '1');
      t.append(v);
    } else {
      t.append(h('span', { class: 'mt-av tile-av', style: 'background:' + colorOf(p.name) }, initials(p.name)));
    }
    if (p.hand) t.append(h('span', { class: 'mt-hand', title: 'Hand raised' }, ic('hand', 16)));
    t.append(h('span', { class: 'mt-chip' }, ic((self ? st.micOn : p.audio) ? 'mic' : 'micoff', 14), p.name + (self ? ' (you)' : '') + (p.host && !self ? ' · host' : '')));
    return t;
  }
  let selfStream = null;
  function videoSelfStream() {
    if (!selfStream || selfStream.getVideoTracks()[0] !== st.cam) selfStream = new MediaStream([st.cam]);
    return selfStream;
  }

  function spotlight() {
    const sharer = st.screen ? { id: st.me, self: true } : st.peers.find((x) => x.screen && st.remote[x.id] && st.remote[x.id].screen);
    if (sharer) {
      if (sharer.self && (!st.screenStream || st.screenStream.getVideoTracks()[0] !== st.screen)) st.screenStream = new MediaStream([st.screen]);
      const v = videoFor(sharer.self ? 'self-screen' : sharer.id + '|screen', sharer.self ? st.screenStream : st.remote[sharer.id].screen);
      return h('div', { class: 'mt-spot' }, v, h('span', { class: 'mt-chip' }, ic('share', 14), (sharer.self ? 'You are' : peerName(sharer.id) + ' is') + ' sharing the screen'));
    }
    if (st.present) return presentation();
    if (st.board) return whiteboard();
    return null;
  }

  function roomView() {
    const me = st.peers.find((p) => p.id === st.me) || { id: st.me, name: 'You', audio: st.micOn, video: st.camOn };
    const others = st.peers.filter((p) => p.id !== st.me);
    const spot = spotlight();
    const tiles = [tile(me, true), others.map((p) => tile(p, false))];
    const n = others.length + 1;
    const stage = h('div', { class: 'mt-stage' + (spot ? ' has-spot' : '') },
      spot,
      h('div', { class: 'mt-grid n' + Math.min(n, 9) + (spot ? ' strip' : '') }, tiles),
      knock(),
      h('div', { class: 'mt-captions' + (st.transcribing ? '' : ' hidden') }));
    const top = h('div', { class: 'mt-top' },
      h('span', { class: 'mt-clock', 'data-clock': '1' }, clock(Date.now() - st.started)),
      st.locked ? h('span', { class: 'mt-pill' }, ic('lock', 12), 'Locked') : null,
      st.rec ? h('span', { class: 'mt-pill red' }, h('i', { class: 'mt-recdot' }), 'Recording ', h('span', { 'data-rec-clock': '1' }, clock(Date.now() - st.recStart))) : null,
      st.transcribing ? h('span', { class: 'mt-pill blue' }, ic('captions', 12), 'Transcribing') : null,
      h('span', { class: 'mt-grow' }),
      h('button', { class: 'mt-btn sm', type: 'button', onclick: copyLink }, ic('link', 15), 'Copy link'),
      h('button', { class: 'mt-btn sm' + (st.rec ? ' on-red' : ''), type: 'button', onclick: () => (st.rec ? stopRecording() : startRecording()), title: 'Records the meeting in this browser and saves it to your downloads' }, ic('record', 15), st.rec ? 'Stop recording' : 'Record'),
      h('button', { class: 'mt-btn sm' + (st.transcribing ? ' on' : ''), type: 'button', onclick: toggleTranscribe }, ic('captions', 15), st.transcribing ? 'Stop transcribing' : 'Transcribe'),
      h('button', { class: 'mt-btn sm' + (st.present && st.present.peer === st.me ? ' on' : ''), type: 'button', onclick: presentFile }, ic('file', 15), st.present && st.present.peer === st.me ? 'Stop presenting' : 'Present file'));
    const waitingN = st.host ? st.lobby.length : 0;
    const bar = h('div', { class: 'mt-bar' },
      h('div', { class: 'mt-bar-mid' },
        h('div', { class: 'mt-pair mt-anchor' },
          h('button', { class: 'mt-caret', type: 'button', title: 'Audio settings', onclick: () => popup('mic') }, ic('up', 14)),
          h('button', { class: 'mt-round' + (st.micOn ? '' : ' red'), type: 'button', title: st.micOn ? 'Mute' : 'Unmute', 'aria-pressed': String(!st.micOn), onclick: toggleMic }, ic(st.micOn ? 'mic' : 'micoff', 18)),
          st.popup === 'mic' ? devicePopup('mic') : null),
        h('div', { class: 'mt-pair mt-anchor' },
          h('button', { class: 'mt-caret', type: 'button', title: 'Video settings', onclick: () => popup('cam') }, ic('up', 14)),
          h('button', { class: 'mt-round' + (st.camOn ? '' : ' red'), type: 'button', title: st.camOn ? 'Turn the camera off' : 'Turn the camera on', 'aria-pressed': String(!st.camOn), onclick: toggleCam }, ic(st.camOn ? 'video' : 'videooff', 18)),
          st.popup === 'cam' ? devicePopup('cam') : null),
        h('button', { class: 'mt-round' + (st.screen ? ' on' : ''), type: 'button', title: st.screen ? 'Stop sharing' : 'Share your screen', onclick: toggleScreen }, ic('share', 18)),
        h('button', { class: 'mt-round' + (st.hand ? ' on' : ''), type: 'button', title: st.hand ? 'Lower your hand' : 'Raise your hand', onclick: () => { st.hand = !st.hand; sendState(); } }, ic('hand', 18)),
        h('div', { class: 'mt-anchor' }, h('button', { class: 'mt-round', type: 'button', title: 'Reactions', onclick: () => popup('emoji') }, ic('smile', 18)),
          st.popup === 'emoji' ? h('div', { class: 'mt-pop mt-emojis' }, ['👍', '👏', '😂', '❤️', '🎉', '😮'].map((e) => h('button', { type: 'button', onclick: () => { send({ t: 'meet.reaction', body: e }); st.popup = ''; redraw(); } }, e))) : null),
        h('button', { class: 'mt-round' + (st.board ? ' on' : ''), type: 'button', title: st.board ? 'Close the whiteboard' : 'Whiteboard', onclick: () => send({ t: 'meet.board', on: !st.board }) }, ic('pen', 18)),
        h('button', { class: 'mt-round' + (st.side === 'chat' ? ' on' : ''), type: 'button', title: 'Chat', onclick: () => side('chat') }, ic('chat', 18), st.unread ? h('span', { class: 'mt-badge' }, String(st.unread)) : null),
        h('div', { class: 'mt-anchor' }, h('button', { class: 'mt-leave', type: 'button', title: 'Leave', onclick: () => (st.host ? popup('leave') : leave()) }, ic('hangup', 20)),
          st.popup === 'leave' ? h('div', { class: 'mt-pop mt-leavepop' },
            h('button', { class: 'mt-btn wide', type: 'button', onclick: () => leave() }, 'Leave the meeting'),
            h('button', { class: 'mt-btn wide danger', type: 'button', onclick: () => { if (confirm('End the meeting for everyone?')) send({ t: 'meet.end' }); } }, 'End the meeting for all')) : null)),
      h('div', { class: 'mt-bar-right' },
        h('button', { class: 'mt-round' + (st.side === 'settings' ? ' on' : ''), type: 'button', title: 'Settings', onclick: () => side('settings') }, ic('settings', 18)),
        h('button', { class: 'mt-people' + (st.side === 'people' ? ' on' : ''), type: 'button', title: 'People', onclick: () => side('people') }, ic('users', 18), String(st.peers.length || 1), waitingN ? h('span', { class: 'mt-badge red' }, String(waitingN)) : null),
        h('button', { class: 'mt-round' + (st.side === 'host' || st.side === 'transcript' ? ' on' : ''), type: 'button', title: st.host ? 'Host controls' : 'Meeting info', onclick: () => side('host') }, ic('shieldcheck', 18))));
    return h('div', { class: 'mt-room' + (mode === 'page' ? ' page' : '') }, top,
      h('div', { class: 'mt-main' }, stage, st.side ? sidePanel() : null),
      h('div', { class: 'mt-micwarn hidden mt-room-warn' }, ic('activity', 16), 'Your microphone is on but is not picking anything up.'),
      bar);
  }
  function popup(name) { st.popup = st.popup === name ? '' : name; redraw(); }
  function side(name) { st.side = st.side === name ? '' : name; if (st.side === 'chat') st.unread = 0; st.popup = ''; redraw(); }
  document.addEventListener('click', (e) => {
    if (st.popup && !e.target.closest('.mt-anchor')) { st.popup = ''; redraw(); }
  });

  function devicePopup(kind) {
    const box = h('div', { class: 'mt-pop mt-devpop' }, h('b', null, kind === 'mic' ? 'Microphone and speaker' : 'Camera'), h('div', { class: 'mt-muted small' }, 'Loading…'));
    deviceList().then((list) => {
      fill(box, h('b', null, kind === 'mic' ? 'Microphone and speaker' : 'Camera'),
        kind === 'mic' ? [deviceSelect('mic', list), h('div', { class: 'mt-level' }, h('i')), 'setSinkId' in HTMLMediaElement.prototype ? deviceSelect('spk', list) : null]
          : [deviceSelect('cam', list), h('div', { class: 'mt-small-title' }, 'Effects'), h('div', { class: 'mt-fxrow' }, EFFECTS.map((e) => h('button', { class: 'mt-effect sm' + (effect === e.id ? ' on' : ''), type: 'button', onclick: async () => { effect = e.id; setPref('effect', e.id); await applyEffect(); applyLocal(); redraw(); } }, h('span', { class: 'mt-fx mt-fx-' + (e.id || 'none') }), e.name)))]);
    });
    return box;
  }

  // Somebody knocks: the host lets them in or not.
  function knock() {
    if (!st.host || !st.lobby.length) return null;
    const w = st.lobby[0];
    return h('div', { class: 'mt-knock' },
      h('span', null, h('b', null, w.name), ' wants to join', st.lobby.length > 1 ? h('button', { class: 'mt-link', type: 'button', onclick: () => side('people') }, ' and ' + (st.lobby.length - 1) + ' more') : null),
      h('button', { class: 'mt-btn sm', type: 'button', onclick: () => send({ t: 'meet.deny', peer: w.id }) }, 'Decline'),
      h('button', { class: 'mt-btn sm primary', type: 'button', onclick: () => send({ t: 'meet.admit', peer: w.id }) }, 'Admit'));
  }

  function sidePanel() {
    const titles = { chat: 'Chat', people: 'People', settings: 'Settings', host: st.host ? 'Host controls' : 'Meeting info', transcript: 'Transcript' };
    const head = h('div', { class: 'mt-side-head' }, h('b', null, titles[st.side]), h('button', { class: 'mt-wbtn', type: 'button', 'aria-label': 'Close', onclick: () => side(st.side) }, ic('x', 16)));
    let body;
    if (st.side === 'chat') body = chatPanel();
    else if (st.side === 'people') body = peoplePanel();
    else if (st.side === 'settings') body = settingsPanel();
    else if (st.side === 'transcript') body = transcriptPanel();
    else body = hostPanel();
    return h('div', { class: 'mt-side' }, head, body);
  }

  function chatPanel() {
    const input = h('input', { id: 'mt-chat-input', class: 'mt-input', placeholder: 'Message everyone', maxlength: '2000' });
    const sendMsg = () => { const t = input.value.trim(); if (t) { send({ t: 'meet.chat', body: t }); input.value = ''; } };
    input.addEventListener('keydown', (e) => { if (e.key === 'Enter') sendMsg(); });
    return [h('div', { class: 'mt-side-body mt-chatlog' }, st.chat.length ? st.chat.map((m) => h('div', { class: 'mt-msg' + (m.from === st.me ? ' mine' : '') },
      h('div', { class: 'mt-msg-head' }, h('b', null, m.from === st.me ? 'You' : m.name), h('span', { class: 'mt-muted' }, new Date(m.time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }))), h('div', null, m.text))) :
      h('div', { class: 'mt-muted small' }, 'Messages are seen by everyone in the meeting and kept with its recent content.')),
    h('div', { class: 'mt-side-input' }, input, h('button', { class: 'mt-btn primary sm', type: 'button', onclick: sendMsg }, 'Send'))];
  }

  function peoplePanel() {
    const hostBtns = (p) => (st.host && p.id !== st.me ? [
      p.audio ? h('button', { class: 'mt-btn sm', type: 'button', onclick: () => send({ t: 'meet.mute', peer: p.id }) }, 'Mute') : null,
      h('button', { class: 'mt-btn sm', type: 'button', onclick: () => { if (confirm('Remove ' + p.name + ' from the meeting?')) send({ t: 'meet.remove', peer: p.id }); } }, 'Remove')] : null);
    return h('div', { class: 'mt-side-body' },
      st.host && st.lobby.length ? h('div', { class: 'mt-lobby' }, h('div', { class: 'mt-small-title' }, 'Waiting to join (' + st.lobby.length + ')'),
        st.lobby.length > 1 ? h('button', { class: 'mt-btn sm primary', type: 'button', onclick: () => st.lobby.forEach((w) => send({ t: 'meet.admit', peer: w.id })) }, 'Admit all') : null,
        st.lobby.map((w) => h('div', { class: 'mt-person' }, h('span', { class: 'mt-av sm', style: 'background:' + colorOf(w.name) }, initials(w.name)), h('span', { class: 'mt-grow' }, w.name),
          h('button', { class: 'mt-btn sm', type: 'button', onclick: () => send({ t: 'meet.deny', peer: w.id }) }, 'Decline'),
          h('button', { class: 'mt-btn sm primary', type: 'button', onclick: () => send({ t: 'meet.admit', peer: w.id }) }, 'Admit')))) : null,
      h('div', { class: 'mt-small-title' }, 'In the meeting (' + st.peers.length + ')'),
      st.peers.map((p) => h('div', { class: 'mt-person' },
        h('span', { class: 'mt-av sm', style: 'background:' + colorOf(p.name) }, initials(p.name)),
        h('div', { class: 'mt-grow' }, h('div', null, p.name + (p.id === st.me ? ' (you)' : '')), h('div', { class: 'mt-muted small' }, [p.host ? 'Host' : p.guest ? 'Guest' : 'Member', p.hand ? 'hand raised' : '', p.screen ? 'sharing' : ''].filter(Boolean).join(' · '))),
        ic(p.audio ? 'mic' : 'micoff', 15), ic(p.video ? 'video' : 'videooff', 15), hostBtns(p))),
      h('button', { class: 'mt-btn sm wide', type: 'button', onclick: copyLink }, ic('link', 14), 'Copy the invite link'));
  }

  function settingsPanel() {
    const box = h('div', { class: 'mt-side-body' }, h('div', { class: 'mt-muted small' }, 'Loading…'));
    deviceList().then((list) => {
      const langs = [['', 'Browser language'], ['en-US', 'English (US)'], ['en-GB', 'English (UK)'], ['en-IN', 'English (India)'], ['en-SG', 'English (Singapore)'],
        ['ta-IN', 'Tamil'], ['hi-IN', 'Hindi'], ['ms-MY', 'Malay'], ['zh-CN', 'Chinese (Mandarin)'], ['ar-AE', 'Arabic'], ['fr-FR', 'French'], ['de-DE', 'German'], ['es-ES', 'Spanish']];
      const lang = h('select', { class: 'mt-select' }, langs.map(([v, l]) => h('option', { value: v, selected: pref('lang', '') === v }, l)));
      lang.addEventListener('change', () => { setPref('lang', lang.value); if (recog) recog.stop(); });
      const mirror = h('input', { type: 'checkbox', checked: pref('mirror', '1') === '1' });
      mirror.addEventListener('change', () => { setPref('mirror', mirror.checked ? '1' : '0'); redraw(); });
      fill(box,
        h('label', { class: 'mt-label' }, 'Microphone', deviceSelect('mic', list)),
        h('div', { class: 'mt-level' }, h('i')),
        'setSinkId' in HTMLMediaElement.prototype ? h('label', { class: 'mt-label' }, 'Speaker', deviceSelect('spk', list)) : null,
        h('label', { class: 'mt-label' }, 'Camera', deviceSelect('cam', list)),
        h('div', { class: 'mt-small-title' }, 'Effects'),
        h('div', { class: 'mt-fxrow' }, EFFECTS.map((e) => h('button', { class: 'mt-effect sm' + (effect === e.id ? ' on' : ''), type: 'button', onclick: async () => { effect = e.id; setPref('effect', e.id); await applyEffect(); applyLocal(); redraw(); } }, h('span', { class: 'mt-fx mt-fx-' + (e.id || 'none') }), e.name))),
        h('label', { class: 'mt-switch-row' }, h('div', null, h('b', null, 'Mirror my video'), h('span', { class: 'mt-muted' }, 'Only on your screen; others see it the right way round')), h('span', { class: 'mt-switch' }, mirror, h('span'))),
        h('label', { class: 'mt-label' }, 'Transcription language', lang),
        h('p', { class: 'mt-muted small' }, 'Transcription uses each browser\'s speech recognition (browsers send the audio to their own speech service).'));
    });
    return box;
  }

  function hostPanel() {
    const info = [
      h('div', { class: 'mt-kv' }, h('span', null, 'Meeting'), h('b', null, st.title)),
      h('div', { class: 'mt-kv' }, h('span', null, 'Code'), h('b', null, st.code)),
      h('div', { class: 'mt-kv' }, h('span', null, 'Link'), h('button', { class: 'mt-link', type: 'button', onclick: copyLink }, location.host + st.link)),
      h('button', { class: 'mt-btn sm wide', type: 'button', onclick: () => side('transcript') }, ic('captions', 14), 'Transcript (' + st.transcript.length + ' lines)'),
    ];
    if (!st.host) return h('div', { class: 'mt-side-body' }, info);
    return h('div', { class: 'mt-side-body' }, info,
      h('div', { class: 'mt-small-title' }, 'Meeting controls'),
      switchRow('Waiting room', 'People wait until you admit them; off lets everyone waiting in', st.lobbyOn, (v) => send({ t: 'meet.lobby', on: v })),
      switchRow('Lock the meeting', 'Nobody new gets in without a host admitting them', st.locked, (v) => send({ t: 'meet.lock', on: v })),
      h('button', { class: 'mt-btn sm wide', type: 'button', onclick: () => send({ t: 'meet.muteall' }) }, ic('micoff', 14), 'Mute everyone'),
      h('button', { class: 'mt-btn sm wide danger', type: 'button', onclick: () => { if (confirm('End the meeting for everyone?')) send({ t: 'meet.end' }); } }, ic('hangup', 14), 'End the meeting for all'));
  }

  function transcriptPanel() {
    const dl = () => {
      const text = st.transcript.map((m) => '[' + new Date(m.time).toLocaleTimeString() + '] ' + m.name + ': ' + m.text).join('\n');
      const a = h('a', { href: URL.createObjectURL(new Blob([text], { type: 'text/plain' })), download: (st.title || 'meeting').replace(/[^\w.-]+/g, '-') + '-transcript.txt' });
      document.body.append(a); a.click(); a.remove();
    };
    return [h('div', { class: 'mt-side-body mt-chatlog' }, st.transcript.length ? st.transcript.map((m) => h('div', { class: 'mt-msg' },
      h('div', { class: 'mt-msg-head' }, h('b', null, m.name), h('span', { class: 'mt-muted' }, new Date(m.time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }))), h('div', null, m.text))) :
      h('div', { class: 'mt-muted small' }, st.transcribing ? 'Listening… lines appear as people speak.' : 'Press Transcribe (top) to write down what is said.')),
    h('div', { class: 'mt-side-input' }, h('button', { class: 'mt-btn sm wide', type: 'button', onclick: dl, disabled: !st.transcript.length }, ic('download', 14), 'Download the transcript'))];
  }

  function copyLink() {
    const url = location.origin + (st.link || '/meet/m/' + st.code);
    (navigator.clipboard ? navigator.clipboard.writeText(url) : Promise.reject(new Error('no clipboard')))
      .then(() => say('Link copied: ' + url)).catch(() => prompt('Copy the meeting link:', url));
  }

  function showReaction(peer, emoji) {
    const t = document.querySelector('.mt-tile[data-peer="' + peer + '"]');
    if (!t) return;
    const r = h('div', { class: 'mt-react' }, emoji);
    t.append(r);
    setTimeout(() => r.remove(), 2600);
  }

  // ------------------------------------------------------------ screen sharing
  async function toggleScreen() {
    if (st.screen) { stopScreen(); return; }
    try {
      const s = await navigator.mediaDevices.getDisplayMedia({ video: { frameRate: { ideal: 15, max: 30 } }, audio: false });
      st.screen = s.getVideoTracks()[0];
      st.screen.onended = stopScreen;
      await replaceOwn(2, st.screen);
      sendState();
      redraw();
    } catch (e) { if (e.name !== 'NotAllowedError') say('Screen sharing failed: ' + e.message, true); }
  }
  async function stopScreen() {
    if (!st.screen) return;
    st.screen.stop();
    st.screen = null;
    await replaceOwn(2, null);
    sendState();
    redraw();
  }

  // ------------------------------------------------------------ present a file
  function presentFile() {
    if (st.present && st.present.peer === st.me) { send({ t: 'meet.present', data: { stop: true } }); return; }
    const input = h('input', { type: 'file', accept: '.pdf,.png,.jpg,.jpeg,.gif,.webp,application/pdf,image/*', class: 'hidden' });
    input.addEventListener('change', async () => {
      const f = input.files[0];
      input.remove();
      if (!f) return;
      if (f.size > 25 * 1024 * 1024) { say('The file is too large (25 MB at most)', true); return; }
      say('Uploading ' + f.name + '…');
      try {
        const r = await request('POST', 'guest/file?token=' + encodeURIComponent(st.token) + '&name=' + encodeURIComponent(f.name), undefined, f);
        send({ t: 'meet.present', data: { file: r.id, page: 1 } });
      } catch (e) { say(e.message, true); }
    });
    document.body.append(input);
    input.click();
  }
  function presentation() {
    const p = st.present;
    const src = '/api/guest/file/' + p.file + '?token=' + encodeURIComponent(st.token);
    const mine = p.peer === st.me;
    let view;
    if (p.type === 'application/pdf') {
      view = videos.get('pdf');
      const want = src + '#page=' + p.page + '&toolbar=0&navpanes=0&view=FitH';
      if (!view) { view = h('iframe', { class: 'mt-doc', title: p.title }); videos.set('pdf', view); }
      if (view.getAttribute('src') !== want) view.setAttribute('src', want);
    } else {
      view = h('img', { class: 'mt-doc img', src, alt: p.title });
    }
    const go = (d) => send({ t: 'meet.present', data: { page: Math.max(1, p.page + d) } });
    return h('div', { class: 'mt-spot doc' }, view,
      h('div', { class: 'mt-doc-bar' }, ic('file', 14), h('span', { class: 'mt-ellipsis' }, (mine ? 'You are presenting ' : p.name + ' is presenting ') + p.title),
        p.type === 'application/pdf' ? [mine ? h('button', { class: 'mt-btn sm', type: 'button', onclick: () => go(-1), disabled: p.page <= 1 }, '‹') : null, h('span', null, 'Page ' + p.page), mine ? h('button', { class: 'mt-btn sm', type: 'button', onclick: () => go(1) }, '›') : null] : null,
        mine || st.host ? h('button', { class: 'mt-btn sm', type: 'button', onclick: () => send({ t: 'meet.present', data: { stop: true } }) }, 'Stop') : null));
  }

  // ------------------------------------------------------------ whiteboard
  let board = null; // {canvas, g}
  let pen = { color: '#1f2937', width: 3 };
  function whiteboard() {
    let c = videos.get('board');
    if (!c) {
      c = h('canvas', { class: 'mt-board-canvas' });
      videos.set('board', c);
      let cur = null; let last = 0;
      const pt = (e) => { const r = c.getBoundingClientRect(); return [Math.round(((e.clientX - r.left) / r.width) * 10000), Math.round(((e.clientY - r.top) / r.height) * 10000)]; };
      const flush = (final) => {
        if (!cur || cur.p.length < 4) { if (final) cur = null; return; }
        const stroke = { c: cur.c, w: cur.w, p: cur.p };
        send({ t: 'meet.draw', data: stroke });
        st.strokes.push(stroke);
        cur = final ? null : { c: cur.c, w: cur.w, p: cur.p.slice(-2) };
      };
      c.addEventListener('pointerdown', (e) => { c.setPointerCapture(e.pointerId); cur = { c: pen.color, w: pen.width, p: pt(e) }; last = Date.now(); });
      c.addEventListener('pointermove', (e) => {
        if (!cur) return;
        const [x, y] = pt(e);
        const n = cur.p.length;
        cur.p.push(x, y);
        drawSegment(cur.c, cur.w, cur.p[n - 2], cur.p[n - 1], x, y);
        if (Date.now() - last > 80 || cur.p.length > 400) { last = Date.now(); flush(false); }
      });
      const end = () => flush(true);
      c.addEventListener('pointerup', end);
      c.addEventListener('pointercancel', end);
      new ResizeObserver(() => redrawBoard()).observe(c);
    }
    board = { canvas: c, g: c.getContext('2d') };
    setTimeout(redrawBoard, 0);
    const colors = ['#1f2937', '#1a8fe3', '#dc2626', '#16a34a', '#f59e0b'];
    return h('div', { class: 'mt-spot board' }, c,
      h('div', { class: 'mt-board-tools' },
        colors.map((col) => h('button', { class: 'mt-swatch' + (pen.color === col && pen.width < 10 ? ' on' : ''), type: 'button', style: 'background:' + col, title: 'Pen', onclick: () => { pen = { color: col, width: 3 }; redraw(); } })),
        h('button', { class: 'mt-btn sm' + (pen.width >= 10 ? ' on' : ''), type: 'button', title: 'Eraser', onclick: () => { pen = { color: '#ffffff', width: 22 }; redraw(); } }, ic('eraser', 14)),
        h('button', { class: 'mt-btn sm', type: 'button', onclick: () => send({ t: 'meet.clear' }) }, 'Clear'),
        h('button', { class: 'mt-btn sm', type: 'button', onclick: () => send({ t: 'meet.board', on: false }) }, 'Close')));
  }
  function drawSegment(color, w, x1, y1, x2, y2) {
    if (!board) return;
    const c = board.canvas, g = board.g;
    g.strokeStyle = color; g.lineWidth = w * (c.width / 1000); g.lineCap = 'round'; g.lineJoin = 'round';
    g.beginPath(); g.moveTo((x1 / 10000) * c.width, (y1 / 10000) * c.height); g.lineTo((x2 / 10000) * c.width, (y2 / 10000) * c.height); g.stroke();
  }
  function drawStroke(s) {
    if (!board || !s || !Array.isArray(s.p)) return;
    for (let i = 2; i + 1 < s.p.length; i += 2) drawSegment(s.c, s.w, s.p[i - 2], s.p[i - 1], s.p[i], s.p[i + 1]);
  }
  function redrawBoard() {
    if (!board) return;
    const c = board.canvas;
    const r = c.getBoundingClientRect();
    if (!r.width) return;
    const dpr = window.devicePixelRatio || 1;
    c.width = Math.round(r.width * dpr); c.height = Math.round(r.height * dpr);
    board.g.fillStyle = '#ffffff'; board.g.fillRect(0, 0, c.width, c.height);
    for (const s of st.strokes) drawStroke(s);
  }

  // ------------------------------------------------------------ transcription
  let recog = null;
  const Recognition = window.SpeechRecognition || window.webkitSpeechRecognition;
  function toggleTranscribe() {
    if (!st.transcribing && !Recognition) say('This browser cannot transcribe your voice (use a browser with speech recognition); other people\'s speech is still written down.', true);
    send({ t: 'meet.transcribe', on: !st.transcribing });
  }
  function startRecognition() {
    if (!Recognition || recog || !st.micOn || view !== 'room') return;
    const r = new Recognition();
    r.continuous = true;
    r.interimResults = true;
    r.lang = pref('lang', '') || navigator.language || 'en-US';
    let lastInterim = 0;
    r.onresult = (e) => {
      for (let i = e.resultIndex; i < e.results.length; i++) {
        const res = e.results[i];
        const text = res[0].transcript.trim();
        if (!text) continue;
        if (res.isFinal) send({ t: 'meet.caption', body: text, on: true });
        else if (Date.now() - lastInterim > 700) { lastInterim = Date.now(); send({ t: 'meet.caption', body: text }); }
      }
    };
    r.onerror = (e) => { if (e.error === 'not-allowed' || e.error === 'service-not-allowed') { say('Speech recognition is blocked in this browser', true); st.noRecog = true; } };
    r.onend = () => { recog = null; if (st.transcribing && st.micOn && view === 'room' && !st.noRecog) setTimeout(startRecognition, 300); };
    try { r.start(); recog = r; } catch (e) { recog = null; }
  }
  function drawCaptions() {
    const box = document.querySelector('.mt-captions');
    if (!box) return;
    const now = Date.now();
    const lines = Object.values(st.captions).filter((c) => now - c.at < 6000).sort((a, b) => a.at - b.at).slice(-2);
    fill(box, lines.map((c) => h('div', null, h('b', null, c.name + ': '), c.text)));
    box.classList.toggle('hidden', !st.transcribing || !lines.length);
  }

  // ------------------------------------------------------------ recording
  function startRecording() {
    if (!window.MediaRecorder) { say('This browser cannot record', true); return; }
    const canvas = h('canvas', { width: 1280, height: 720 });
    const g = canvas.getContext('2d');
    const a = ctx();
    const dest = a.createMediaStreamDestination();
    const sources = [];
    const addAudio = (s) => { try { const n = a.createMediaStreamSource(s); n.connect(dest); sources.push(n); } catch (e) { /* no audio */ } };
    if (st.mic) addAudio(new MediaStream([st.mic]));
    for (const r of Object.values(st.remote)) if (r.audio) addAudio(r.audio);
    let running = true;
    const draw = () => {
      if (!running) return;
      g.fillStyle = '#0f1418';
      g.fillRect(0, 0, 1280, 720);
      const spot = document.querySelector('.mt-spot video, .mt-spot canvas, .mt-spot img');
      const vids = Array.from(document.querySelectorAll('.mt-grid video')).filter((v) => v.videoWidth);
      if (spot && (spot.videoWidth || spot.width || spot.naturalWidth)) {
        try { g.drawImage(spot, 0, 0, 1000, 720); } catch (e) { /* not drawable */ }
        vids.slice(0, 4).forEach((v, i) => g.drawImage(v, 1010, i * 180, 270, 170));
      } else {
        const n = Math.max(vids.length, 1);
        const cols = Math.ceil(Math.sqrt(n)), rows = Math.ceil(n / cols);
        const w = 1280 / cols, hh = 720 / rows;
        vids.forEach((v, i) => g.drawImage(v, (i % cols) * w, Math.floor(i / cols) * hh, w - 4, hh - 4));
      }
    };
    const timer = setInterval(draw, 1000 / 24);
    const stream = new MediaStream([...canvas.captureStream(24).getVideoTracks(), ...dest.stream.getAudioTracks()]);
    const chunks = [];
    const type = ['video/webm;codecs=vp9,opus', 'video/webm;codecs=vp8,opus', 'video/webm'].find((t) => MediaRecorder.isTypeSupported(t)) || '';
    const rec = new MediaRecorder(stream, type ? { mimeType: type } : {});
    rec.ondataavailable = (e) => { if (e.data.size) chunks.push(e.data); };
    rec.onstop = () => {
      running = false;
      clearInterval(timer);
      sources.forEach((n) => n.disconnect());
      const link = h('a', { href: URL.createObjectURL(new Blob(chunks, { type: 'video/webm' })), download: 'meeting-' + st.code + '-' + new Date().toISOString().slice(0, 16).replace(':', '') + '.webm' });
      document.body.append(link);
      link.click();
      link.remove();
      say('The recording is saved to your downloads');
    };
    rec.start(1000);
    st.rec = rec;
    st.recStart = Date.now();
    send({ t: 'meet.chat', body: '● Recording started' });
    redraw();
  }
  function stopRecording() {
    if (!st.rec) return;
    st.rec.stop();
    st.rec = null;
    send({ t: 'meet.chat', body: '■ Recording stopped' });
    redraw();
  }

  // ------------------------------------------------------------ leaving
  function leave(quiet) {
    send({ t: 'meet.leave' });
    const was = view;
    teardownCall();
    if (quiet) return;
    if (was === 'room') ended('');
  }
  function teardownCall() {
    if (st.rec) stopRecording();
    if (recog) { st.transcribing = false; recog.stop(); recog = null; }
    if (st.pc) st.pc.close();
    st.pc = null; st.ownMids = null;
    if (st.ws) { st.ws.onclose = null; st.ws.close(); st.ws = null; }
    for (const id of Object.keys(st.remote)) stopAnalyser(id);
    st.remote = {}; st.peers = []; st.lobby = []; st.speaking = {}; st.present = null; st.board = false; st.strokes = []; st.captions = {};
    st.side = ''; st.popup = ''; st.hand = false; st.unread = 0;
    if (st.screen) { st.screen.stop(); st.screen = null; }
    for (const [k, v] of videos) { if (v.srcObject !== undefined) v.srcObject = null; videos.delete(k); }
    board = null;
  }
  function teardown() { teardownCall(); stopDevices(); }
  function ended(reason) {
    teardownCall();
    view = 'ended';
    st.endReason = reason;
    if (shell) { shell.mini.classList.add('hidden'); shell.overlay.classList.remove('hidden'); }
    redraw();
  }
  function endedView() {
    return h('div', { class: 'mt-center' },
      h('span', { class: 'mt-wicon big' }, ic('video', 26)),
      h('h2', null, 'You left the meeting'),
      st.endReason ? h('p', { class: 'mt-muted' }, st.endReason.charAt(0).toUpperCase() + st.endReason.slice(1)) : null,
      h('div', { class: 'mt-row' },
        h('button', { class: 'mt-btn primary', type: 'button', onclick: async () => { if (!st.local) await openDevices(); join(); } }, 'Rejoin'),
        mode === 'window' ? h('button', { class: 'mt-btn', type: 'button', onclick: () => { view = 'prejoin'; recent = null; redraw(); } }, 'Back to Meet') : null));
  }
  window.addEventListener('beforeunload', () => send({ t: 'meet.leave' }));

  // ------------------------------------------------------------ drawing
  function redraw() {
    if (!root) return;
    if (view === 'room') {
      const focused = document.activeElement && document.activeElement.id === 'mt-chat-input';
      const draft = focused ? document.activeElement.value : null;
      const log = root.querySelector('.mt-chatlog');
      const atEnd = !log || log.scrollTop + log.clientHeight >= log.scrollHeight - 30;
      fill(root, roomView());
      if (focused) { const i = document.getElementById('mt-chat-input'); i.value = draft; i.focus(); }
      const nlog = root.querySelector('.mt-chatlog');
      if (nlog && atEnd) nlog.scrollTop = nlog.scrollHeight;
      drawCaptions();
    } else if (view === 'ended') fill(root, endedView());
    else if (mode === 'window') fill(root, view === 'waiting' ? waitingCard() : memberPrejoin());
    else fill(root, guestPrejoin());
    if (shell) shell.win.classList.toggle('in-room', view === 'room');
    drawMini();
  }
  function waitingCard() {
    return h('div', { class: 'mt-center' }, h('span', { class: 'mt-spin big' }), h('h2', null, st.title || 'Meeting'),
      h('p', { class: 'mt-muted' }, st.waitMsg || 'Waiting for the organizer to let you in'),
      h('button', { class: 'mt-btn', type: 'button', onclick: () => { leave(true); view = 'prejoin'; redraw(); } }, 'Cancel'));
  }

  if (document.body && document.body.hasAttribute('data-meet-page')) page();
  return { openWindow, inMeeting: () => view === 'room' || view === 'waiting' };
})();
