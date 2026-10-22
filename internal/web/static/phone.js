'use strict';
// The browser dialer: WebRTC audio to the server, which registers a SIP
// phone as your extension. Calls, hold, mute, keypad, transfers, your
// break / AUX status, do not disturb, contacts, recent calls and voicemail.

const Phone = (() => {
  let pc = null;
  // The microphone is open only while something needs it (a call, the level
  // meter in Settings): browsers show a recording dot in the tab as long as
  // it is open.
  let stream = null;
  const micUsers = new Set();
  let micOpening = null;
  let micGraph = null; // the boost's Web Audio nodes
  let answering = null; // the incoming call being answered
  let meterSeq = 0;
  let started = false;
  let reg = { ok: false, reason: 'not started' };
  let calls = {};
  let mwi = { new: 0, old: 0 };
  let mwiSeen = false; // the first count is not news
  let muted = false;
  let tab = 'keypad';
  let number = '';
  let consultFor = null; // call being transferred after consultation
  let me = null; // my directory entry (agent status, DND)
  let dtmfSeq = ''; // a sequence of tones to send (1234#, "," pauses)
  let dir = [];
  let pendingIce = [];
  let remoteSet = false;
  let gen = 0; // numbers each phone.start; see current()
  let media = ''; // audio connection: new, connecting, connected, failed...
  let via = ''; // udp or tcp, once connected
  let sender = null; // the RTCRtpSender of the microphone
  let micGain = Number(pref('mic-gain', '1')) || 1; // 1 = the microphone as captured
  let offered = []; // the server addresses the browser may send audio to
  let audioCtx = null;
  let ringTimer = null;
  let ringbackTimer = null;
  let tick = null;
  let soundBlocked = false; // the browser refused to play the call's sound
  let checkOpen = false; // the audio check panel is shown
  let audioRep = null; // the latest audio check: { server, browser, problems }
  let checkTimer = null;
  let prevReceived = -1;

  const panel = () => $('#phone');
  let closing = null; // the closing animation's timer
  const isOpen = () => !panel().classList.contains('hidden') && !closing;
  const reduced = () => window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  function setDot() {
    const color = !started ? '' : reg.ok && media !== 'failed' ? (me && me.dnd ? 'red' : me && me.agent_status === 'On Break' ? 'amber' : 'green') : 'red';
    const d = $('#phone-dot');
    if (d) d.className = 'fab-dot ' + color;
    const pr = $('#top-presence');
    if (pr) {
      pr.className = 'presence ' + color;
      pr.dataset.status = me && me.dnd ? 'dnd' : me && me.agent_status === 'On Break' ? 'break' : 'online';
    }
    const fab = $('#phone-btn');
    if (fab) {
      const ringing = Object.values(calls).some((c) => c.direction === 'in' && c.state === 'ringing');
      const open = isOpen();
      fab.classList.toggle('ringing', ringing);
      if (fab.dataset.icon !== (open ? 'x' : 'phone')) { // turns into the other icon (only when it changes)
        fab.dataset.icon = open ? 'x' : 'phone';
        fill(fab, icon(open ? 'x' : 'phone', 24, 'fab-ic'), d);
      }
      fab.setAttribute('aria-label', open ? 'Close the dialer' : 'Open the dialer');
    }
  }

  let inited = false;
  function init() {
    if (inited) { autoStart(); render(); return; } // signed in again in the same page
    inited = true;
    $('#phone-btn').onclick = () => toggle();
    // Messages of an earlier start (a reconnect, or a start queued while the
    // WebSocket was opening) carry an older gen and are ignored.
    const current = (m) => pc && (m.gen === undefined || m.gen === gen);
    on('phone.sdp', async (m) => {
      if (!current(m) || pc.signalingState !== 'have-local-offer') return;
      try {
        String(m.sdp.sdp || '').split(/\r?\n/).filter((l) => l.startsWith('a=candidate:')).forEach((l) => addOffered(l.slice(2)));
        await pc.setRemoteDescription(m.sdp);
        remoteSet = true;
        for (const c of pendingIce) await pc.addIceCandidate(c).catch(() => {});
        pendingIce = [];
      } catch (e) { fail(e); }
    });
    on('phone.ice', (m) => {
      if (!current(m)) return;
      addOffered(m.candidate && m.candidate.candidate);
      if (remoteSet) pc.addIceCandidate(m.candidate).catch(() => {}); else pendingIce.push(m.candidate);
    });
    // Call times come from the server's clock; the browser's may be off.
    const skew = (m) => { if (m.now) S.clockSkew = Date.now() / 1000 - m.now / 1000; };
    on('phone.reg', (m) => { skew(m); reg = m; setDot(); render(); });
    on('phone.call', (m) => { skew(m); onCall(m.call); });
    on('phone.mwi', (m) => {
      if (m.new > mwi.new && mwiSeen) {
        Notes.show({ key: 'vm', icon: 'voicemail', tone: 'amber', title: 'New voicemail', body: m.new + ' new message' + (m.new === 1 ? '' : 's'),
          click: () => { location.hash = '#/voicemail'; } });
      }
      mwiSeen = true;
      mwi = m;
      render();
    });
    on('phone.media', (m) => {
      if (!current(m) || m.state !== 'failed' || media === 'failed') return;
      media = 'failed';
      toast('The dialer could not connect its audio to the server: see the dialer for what to check', true);
      render();
    });
    on('ws.open', () => { if (started) connect(); });
    on('ws.close', () => { if (started) { reg = { ok: false, reason: 'reconnecting…' }; calls = {}; syncMic(); setDot(); render(); } });
    on('presence', () => { if (!panel().classList.contains('hidden')) loadDir(); });
    document.addEventListener('keydown', onKey);
    window.addEventListener('resize', () => { if (!panel().classList.contains('hidden')) fitBody(); });
    initDrag();
    document.addEventListener('click', (e) => { if (menuOpen && !e.target.closest('.ph-menu')) { menuOpen = false; render(); } });
    autoStart();
    render();
  }

  // The extension registers by itself after signing in, unless its user
  // turned the dialer off.
  function autoStart() {
    let want = true;
    try { want = localStorage.getItem('rq-phone') !== 'off'; } catch (e) { /* private mode */ }
    if (want) start(true);
  }

  // The dialer opens out of the round button and shrinks back into it.
  function toggle(force) {
    const p = panel();
    const show = force === undefined ? !isOpen() : force;
    menuOpen = false;
    if (show) {
      if (closing) { clearTimeout(closing); closing = null; p.classList.remove('ph-out'); }
      if (p.classList.contains('hidden')) {
        applyPos();
        towardButton();
        p.classList.remove('hidden', 'ph-in');
        if (!reduced()) {
          void p.offsetWidth;
          p.classList.add('ph-in');
          p.addEventListener('animationend', placed, { once: true }); // and again once it has opened
        }
      }
      loadDir();
      render();
    } else if (isOpen()) {
      if (reduced()) p.classList.add('hidden');
      else {
        towardButton();
        p.classList.add('ph-out');
        closing = setTimeout(() => { closing = null; p.classList.remove('ph-out', 'ph-in'); p.classList.add('hidden'); setDot(); placed(); }, 170);
      }
    }
    setDot();
    placed();
  }

  // ---- where the dialer is: dragged by its header, kept for next time
  // (as distances from the window's right and bottom edges); a double
  // click on the header puts it back in the corner.
  const CORNER = { right: 24, bottom: 96 };
  let pos = null; // null: the corner
  try { const v = JSON.parse(localStorage.getItem('rq-dialer-pos') || 'null'); if (v && Number.isFinite(v.right) && Number.isFinite(v.bottom)) pos = v; } catch (e) { /* private mode */ }
  const movable = () => window.innerWidth > 520 && document.documentElement.dir !== 'rtl';
  // The page area the dialer moves in: below the top bar, right of the menu
  // (both stay on top, their menus usable).
  function bounds() {
    const t = $('.top'), sd = $('.side');
    const top = t && t.getClientRects().length ? t.getBoundingClientRect().bottom : 0;
    const side = sd && sd.getClientRects().length && getComputedStyle(sd).position !== 'fixed' ? Math.max(0, sd.getBoundingClientRect().right) : 0;
    return { left: side + 8, top: top + 8 };
  }
  const size = () => { const p = panel(); return { w: p.offsetWidth || 340, h: p.offsetHeight || Math.min(640, window.innerHeight - 176) }; };

  // box is where the dialer is, or would be when opened (the live
  // transcript panel goes beside it).
  function box() {
    const p = panel();
    const W = window.innerWidth, H = window.innerHeight;
    if (p && !p.classList.contains('hidden')) {
      // Its layout box: while it opens or closes, the animation scales it,
      // and its measured rectangle is not where it stands.
      const cs = getComputedStyle(p), right = parseFloat(cs.right), bottom = parseFloat(cs.bottom);
      const w = p.offsetWidth, ht = p.offsetHeight;
      if (Number.isFinite(right) && Number.isFinite(bottom)) return { left: W - right - w, top: H - bottom - ht, right: W - right, bottom: H - bottom, width: w, height: ht };
      return p.getBoundingClientRect();
    }
    const { w, h: ht } = size();
    const at = movable() && pos ? pos : CORNER;
    return { left: W - at.right - w, top: H - at.bottom - ht, right: W - at.right, bottom: H - at.bottom, width: w, height: ht };
  }
  // Whole on the screen, in the page area.
  function clampPos(at) {
    const { w, h: ht } = size(), b = bounds();
    return { right: Math.max(8, Math.min(at.right, window.innerWidth - w - b.left)), bottom: Math.max(8, Math.min(at.bottom, window.innerHeight - ht - b.top)) };
  }
  function applyPos() {
    const p = panel();
    if (movable() && pos) {
      pos = clampPos(pos); // the window or the menu may have changed size
      p.style.right = pos.right + 'px';
      p.style.bottom = pos.bottom + 'px';
    } else { p.style.right = ''; p.style.bottom = ''; }
  }
  function savePos() { try { if (pos) localStorage.setItem('rq-dialer-pos', JSON.stringify(pos)); else localStorage.removeItem('rq-dialer-pos'); } catch (e) { /* private mode */ } }
  function towardButton() {
    const f = $('#phone-btn');
    if (!f || !f.getClientRects().length) return;
    const fr = f.getBoundingClientRect(), b = box();
    panel().style.transformOrigin = Math.round(fr.left + fr.width / 2 - b.left) + 'px ' + Math.round(fr.top + fr.height / 2 - b.top) + 'px';
  }
  function placed() {
    if (window.PhoneSide) PhoneSide.place();
    if (window.LiveTr) LiveTr.place();
  }

  function initDrag() {
    const p = panel();
    const onHead = (e) => e.target.closest('.ph-head') && !e.target.closest('button, input, select, a, label, .tgl, .ph-menu');
    p.addEventListener('pointerdown', (e) => {
      if (e.button !== 0 || !movable() || !onHead(e)) return;
      e.preventDefault(); // no text selection
      const r = p.getBoundingClientRect();
      const from = { x: e.clientX, y: e.clientY, right: window.innerWidth - r.right, bottom: window.innerHeight - r.bottom };
      let dragging = false; // a click (or a double click) does not move it
      const move = (ev) => {
        if (!dragging) {
          if (Math.abs(ev.clientX - from.x) + Math.abs(ev.clientY - from.y) < 4) return;
          dragging = true;
          p.setPointerCapture(e.pointerId);
          p.classList.add('dragging');
          document.body.classList.add('rq-dragging');
        }
        pos = clampPos({ right: from.right - (ev.clientX - from.x), bottom: from.bottom - (ev.clientY - from.y) });
        applyPos();
        placed();
      };
      const up = () => {
        p.removeEventListener('pointermove', move);
        p.removeEventListener('pointerup', up);
        p.removeEventListener('pointercancel', up);
        if (!dragging) return;
        p.classList.remove('dragging');
        document.body.classList.remove('rq-dragging');
        savePos();
      };
      p.addEventListener('pointermove', move);
      p.addEventListener('pointerup', up);
      p.addEventListener('pointercancel', up);
    });
    p.addEventListener('dblclick', (e) => { if (onHead(e)) { pos = null; savePos(); applyPos(); placed(); } });
    window.addEventListener('resize', () => { if (pos) pos = clampPos(pos); applyPos(); placed(); });
    applyPos();
  }

  async function start(auto) {
    if (started) return;
    try {
      // Ask for the microphone now, so a call can be answered at once
      // later; it is closed again until a call needs it.
      (await openMic()).getTracks().forEach((t) => t.stop());
    } catch (e) {
      if (!auto) toast('The dialer needs your microphone: ' + e.message + (location.protocol !== 'https:' ? ' (open the page with https)' : ''), true);
      return;
    }
    started = true;
    try { localStorage.setItem('rq-phone', 'on'); } catch (e) { /* private mode */ }
    if (!audioCtx) { try { audioCtx = new AudioContext(); } catch (e) { /* no tones */ } }
    if (window.Notification && Notification.permission === 'default') Notification.requestPermission().catch(() => {});
    reg = { ok: false, reason: 'connecting…' };
    setDot();
    if (WS.open) await connect(); // otherwise the ws.open handler connects
    render();
  }

  async function connect() {
    if (pc) pc.close();
    if (muted) { muted = false; if (stream) stream.getAudioTracks().forEach((t) => { t.enabled = true; }); }
    remoteSet = false;
    pendingIce = [];
    calls = {};
    answering = null;
    releaseMic('call');
    pc = new RTCPeerConnection({ bundlePolicy: 'max-bundle' });
    sender = pc.addTransceiver('audio', { direction: 'sendrecv' }).sender;
    if (stream) sender.replaceTrack(outTrack()).catch(() => {});
    pc.ontrack = (e) => {
      const a = $('#phone-audio');
      a.srcObject = e.streams[0] || new MediaStream([e.track]);
      applySpeaker();
      ensureSound();
    };
    const my = ++gen;
    const mine = pc;
    media = 'new';
    offered = [];
    pc.onconnectionstatechange = () => {
      if (pc !== mine) return;
      const was = media;
      media = mine.connectionState;
      if (media === 'connected') {
        diagNow().then((d) => { via = d.via.split(' ')[0]; render(); }).catch(() => {});
      }
      if (was !== media && (media === 'failed' || was === 'failed' || media === 'connected')) render();
    };
    pc.onicecandidate = (e) => { if (e.candidate && pc === mine) wsSend({ t: 'phone.ice', candidate: e.candidate.toJSON() }); };
    try {
      await mine.setLocalDescription(await mine.createOffer());
    } catch (e) {
      if (my === gen) fail(e);
      return;
    }
    if (my !== gen) return; // a newer connect() started meanwhile
    wsSend({ t: 'phone.start', sdp: mine.localDescription, gen: my });
  }

  // stop ends the dialer; byUser (the switch or the menu) also keeps it off
  // at the next sign-in, signing out does not.
  function stop(byUser) {
    if (byUser === true) { try { localStorage.setItem('rq-phone', 'off'); } catch (e) { /* private mode */ } }
    if (!started) return;
    started = false;
    wsSend({ t: 'phone.stop' });
    if (pc) pc.close();
    pc = null;
    micUsers.clear();
    answering = null;
    closeMic();
    sender = null;
    via = '';
    $('#phone-audio').srcObject = null;
    soundBlocked = false;
    calls = {};
    reg = { ok: false, reason: 'not started' };
    stopRing();
    setDot();
    render();
  }

  // ---- microphone, speaker and levels (remembered by this browser)
  function pref(k, d) { try { const v = localStorage.getItem('rq-' + k); return v === null ? d : v; } catch (e) { return d; } }
  function setPref(k, v) { try { localStorage.setItem('rq-' + k, String(v)); } catch (e) { /* private mode */ } }

  async function openMic() {
    const audio = { echoCancellation: true, noiseSuppression: true, autoGainControl: true };
    const id = pref('mic', '');
    if (id) audio.deviceId = { exact: id };
    try {
      return await navigator.mediaDevices.getUserMedia({ audio });
    } catch (e) {
      if (!id) throw e;
      delete audio.deviceId; // that microphone is gone: use the default one
      return navigator.mediaDevices.getUserMedia({ audio });
    }
  }

  // needMic opens the microphone for who (a call, a meter) and sends it;
  // releaseMic closes it once nobody needs it.
  function needMic(who) {
    micUsers.add(who);
    if (stream || !started) return Promise.resolve();
    if (!micOpening) {
      micOpening = openMic().then((s) => {
        micOpening = null;
        if (!micUsers.size || !started || stream) { s.getTracks().forEach((t) => t.stop()); return; }
        s.getAudioTracks().forEach((t) => { t.enabled = !muted; });
        stream = s;
        applyMic();
        render();
      }, (e) => {
        micOpening = null;
        micUsers.clear();
        toast('The dialer needs your microphone: ' + e.message, true);
      });
    }
    return micOpening;
  }
  function releaseMic(who) {
    micUsers.delete(who);
    if (!micUsers.size) closeMic();
  }
  function closeMic() {
    if (!stream) return;
    if (sender) sender.replaceTrack(null).catch(() => {});
    stream.getTracks().forEach((t) => t.stop());
    stream = null;
    dropGraph();
  }
  function dropGraph() {
    if (micGraph) { micGraph.forEach((n) => n.disconnect()); micGraph = null; }
  }
  // syncMic keeps the microphone open while a call needs it: one being
  // dialled, answered or on hold (not one still ringing here).
  function syncMic() {
    if (answering && !(calls[answering] && calls[answering].state === 'ringing')) answering = null;
    const busy = Object.values(calls).some((c) => c.direction === 'out' || c.state !== 'ringing' || c.id === answering);
    if (busy) needMic('call'); else releaseMic('call');
  }

  // outTrack is what the dialer sends: the microphone, through a gain node
  // when a boost is set (and Web Audio is running; otherwise as captured).
  function outTrack() {
    dropGraph();
    const raw = stream.getAudioTracks()[0];
    if (micGain === 1 || !audioCtx || audioCtx.state !== 'running') return raw;
    const src = audioCtx.createMediaStreamSource(stream);
    const g = audioCtx.createGain();
    g.gain.value = micGain;
    const dst = audioCtx.createMediaStreamDestination();
    src.connect(g).connect(dst);
    micGraph = [src, g];
    return dst.stream.getAudioTracks()[0];
  }
  function applyMic() {
    if (sender && stream) sender.replaceTrack(outTrack()).catch(() => {});
  }
  async function changeMic(id) {
    setPref('mic', id);
    if (!started || !stream) return; // used from the next call on
    try {
      const next = await openMic();
      next.getAudioTracks().forEach((t) => { t.enabled = !muted; });
      const old = stream;
      stream = next;
      applyMic();
      old.getTracks().forEach((t) => t.stop());
      render();
    } catch (e) { fail(e); }
  }
  function applySpeaker() {
    const a = $('#phone-audio');
    a.volume = Math.min(1, Math.max(0, Number(pref('volume', '1'))));
    const id = pref('speaker', '');
    if (a.setSinkId) a.setSinkId(id).catch(() => {});
    if (audioCtx && audioCtx.setSinkId) audioCtx.setSinkId(id).catch(() => {});
  }
  // Browsers play sound only after the person has clicked or typed in the
  // page. A dialer started on page load (or a reload) may therefore be
  // silent: try again on every click and key press, and when calls change.
  function ensureSound() {
    if (audioCtx && audioCtx.state === 'suspended') audioCtx.resume().then(() => { if (micGain !== 1) applyMic(); }).catch(() => {});
    const a = $('#phone-audio');
    if (!a || !a.srcObject) return;
    const ok = () => { if (soundBlocked) { soundBlocked = false; render(); } };
    if (!a.paused) { ok(); return; }
    a.play().then(ok).catch(() => { if (!soundBlocked) { soundBlocked = true; render(); } });
  }
  document.addEventListener('pointerdown', ensureSound, true);
  document.addEventListener('keydown', ensureSound, true);

  async function diagNow() {
    const out = { state: pc ? pc.connectionState : 'none', offered: offered.slice(), via: '' };
    if (pc) {
      const st = await pc.getStats();
      st.forEach((r) => {
        if (r.type === 'candidate-pair' && r.nominated && r.state === 'succeeded') {
          const l = st.get(r.localCandidateId), rc = st.get(r.remoteCandidateId);
          if (l && rc) out.via = (l.protocol || '') + ' → ' + rc.address + ':' + rc.port;
        }
      });
    }
    return out;
  }

  // ---- tones (Web Audio)
  function tone(freqs, ms) {
    if (!audioCtx) return;
    if (audioCtx.state === 'suspended') audioCtx.resume().catch(() => {});
    const g = audioCtx.createGain();
    g.gain.value = 0.08;
    g.connect(audioCtx.destination);
    for (const f of freqs) {
      const o = audioCtx.createOscillator();
      o.frequency.value = f;
      o.connect(g);
      o.start();
      o.stop(audioCtx.currentTime + ms / 1000);
    }
  }
  function startRing() {
    if (ringTimer) return;
    const ring = () => { tone([440, 480], 1000); setTimeout(() => tone([440, 480], 1000), 1200); };
    ring();
    ringTimer = setInterval(ring, 4000);
  }
  function stopRing() { clearInterval(ringTimer); ringTimer = null; }
  function updateRingback() {
    const ringing = Object.values(calls).some((c) => c.direction === 'out' && c.state === 'ringing');
    if (ringing && !ringbackTimer) { tone([440, 480], 2000); ringbackTimer = setInterval(() => tone([440, 480], 2000), 6000); }
    if (!ringing && ringbackTimer) { clearInterval(ringbackTimer); ringbackTimer = null; }
  }
  const dtmfFreq = { 1: [697, 1209], 2: [697, 1336], 3: [697, 1477], 4: [770, 1209], 5: [770, 1336], 6: [770, 1477], 7: [852, 1209], 8: [852, 1336], 9: [852, 1477], '*': [941, 1209], 0: [941, 1336], '#': [941, 1477] };

  function onCall(c) {
    const prev = calls[c.id];
    if (c.state === 'ended') {
      delete calls[c.id];
      if (consultFor === c.id) consultFor = null;
      Notes.close('call-' + c.id);
      if (c.direction === 'in' && (c.cause === 'missed') && prev) {
        Notes.show({ key: 'missed-' + c.id, icon: 'incoming', tone: 'red', title: 'Missed call', body: (c.name && c.name !== c.number ? c.name + ' · ' : '') + c.number, ms: 10000,
          actions: [{ label: 'Call back', kind: 'primary', fn: () => { toggle(true); dial(c.number); } }] });
      }
      else if (c.cause && c.cause !== 'hung up' && c.cause !== 'the other side hung up' && c.cause !== 'declined') toast('Call ' + c.number + ': ' + c.cause);
    } else {
      calls[c.id] = c;
    }
    if (c.direction === 'in' && c.state === 'ringing' && !prev) {
      toggle(true);
      if (c.auto_answer && !Object.values(calls).some((x) => x.id !== c.id && x.state === 'answered' && !x.held)) {
        // Intercom or supervisor monitoring: answer at once (with a short beep).
        tone([880], 150);
        answering = c.id;
        send('phone.answer', { call: c.id });
      } else {
        startRing();
        Notes.show({ key: 'call-' + c.id, icon: 'incoming', tone: 'green', title: 'Incoming call', body: (c.name && c.name !== c.number ? c.name + ' · ' : '') + c.number, sticky: true,
          actions: [{ label: 'Decline', kind: 'decline', fn: () => send('phone.hangup', { call: c.id }) },
            { label: 'Answer', kind: 'answer', fn: () => { toggle(true); answer(c); } }] });
      }
    }
    if (c.state !== 'ringing') Notes.close('call-' + c.id);
    if (!Object.values(calls).some((x) => x.direction === 'in' && x.state === 'ringing')) stopRing();
    updateRingback();
    if (c.state === 'answered' || c.state === 'early') ensureSound();
    if (!Object.keys(calls).length && muted) setMute(false); // the next call starts unmuted
    syncMic();
    watchAudio();
    render();
  }

  // ---- audio check: where does the sound stop?
  // The server counts the audio of the call in each direction (browser ->
  // server -> other side and back); the browser adds what it sent, received
  // and plays.
  function watchAudio() {
    const live = Object.values(calls).some((c) => c.state === 'answered' && !c.held);
    if (live && !checkTimer) { prevReceived = -1; checkTimer = setInterval(pollAudio, 2000); pollAudio(); }
    if (!live && checkTimer) { clearInterval(checkTimer); checkTimer = null; audioRep = null; checkOpen = false; }
  }
  async function browserStats() {
    const a = $('#phone-audio');
    const out = { sent: 0, received: 0, micLevel: null, inLevel: null, rtt: null, jitterMs: null, lost: 0, playing: !!(a && a.srcObject && !a.paused), volume: a ? a.volume : 1 };
    if (!pc) return out;
    const st = await pc.getStats();
    st.forEach((r) => {
      if (r.type === 'outbound-rtp' && r.kind === 'audio') out.sent = r.packetsSent || 0;
      if (r.type === 'media-source' && r.kind === 'audio' && r.audioLevel !== undefined) out.micLevel = r.audioLevel;
      if (r.type === 'inbound-rtp' && r.kind === 'audio') {
        out.received = r.packetsReceived || 0;
        out.lost = r.packetsLost || 0;
        if (r.audioLevel !== undefined) out.inLevel = r.audioLevel;
        if (r.jitterBufferEmittedCount) out.jitterMs = Math.round((r.jitterBufferDelay / r.jitterBufferEmittedCount) * 1000);
      }
      if (r.type === 'candidate-pair' && r.nominated && r.state === 'succeeded' && r.currentRoundTripTime !== undefined) out.rtt = Math.round(r.currentRoundTripTime * 1000);
    });
    return out;
  }
  async function pollAudio() {
    let server;
    try { server = await wsSend({ t: 'phone.stats' }, true); } catch (e) { return; }
    if (!checkTimer) return;
    const browser = await browserStats().catch(() => null);
    if (!browser) return;
    browser.arriving = prevReceived < 0 ? null : browser.received > prevReceived;
    prevReceived = browser.received;
    const problems = (server.audio && server.audio.problems) ? server.audio.problems.slice() : [];
    if (soundBlocked || (!browser.playing && server.audio && server.audio.seconds >= 2)) {
      problems.unshift('The browser is not playing the call\'s sound. Click "Turn on sound" below.');
    } else if (browser.arriving === false && server.audio && server.audio.to_browser.packets > 0) {
      problems.push('The server sends the other side\'s sound, but it does not reach this browser (network between you and the server).');
    } else if (browser.volume === 0) {
      problems.push('The speaker volume is 0: raise it under Settings.');
    }
    const before = audioRep ? audioRep.problems.join('|') : '';
    audioRep = { server, browser, problems };
    if (before !== problems.join('|')) render();
    else { const old = document.getElementById('ph-check'); if (old) old.replaceWith(audioCheck()); }
  }
  // dB (server, dBFS) or linear (browser, 0..1) to a 0..100 bar.
  const pctDB = (db) => Math.max(0, Math.min(100, ((db + 70) / 55) * 100));
  const pctLin = (v) => (v > 0 ? pctDB(20 * Math.log10(v)) : 0);
  function audioCheck() {
    const r = audioRep;
    const box = h('div', { class: 'ph-check', id: 'ph-check' },
      h('div', { class: 'ph-check-head' }, h('b', null, 'Audio check'), h('button', { class: 'icon-btn', type: 'button', 'aria-label': 'Close the audio check', onclick: () => { checkOpen = false; render(); } }, icon('x', 14))));
    if (!r || !r.server.audio) { box.append(h('div', { class: 'muted small' }, 'Measuring… (a few seconds)')); return box; }
    const a = r.server.audio, b = r.browser;
    const line = (label, color, state, pct) => h('div', { class: 'ph-check-row' },
      h('span', { class: 'dot ' + color }), h('span', { class: 'grow' }, label),
      pct !== undefined ? h('span', { class: 'bar ph-meter mini', title: 'Level' }, h('i', { style: 'width:' + pct.toFixed(0) + '%' })) : null,
      h('span', { class: 'ph-check-state' }, state));
    const flowing = (f) => f.packets >= Math.max(1, a.seconds * 10);
    const st = (f) => (!flowing(f) ? ['red', 'Nothing arrives'] : f.loudest < -60 ? ['amber', 'Only silence'] : f.recent < -60 ? ['amber', 'Quiet'] : ['green', 'OK']);
    const [c1, s1] = muted ? ['amber', 'Muted'] : st(a.from_browser);
    const [c3, s3] = st(a.from_call);
    const c2 = flowing(a.to_call) ? 'green' : 'red';
    const playing = b.playing && !soundBlocked;
    const c4 = !flowing(a.to_browser) || b.arriving === false ? 'red' : playing ? 'green' : 'red';
    box.append(
      line('Your mic → server', c1, s1, pctDB(a.from_browser.level)),
      line('Server → other side', c2, c2 === 'green' ? 'OK' : 'Not sent'),
      line('Other side → server', c3, s3, pctDB(a.from_call.level)),
      line('Server → your speaker', c4, !playing ? 'Not playing' : c4 === 'green' ? 'OK' : 'Nothing arrives', b.inLevel !== null ? pctLin(b.inLevel) : undefined));
    box.append(h('div', { class: 'muted small' }, [
      'Via ' + (via || r.server.via || '?').toUpperCase(),
      b.rtt !== null ? 'round trip ' + b.rtt + ' ms' : '',
      b.jitterMs !== null ? 'buffer ' + b.jitterMs + ' ms' : '',
      'lost ' + b.lost,
      r.server.rtp && r.server.rtp.codec ? r.server.rtp.codec : '',
    ].filter(Boolean).join(' · ')));
    return box;
  }

  async function cmd(t, extra) {
    try { return await wsSend(Object.assign({ t }, extra), true); } catch (e) { fail(e); return null; }
  }
  function send(t, extra) { wsSend(Object.assign({ t }, extra)); }

  function dial(num) {
    num = String(num || number).replace(/[^0-9*#+]/g, '');
    if (!num) return;
    if (!started) { toast('Start the dialer first', true); return; }
    // Starting a second call puts the current one on hold.
    for (const c of Object.values(calls)) if (c.state === 'answered' && !c.held) send('phone.hold', { call: c.id, on: true });
    needMic('call'); // opening while it rings
    send('phone.dial', { number: num });
    number = '';
    dialMode = false;
    tab = 'keypad';
    render();
  }

  async function loadDir() {
    if (!S.domain) return;
    try {
      dir = await api('GET', dpath('directory'));
      me = dir.find((d) => d.extension === S.me.extension) || null;
      render();
    } catch (e) { /* ignore */ }
  }

  async function setStatus(status, aux) {
    if (!me || !me.agent) return;
    try {
      await api('POST', 'agents/' + me.agent + '/status', { status, aux: aux || '' });
      toast(aux || status);
      loadDir();
    } catch (e) { fail(e); }
  }

  // sendTones sends a sequence of tones; "," waits a second.
  async function sendTones(c, seq) {
    for (const ch of seq) {
      if (!calls[c.id]) return;
      if (ch === ',') { await new Promise((r) => setTimeout(r, 1000)); continue; }
      if (!/[0-9*#A-D]/i.test(ch)) continue;
      send('phone.dtmf', { call: c.id, digits: ch.toUpperCase() });
      if (dtmfFreq[ch]) tone(dtmfFreq[ch], 100);
      dtmfSent += ch;
      render();
      await new Promise((r) => setTimeout(r, 250));
    }
  }

  async function setDND(onOff) {
    try { await api('POST', 'dnd', { on: onOff }); toast(onOff ? 'Do not disturb: on' : 'Do not disturb: off'); loadDir(); } catch (e) { fail(e); }
  }

  // addOffered notes a server candidate ("candidate:… udp … 1.2.3.4 8443 typ host")
  // as "UDP 1.2.3.4:8443" for the no-audio message.
  function addOffered(c) {
    const f = String(c || '').split(' ');
    if (f.length < 8 || f[6] !== 'typ' || /^127\./.test(f[4])) return;
    const a = f[2].toUpperCase() + ' ' + f[4] + ':' + f[5];
    if (!offered.includes(a)) offered.push(a);
  }

  const udpPort = () => (offered.find((x) => x.startsWith('UDP')) || 'UDP 8443').replace(/^.*:/, '');

  // Shown while the audio connection to the server has failed.
  function audioWarning() {
    if (!started || media !== 'failed') return null;
    return h('div', { class: 'ph-warn', role: 'alert' },
      h('b', null, 'No audio connection to the server'),
      h('p', null, 'Calls connect, but nobody hears anything. Your browser could not reach ',
        offered.length ? offered.join(', ') : 'the server', '.'),
      h('p', null, 'Ask your administrator to open UDP port ', h('b', null, udpPort()), ' in the server firewall (',
        h('code', null, 'sudo ufw allow ' + udpPort() + '/udp'), ') and in the cloud provider\'s firewall. ',
        offered.some((x) => x.startsWith('TCP')) ? 'The web port over TCP did not work either.' : 'Some office networks block UDP.'),
      h('button', { class: 'btn small', onclick: () => {
        if (Object.keys(calls).length) { toast('Hang up first: reconnecting ends the calls of this dialer', true); return; }
        connect();
        render();
      } }, icon('power', 14), 'Try again'));
  }

  // ---- rendering
  const KEYS = [['1', ''], ['2', 'ABC'], ['3', 'DEF'], ['4', 'GHI'], ['5', 'JKL'], ['6', 'MNO'], ['7', 'PQRS'], ['8', 'TUV'], ['9', 'WXYZ'], ['*', ''], ['0', '+'], ['#', '']];
  let dialMode = false; // the keypad is shown although there are calls (new call, consult)
  let dtmfPad = false; // keypad over the call screen (sends tones)
  let dtmfSent = '';
  let menuOpen = false;

  const callList = () => Object.values(calls).sort((a, b) => a.started - b.started);
  // The call the screen is about: a ringing incoming call first, then the live one.
  function focusCall(list) {
    return list.find((c) => c.direction === 'in' && c.state === 'ringing') ||
      list.find((c) => c.state === 'answered' && !c.held) || list[list.length - 1] || null;
  }
  function callLabel(c) {
    return { dialing: 'Calling…', ringing: c.direction === 'in' ? 'Incoming call' : 'Ringing…', early: 'Ringing…',
      answered: c.held ? 'On hold' : (c.remote_hold ? 'Held by the other side' : 'Connected') }[c.state] || c.state;
  }
  // personName: an extension's name, or the address-book name of a number.
  const bookNames = {}; // number -> name ('' = looked up, not found)
  function personName(num) {
    const d = dir.find((x) => x.extension === num);
    if (d) return d.name && d.name !== num ? d.name : '';
    if (bookNames[num] === undefined && num && S.domain && S.me.database) {
      bookNames[num] = '';
      api('GET', dpath('contacts/lookup?number=' + encodeURIComponent(num))).then((r) => {
        if (r.name) { bookNames[num] = r.name + (r.company ? ' · ' + r.company : ''); render(); }
      }).catch(() => {});
    }
    return bookNames[num] || '';
  }
  function presenceOf(d) {
    if (d.on_call) return ['amber', 'On a call'];
    if (d.dnd) return ['red', 'Do not disturb'];
    if (d.agent_status === 'On Break') return ['amber', 'Break' + (d.aux ? ': ' + d.aux : '')];
    return (d.phone || d.web) ? ['green', 'Available'] : ['', 'Offline'];
  }
  const avatarOf = (name, num, color) => h('span', { class: 'avatar' }, initials(name || num), color !== undefined ? h('i', { class: 'presence ' + color }) : null);
  const ctrl = (ic, label, fn, on, disabled) => h('button', { class: 'ph-ctrl' + (on ? ' on' : ''), type: 'button', onclick: fn, disabled: !!disabled },
    h('span', null, icon(ic, 22)), label);
  const roundBtn = (ic, cls, title, fn, disabled) => h('button', { class: 'ph-callbtn ' + (cls || ''), type: 'button', title, 'aria-label': title, onclick: fn, disabled: !!disabled }, icon(ic, 26));

  function setMute(on) {
    muted = on;
    if (stream) stream.getAudioTracks().forEach((t) => { t.enabled = !muted; });
    send('phone.mute', { on: muted });
    render();
  }
  // resume takes a held call back, holding the one that is live.
  function resume(c) {
    for (const x of Object.values(calls)) if (x.id !== c.id && x.state === 'answered' && !x.held) send('phone.hold', { call: x.id, on: true });
    send('phone.hold', { call: c.id, on: false });
  }
  function answer(c) {
    answering = c.id;
    needMic('call');
    send('phone.answer', { call: c.id });
    dialMode = false;
  }

  function myStatus() {
    if (!started) return ['', 'Off'];
    if (!reg.ok) return ['red', reg.reason || 'Not registered'];
    if (media === 'failed') return ['red', 'No audio'];
    if (me && me.dnd) return ['red', 'Do not disturb'];
    if (me && me.agent) {
      if (me.agent_status === 'On Break') return ['amber', me.aux || 'Break'];
      if (me.agent_status === 'Logged Out') return ['green', 'Logged out'];
      return ['green', 'Available'];
    }
    return ['green', 'Ready'];
  }

  function statusMenu() {
    const pick = (fn) => () => { menuOpen = false; fn(); render(); };
    const items = [];
    if (me && me.agent) {
      items.push(h('div', { class: 'group-label' }, 'Your call-centre status'));
      const cur = me.agent_status === 'On Break' ? 'aux:' + me.aux : me.agent_status;
      items.push(h('button', { class: cur === 'Available' ? 'on' : '', onclick: pick(() => setStatus('Available')) }, h('span', { class: 'dot green' }), 'Available'));
      for (const x of (S.meta ? S.meta.auxcodes : [])) {
        items.push(h('button', { class: cur === 'aux:' + x.name ? 'on' : '', onclick: pick(() => setStatus('On Break', x.name)) }, h('span', { class: 'dot amber' }), x.name));
      }
      items.push(h('button', { class: cur === 'Logged Out' ? 'on' : '', onclick: pick(() => setStatus('Logged Out')) }, h('span', { class: 'dot' }), 'Logged out'));
    }
    if (me) {
      items.push(h('div', { class: 'group-label' }, 'Calls to me'));
      items.push(h('button', { class: me.dnd ? 'on' : '', onclick: pick(() => setDND(!me.dnd)) }, icon(me.dnd ? 'belloff' : 'bell', 16), me.dnd ? 'Do not disturb is on (turn off)' : 'Do not disturb'));
    }
    items.push(h('div', { class: 'group-label' }, 'Dialer'));
    items.push(started ? h('button', { onclick: pick(() => stop(true)) }, icon('power', 16), 'Stop the dialer') : h('button', { onclick: pick(() => start(false)) }, icon('power', 16), 'Start the dialer'));
    return h('div', { class: 'ph-menu', role: 'menu' }, items);
  }

  function header() {
    const [color, text] = myStatus();
    const sw = h('input', { type: 'checkbox', checked: started, 'aria-label': 'Dialer on or off' });
    sw.addEventListener('change', () => { if (sw.checked) start(false); else stop(true); });
    return h('div', { class: 'ph-head', title: movable() ? 'Drag to move the dialer · double-click to put it back' : null },
      h('span', { class: 'ph-grip', 'aria-hidden': 'true' }),
      h('span', { class: 'logo' }),
      h('span', { class: 'tgl', title: started ? 'The dialer is on' : 'Turn the dialer on' }, sw, h('span')),
      h('span', { class: 'spacer' }),
      h('button', { class: 'ph-status', type: 'button', title: 'Your status', onclick: (e) => { e.stopPropagation(); menuOpen = !menuOpen; render(); } },
        h('span', { class: 'dot ' + color }), h('span', { class: 'ellipsis' }, text + ' · Ext ' + S.me.extension), icon('down', 14)),
      h('button', { class: 'icon-btn', type: 'button', title: 'Close', 'aria-label': 'Close the dialer', onclick: () => toggle(false) }, icon('x', 18)));
  }

  function offScreen() {
    return h('div', { class: 'ph-off' },
      h('div', { class: 'ring-icon' }, h('span', null, icon('phone', 30))),
      h('h2', null, 'Your browser as your phone'),
      h('p', null, 'Calls to ', h('b', null, S.me.extension), ' ring here and on your other phones. A headset gives the best sound.'),
      h('button', { class: 'btn primary', onclick: () => start(false) }, icon('power', 16), 'Start the dialer'));
  }

  function callScreen(list) {
    const c = focusCall(list);
    const now = Date.now() / 1000 - S.clockSkew;
    const name = c.name && c.name !== c.number ? c.name : personName(c.number);
    const since = c.answered || c.started;
    const others = list.filter((x) => x.id !== c.id);
    let body;
    if (c.direction === 'in' && c.state === 'ringing') {
      body = h('div', { class: 'ph-answer' },
        roundBtn('hangup', 'red', 'Decline', () => send('phone.hangup', { call: c.id })),
        roundBtn('phone', '', 'Answer', () => answer(c)));
    } else if (c.state !== 'answered') {
      body = h('div', { class: 'ph-answer' }, roundBtn('hangup', 'red', 'Cancel', () => send('phone.hangup', { call: c.id })));
    } else if (dtmfPad) {
      // DTMF: tones to the other side (IVR menus, PINs); a whole sequence
      // can be sent at once ("," waits a second).
      const seq = h('input', { value: dtmfSeq, placeholder: 'Sequence, e.g. 1,,2345#', inputmode: 'tel', autocomplete: 'off', 'aria-label': 'Tones to send' });
      seq.addEventListener('input', () => { dtmfSeq = seq.value; });
      const go = () => { const v = seq.value.trim(); if (v) { dtmfSeq = ''; sendTones(c, v); } };
      seq.addEventListener('keydown', (e) => { e.stopPropagation(); if (e.key === 'Enter') go(); });
      body = [
        h('div', { class: 'ph-number' }, h('input', { value: dtmfSent, readonly: true, 'aria-label': 'Tones sent', placeholder: 'DTMF' })),
        keyGrid((k) => { send('phone.dtmf', { call: c.id, digits: k }); if (dtmfFreq[k]) tone(dtmfFreq[k], 120); dtmfSent += k; render(); }),
        h('div', { class: 'ph-dtmf-seq' }, seq, h('button', { type: 'button', class: 'btn small', onclick: go }, 'Send')),
        h('div', { class: 'ph-answer' }, roundBtn('hangup', 'red', 'Hang up', () => send('phone.hangup', { call: c.id }))),
        h('button', { class: 'link-btn', style: 'align-self:center', onclick: () => { dtmfPad = false; render(); } }, 'Hide keypad'),
      ];
    } else {
      const completing = consultFor && consultFor !== c.id && calls[consultFor];
      body = [
        h('div', { class: 'ph-ctrls' },
          ctrl(muted ? 'micoff' : 'mic', muted ? 'Unmute' : 'Mute', () => setMute(!muted), muted),
          ctrl('keypad', 'DTMF', () => { dtmfPad = true; dtmfSent = ''; render(); }),
          ctrl(c.held ? 'play' : 'pause', c.held ? 'Resume' : 'Hold', () => (c.held ? resume(c) : send('phone.hold', { call: c.id, on: true })), c.held),
          ctrl('transfer', 'Transfer', async () => {
            const n = prompt('Transfer this call to which number? (it is passed on right away)');
            if (n) { const r = await cmd('phone.transfer', { call: c.id, target: n.trim() }); if (r !== null) toast('Transferred'); }
          }),
          completing ? ctrl('transfer', 'Complete transfer', async () => {
            const r = await cmd('phone.attxfer', { call: consultFor, target: c.id });
            if (r !== null) { toast('Transferred'); consultFor = null; }
          }, true) : ctrl('consult', 'Consult', () => {
            consultFor = c.id;
            if (!c.held) send('phone.hold', { call: c.id, on: true });
            dialMode = true; tab = 'keypad';
            toast('Dial the person to talk to first, then press Complete transfer');
            render();
          }),
          ctrl('plus', 'New call', () => { dialMode = true; tab = 'keypad'; render(); })),
        h('div', { class: 'ph-answer' }, roundBtn('hangup', 'red', 'Hang up', () => send('phone.hangup', { call: c.id }))),
        h('button', { class: 'link-btn ph-check-link', type: 'button', onclick: () => { checkOpen = !checkOpen; render(); } }, icon('activity', 14), checkOpen ? 'Hide the audio check' : 'Audio check'),
      ];
    }
    const answered = c.state === 'answered';
    const problem = answered && audioRep && audioRep.problems.length ? audioRep.problems[0] : '';
    return h('div', { class: 'ph-call' },
      others.length ? h('div', { class: 'ph-others' }, others.map((o) => h('div', { class: 'ph-other' },
        h('span', { class: 'dot ' + (o.state === 'answered' ? (o.held ? 'amber' : 'green') : 'blue') }),
        h('div', { class: 'grow ellipsis' }, (o.name && o.name !== o.number ? o.name + ' ' : personName(o.number) + ' ') + o.number, h('div', { class: 'muted small' }, callLabel(o))),
        o.direction === 'in' && o.state === 'ringing' ? h('button', { class: 'btn small primary', onclick: () => answer(o) }, 'Answer') : null,
        o.state === 'answered' && o.held ? h('button', { class: 'btn small', onclick: () => resume(o) }, 'Resume') : null,
        h('button', { class: 'icon-btn', title: 'Hang up', 'aria-label': 'Hang up', onclick: () => send('phone.hangup', { call: o.id }) }, icon('hangup', 16))))) : null,
      h('div', { class: 'ph-avatar' }, initials(name || c.number)),
      h('div', { class: 'who-name' }, name || c.number),
      name ? h('div', { class: 'who-num' }, c.number) : null,
      h('div', { class: 'when' + (c.state === 'answered' && !c.held ? ' live' : '') },
        callLabel(c), c.state === 'answered' ? [' · ', h('span', { 'data-since': since }, fmtDur(now - since))] : null),
      consultFor === c.id ? h('div', { class: 'ph-note' }, 'Being transferred: dial the colleague, then Complete transfer.') : null,
      c.state === 'answered' && via === 'tcp' ? h('div', { class: 'ph-note' }, 'Audio goes over TCP because UDP port ' + udpPort() + ' is blocked; opening it reduces delay.') : null,
      answered && (soundBlocked || (audioRep && !audioRep.browser.playing)) ? h('button', { class: 'btn primary small ph-sound', type: 'button', onclick: ensureSound }, icon('volume', 15), 'Turn on sound') : null,
      problem && !checkOpen ? h('button', { class: 'ph-problem', type: 'button', onclick: () => { checkOpen = true; render(); } },
        h('b', null, 'Audio problem'), h('span', null, problem), h('u', null, 'Open the audio check')) : null,
      answered && checkOpen ? audioCheck() : null,
      answered && checkOpen && problem ? h('div', { class: 'ph-warn' }, audioRep.problems.map((x) => h('p', null, x))) : null,
      body);
  }

  function keyGrid(press) {
    return h('div', { class: 'ph-keys' }, KEYS.map(([k, sub]) => h('button', { class: 'ph-key', type: 'button', onclick: () => press(k) }, h('b', null, k), h('small', null, sub))));
  }

  function keypad() {
    const input = h('input', { value: number, placeholder: '', inputmode: 'tel', autocomplete: 'off', 'aria-label': 'Number to call' });
    input.addEventListener('input', () => { number = input.value; });
    input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { number = input.value; dial(); } });
    const press = (k) => { number += k; input.value = number; if (dtmfFreq[k]) tone(dtmfFreq[k], 80); };
    const list = callList();
    return [
      h('div', { class: 'ph-from', title: 'Your calls show this extension\'s caller ID' }, icon('phone', 13), 'From ', h('b', null, S.me.extension)),
      consultFor && calls[consultFor] ? h('div', { class: 'ph-note' }, 'Consult: dial the colleague. The caller waits on hold.') : null,
      h('div', { class: 'ph-number' }, input, h('button', { class: 'icon-btn', type: 'button', title: 'Delete', 'aria-label': 'Delete', onclick: () => { number = number.slice(0, -1); input.value = number; } }, icon('backspace', 22))),
      keyGrid(press),
      roundBtn('phone', '', 'Call', () => dial(), !reg.ok),
      list.length ? h('button', { class: 'link-btn', style: 'align-self:center;margin-top:10px', onclick: () => { dialMode = false; if (consultFor && calls[consultFor] && !list.some((c) => c.id !== consultFor)) consultFor = null; render(); } }, icon('left', 14), 'Back to the call') : null,
    ];
  }

  let peopleView = 'team'; // team (extensions) or book (address book)
  function contacts() {
    const q = h('input', { type: 'search', class: 'ph-search', placeholder: peopleView === 'team' ? 'Search people' : 'Search the address book', 'aria-label': 'Search' });
    const list = h('div', { class: 'ph-list' });
    const seg = h('div', { class: 'tabs ph-seg' }, [['team', 'Team'], ['book', 'Address book']].map(([k, l]) =>
      h('button', { class: peopleView === k ? 'on' : '', type: 'button', onclick: () => { peopleView = k; render(); } }, l)));
    const drawTeam = () => {
      const f = q.value.toLowerCase();
      const people = dir.filter((d) => d.extension !== S.me.extension && (d.extension + ' ' + d.name).toLowerCase().includes(f));
      fill(list, people.length ? people.map((d) => {
        const [color, st] = presenceOf(d);
        return h('div', { class: 'prow' }, avatarOf(d.name, d.extension, color),
          h('div', { class: 'grow' }, h('div', { class: 'ellipsis' }, (d.name && d.name !== d.extension ? d.name + ' · ' : '') + d.extension), h('div', { class: 'muted small' }, st)),
          h('button', { class: 'icon-btn', title: 'Call ' + d.extension, 'aria-label': 'Call ' + d.extension, onclick: () => dial(d.extension) }, icon('phone', 18)),
          h('a', { class: 'icon-btn', title: 'Chat', 'aria-label': 'Chat with ' + d.extension, href: '#/chat/' + encodeURIComponent(d.extension) }, icon('chat', 18)));
      }) : h('div', { class: 'muted small pad' }, 'Nobody found.'));
    };
    let timer;
    const drawBook = async () => {
      try {
        const r = await api('GET', dpath('contacts?limit=50&q=' + encodeURIComponent(q.value.trim())));
        fill(list, r.rows.length ? r.rows.map((c) => h('div', { class: 'prow' }, h('span', { class: 'avatar' }, initials(c.name)),
          h('div', { class: 'grow' }, h('div', { class: 'ellipsis' }, c.name), h('div', { class: 'muted small ellipsis' }, [c.company, c.phone || c.mobile].filter(Boolean).join(' · '))),
          c.phone ? h('button', { class: 'icon-btn', title: 'Call ' + c.phone, 'aria-label': 'Call ' + c.name, onclick: () => dial(c.phone) }, icon('phone', 18)) : null,
          c.mobile ? h('button', { class: 'icon-btn', title: 'Call mobile ' + c.mobile, 'aria-label': 'Call mobile of ' + c.name, onclick: () => dial(c.mobile) }, icon('outgoing', 18)) : null))
          : h('div', { class: 'muted small pad' }, q.value ? 'No contact matches.' : 'The address book is empty. Add contacts under Contact center → Contacts.'));
      } catch (e) { fill(list, h('div', { class: 'muted small pad' }, 'Could not load the address book.')); }
    };
    if (peopleView === 'team') {
      q.addEventListener('input', drawTeam);
      drawTeam();
    } else {
      fill(list, h('div', { class: 'muted small' }, 'Loading…'));
      q.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(drawBook, 250); });
      drawBook();
    }
    return [S.me.database ? seg : null, q, list];
  }

  function recent() {
    const list = h('div', { class: 'ph-list' }, h('div', { class: 'muted small' }, 'Loading…'));
    api('GET', dpath('cdr?from=' + daysAgo(30) + '&to=' + today() + '&limit=40')).then((res) => {
      fill(list, res.rows.length ? res.rows.map((r) => {
        const out = r.caller_id_number === S.me.extension;
        const other = out ? (r.caller_destination || r.destination_number) : r.caller_id_number;
        const name = out ? personName(other) : (r.caller_id_name && r.caller_id_name !== other ? r.caller_id_name : personName(other));
        const missed = r.status === 'missed' || r.status === 'failed' || r.status === 'busy';
        return h('div', { class: 'prow' },
          h('span', { class: 'call-dir ' + (out ? 'out' : 'in'), title: out ? 'Outgoing' : 'Incoming' }, icon(out ? 'outgoing' : 'incoming', 14)),
          h('div', { class: 'grow' }, h('div', { class: 'ellipsis' + (missed ? ' error-text' : ''), style: 'margin:0;min-height:0' }, (name ? name + ' · ' : '') + other),
            h('div', { class: 'muted small' }, fmtTime(r.start_stamp) + ' · ' + (r.status || '') + (Number(r.billsec) ? ' · ' + fmtDur(r.billsec) : ''))),
          h('button', { class: 'icon-btn', title: 'Call back', 'aria-label': 'Call ' + other, onclick: () => dial(other) }, icon('phone', 18)));
      }) : h('div', { class: 'muted small pad' }, 'No calls in the last 30 days.'));
    }).catch(() => fill(list, h('div', { class: 'muted small' }, 'Could not load the calls.')));
    return list;
  }

  function voicemail() {
    const list = h('div', { class: 'ph-list' }, h('div', { class: 'muted small' }, 'Loading…'));
    api('GET', dpath('voicemail-messages')).then((rows) => {
      fill(list, rows.length ? rows.map((r) => h('div', { class: 'prow vm-row' },
        h('div', { class: 'grow' },
          h('div', { class: 'ellipsis', style: 'margin:0;min-height:0' }, h('b', null, party(r.caller_id_name, r.caller_id_number)), ' ', r.status === 'saved' ? '' : badge('New', 'green')),
          h('div', { class: 'muted small' }, fmtShort(r.created) + ' · ' + fmtDur(r.length)),
          r.transcript_text ? h('button', { class: 'vm-snippet', type: 'button', title: 'Read the message', onclick: () => openVoicemailTranscript(r) }, r.transcript_text)
            : r.transcript === 'queued' || r.transcript === 'processing' ? h('div', { class: 'muted small' }, 'Transcribing…') : null),
        h('div', { class: 'vm-acts' }, vmIcons(r, () => { list.replaceWith(voicemail()); })))) :
        h('div', { class: 'muted small pad' }, 'No voicemail.'));
    }).catch(() => fill(list, h('div', { class: 'muted small' }, 'Could not load voicemail.')));
    return list;
  }

  // startMeter shows the microphone level while the bar is on screen (the
  // microphone is open meanwhile).
  function startMeter(bar) {
    if (!started || !audioCtx || audioCtx.state !== 'running' || panel().classList.contains('hidden')) return;
    const who = 'meter' + (++meterSeq);
    needMic(who).then(() => {
      if (!stream || !bar.isConnected || !bar.offsetParent) { releaseMic(who); return; }
      meter(bar, who);
    });
  }
  function meter(bar, who) {
    const src = audioCtx.createMediaStreamSource(stream);
    const an = audioCtx.createAnalyser();
    an.fftSize = 512;
    src.connect(an);
    const buf = new Float32Array(an.fftSize);
    const step = () => {
      if (!bar.isConnected || !bar.offsetParent) { src.disconnect(); releaseMic(who); return; }
      an.getFloatTimeDomainData(buf);
      let sum = 0;
      for (const v of buf) sum += v * v;
      const level = Math.min(100, Math.sqrt(sum / buf.length) * micGain * 400);
      bar.firstChild.style.width = level.toFixed(0) + '%';
      requestAnimationFrame(step);
    };
    requestAnimationFrame(step);
  }

  function settings() {
    const row = (label, ...right) => h('div', { class: 'ph-set-row' }, h('label', null, label), right);
    const sw = (on, fn, label) => {
      const i = h('input', { type: 'checkbox', checked: on, 'aria-label': label });
      i.addEventListener('change', () => fn(i.checked));
      return h('span', { class: 'tgl' }, i, h('span'));
    };
    const rows = [row('Dialer', sw(started, (on) => (on ? start(false) : stop(true)), 'Dialer on or off'))];
    if (me && me.agent) {
      const sel = h('select', { 'aria-label': 'Queue status' },
        h('option', { value: 'Available', selected: me.agent_status === 'Available' }, 'Available'),
        (S.meta ? S.meta.auxcodes : []).map((x) => h('option', { value: 'aux:' + x.name, selected: me.agent_status === 'On Break' && me.aux === x.name }, 'Break: ' + x.name)),
        h('option', { value: 'Logged Out', selected: me.agent_status === 'Logged Out' }, 'Logged out'));
      sel.addEventListener('change', () => { if (sel.value.startsWith('aux:')) setStatus('On Break', sel.value.slice(4)); else setStatus(sel.value); });
      rows.push(row('Queue status', sel));
    }
    if (me) rows.push(row('Do not disturb', sw(me.dnd, (on) => setDND(on), 'Do not disturb')));
    if (window.Notification) {
      rows.push(row('Call notifications', Notification.permission === 'granted' ? badge('On', 'green') :
        Notification.permission === 'denied' ? h('span', { class: 'muted small' }, 'Blocked in the browser') :
          h('button', { class: 'btn small', onclick: () => Notification.requestPermission().then(render).catch(() => {}) }, 'Allow')));
    }
    rows.push(h('div', { class: 'group-label' }, 'Audio'));
    const micSel = h('select', { 'aria-label': 'Microphone' }, h('option', { value: '' }, 'Default'));
    const spkSel = h('select', { 'aria-label': 'Speaker' }, h('option', { value: '' }, 'Default'));
    if (navigator.mediaDevices && navigator.mediaDevices.enumerateDevices) {
      navigator.mediaDevices.enumerateDevices().then((list) => {
        for (const d of list) {
          if (!d.deviceId || d.deviceId === 'default' || d.deviceId === 'communications') continue;
          const o = h('option', { value: d.deviceId }, d.label || (d.kind === 'audioinput' ? 'Microphone' : 'Speaker'));
          if (d.kind === 'audioinput') micSel.append(o);
          if (d.kind === 'audiooutput') spkSel.append(o);
        }
        micSel.value = pref('mic', '');
        spkSel.value = pref('speaker', '');
        if (micSel.value !== pref('mic', '')) micSel.value = '';
        if (spkSel.value !== pref('speaker', '')) spkSel.value = '';
      }).catch(() => {});
    }
    micSel.addEventListener('change', () => changeMic(micSel.value));
    spkSel.addEventListener('change', () => { setPref('speaker', spkSel.value); applySpeaker(); });
    const hasSink = !!$('#phone-audio').setSinkId;
    const vol = h('input', { type: 'range', min: '0', max: '1', step: '0.05', value: pref('volume', '1'), 'aria-label': 'Speaker volume' });
    vol.addEventListener('input', () => { setPref('volume', vol.value); applySpeaker(); });
    const boostLbl = h('span', { class: 'muted small' }, Math.round(micGain * 100) + '%');
    const boost = h('input', { type: 'range', min: '0.5', max: '3', step: '0.25', value: String(micGain), 'aria-label': 'Microphone boost' });
    boost.addEventListener('input', () => { boostLbl.textContent = Math.round(Number(boost.value) * 100) + '%'; });
    boost.addEventListener('change', () => { micGain = Number(boost.value) || 1; setPref('mic-gain', micGain); applyMic(); });
    const meter = h('div', { class: 'bar ph-meter', 'aria-hidden': 'true' }, h('i', { style: 'width:0%' }));
    rows.push(row('Microphone', micSel));
    rows.push(row('Microphone boost', boost, boostLbl));
    rows.push(row('Your voice level', meter));
    if (hasSink) rows.push(row('Speaker', spkSel));
    rows.push(row('Speaker volume', vol));
    rows.push(row('Speaker test', h('button', { class: 'btn small', onclick: () => { if (!audioCtx) { try { audioCtx = new AudioContext(); } catch (e) { return; } } tone([440, 480], 1000); } }, icon('play', 14), 'Play a tone')));
    if (started) {
      rows.push(row('Audio path', media === 'connected' ? (via === 'tcp' ? badge('TCP (UDP blocked: more delay)', 'amber') : badge((via || 'UDP').toUpperCase() + ' (best)', 'green')) :
        media === 'failed' ? badge('Not connected', 'red') : badge('Connecting…')));
    }
    startMeter(meter);
    rows.push(h('p', { class: 'muted small' }, 'Signed in as ', h('b', null, S.me.extension), S.me.name && S.me.name !== S.me.extension ? ' (' + S.me.name + ')' : '', '. Calls to your extension ring here and on your other phones.'));
    return h('div', { class: 'ph-set' }, rows);
  }

  function tabs() {
    const items = [['recent', 'Recents', 'history'], ['voicemail', 'Voicemail', 'voicemail'], ['keypad', 'Keypad', 'keypad'], ['contacts', 'Contacts', 'contacts'], ['settings', 'Settings', 'settings']];
    return h('div', { class: 'ph-tabs', role: 'tablist' }, items.map(([k, label, ic]) => h('button', { class: tab === k ? 'on' : '', type: 'button', role: 'tab', 'aria-selected': String(tab === k),
      onclick: () => { tab = k; dialMode = k === 'keypad' && Object.keys(calls).length > 0 ? dialMode : false; menuOpen = false; render(); } },
    icon(ic, 20), label, k === 'voicemail' && mwi.new ? h('span', { class: 'tab-badge' }, String(mwi.new)) : null)));
  }

  function render() {
    setDot();
    const p = panel();
    if (!p || p.classList.contains('hidden') || !S.me) return; // signed out
    const list = callList();
    if (!list.length) { dialMode = false; dtmfPad = false; }
    let content;
    if (!started && tab !== 'settings') content = offScreen();
    else if (tab === 'keypad') content = list.length && !dialMode ? callScreen(list) : keypad();
    else content = { contacts, recent, voicemail, settings }[tab]();
    // The call screen, the keypad and the start screen always fit the
    // dialer (they get smaller until they do); lists scroll.
    const fits = !started || tab === 'keypad';
    fill(p, header(), menuOpen ? statusMenu() : null, h('div', { class: 'ph-body' + (fits ? ' fit' : '') }, audioWarning(), content), tabs());
    if (fits) fitBody();
    if (window.PhoneSide) PhoneSide.calls(list);
  }

  function fitBody() {
    const b = panel().querySelector('.ph-body.fit');
    if (!b || !b.clientHeight) return; // hidden: measured when shown
    for (let level = 0; level <= 6; level++) {
      b.dataset.fit = String(level);
      if (b.scrollHeight <= b.clientHeight + 1) return;
    }
  }

  // Timers of calls in progress tick without redrawing the dialer.
  function liveTick() {
    const now = Date.now() / 1000 - S.clockSkew;
    $$('#phone [data-since]').forEach((el) => { el.textContent = fmtDur(now - Number(el.dataset.since)); });
  }

  // Typing digits while the dialer is open dials them (or sends tones in a call).
  function onKey(e) {
    const p = panel();
    if (!p || p.classList.contains('hidden') || !started || e.ctrlKey || e.metaKey || e.altKey) return;
    const t = document.activeElement;
    if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
    if (!/^[0-9*#]$/.test(e.key) && e.key !== 'Backspace' && e.key !== 'Enter') return;
    const live = Object.values(calls).find((c) => c.state === 'answered' && !c.held);
    if (tab === 'keypad' && live && !dialMode) {
      if (/^[0-9*#]$/.test(e.key)) { dtmfPad = true; send('phone.dtmf', { call: live.id, digits: e.key }); dtmfSent += e.key; if (dtmfFreq[e.key]) tone(dtmfFreq[e.key], 120); render(); }
      return;
    }
    if (tab !== 'keypad') return;
    e.preventDefault();
    if (e.key === 'Enter') dial();
    else if (e.key === 'Backspace') number = number.slice(0, -1);
    else number += e.key;
    render();
  }

  return {
    init() {
      init();
      if (!tick) tick = setInterval(liveTick, 1000);
    },
    stop, dial, toggle, box, bounds,
    isOpen: () => isOpen(),
    started: () => started,
    // The call being talked on (not held), for the BLF and parking keys.
    liveCall: () => callList().find((c) => c.state === 'answered' && !c.held) || null,
    hasCalls: () => callList().length > 0,
    transfer: (callId, target) => cmd('phone.transfer', { call: callId, target }),
    isAgent: () => !!(me && me.agent),
    setStatus: (status, aux) => setStatus(status, aux),
    show: (t) => { tab = t; dialMode = false; render(); },
    // The audio connection: state, the server addresses offered, and the
    // path in use (support and tests).
    diag: () => diagNow(),
    // Audio counters (support and tests).
    async stats() {
      if (!pc) return null;
      const out = { sent: 0, received: 0 };
      (await pc.getStats()).forEach((r) => {
        if (r.type === 'outbound-rtp' && r.kind === 'audio') out.sent += r.packetsSent || 0;
        if (r.type === 'inbound-rtp' && r.kind === 'audio') out.received += r.packetsReceived || 0;
      });
      return out;
    },
  };
})();
window.Phone = Phone;
