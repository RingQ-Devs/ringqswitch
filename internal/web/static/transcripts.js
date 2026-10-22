'use strict';
// Call transcripts: the icon in Call history and Queue calls opens the
// conversation (Speaker A, the caller; Speaker B, who answered), with the
// sentiment of each line, of each speaker and of the call, and the
// recording (a click on a line plays from there). Settings → Call
// transcription turns it on.

const TR_LABEL = { positive: ['Positive', 'green'], neutral: ['Neutral', 'grey'], negative: ['Negative', 'red'] };
const TR = { onUpdate: null, timer: null };

on('transcript', () => {
  // A transcript is ready: the open report shows it.
  clearTimeout(TR.timer);
  TR.timer = setTimeout(() => { if (TR.onUpdate) TR.onUpdate(); }, 800);
});

function trScore(v) {
  const n = Number(v);
  return Number.isFinite(n) ? (n > 0 ? '+' : '') + n.toFixed(2) : '';
}

// sentimentBadge: a label (positive / neutral / negative) as a badge.
function sentimentBadge(label, score) {
  const l = TR_LABEL[label];
  if (!l) return '';
  return h('span', { class: 'badge tr-badge ' + l[1], title: 'Sentiment ' + trScore(score) }, l[0]);
}

// trCell is a report row's Transcript cell; id is the call's uuid.
function trCell(r, id, title, canRequest, reload) {
  const ask = async (ev) => {
    ev.stopPropagation();
    try { await api('POST', dpath('transcripts/' + id)); toast('Transcribing the call…'); if (reload) reload(); } catch (e) { fail(e); }
  };
  switch (r.transcript) {
    case 'done':
      return h('button', { class: 'icon-btn tr-open', type: 'button', title: 'Read the transcript', 'aria-label': 'Read the transcript',
        onclick: (ev) => { ev.stopPropagation(); openTranscript(id, title); } }, icon('transcript', 18));
    case 'empty':
      return h('span', { class: 'muted small', title: 'The recording holds no speech' }, 'No speech');
    case 'queued':
    case 'processing':
      return h('span', { class: 'tr-wait', title: r.transcript === 'queued' ? 'Waiting to be transcribed' : 'Transcribing…' }, icon('clock', 15));
    case 'failed':
      return h('span', { class: 'tr-fail' }, h('span', { class: 'badge red', title: 'The transcription failed' }, 'Failed'),
        canRequest ? h('button', { class: 'icon-btn', type: 'button', title: 'Try again', 'aria-label': 'Try again', onclick: ask }, icon('refresh', 15)) : null);
  }
  if (canRequest && r.record_name) {
    return h('button', { class: 'btn small tr-ask', type: 'button', title: 'Transcribe this recorded call', onclick: ask }, 'Transcribe');
  }
  return '';
}

function trClock(sec) {
  const s = Math.max(0, Math.floor(Number(sec) || 0));
  return String(Math.floor(s / 60)).padStart(2, '0') + ':' + String(s % 60).padStart(2, '0');
}

// openTranscript shows a call's transcript in a window.
async function openTranscript(id, title) {
  let data;
  try { data = await api('GET', dpath('transcripts/' + id)); } catch (e) { fail(e); return; }
  const tr = data.transcript || {};
  const call = data.call || {};
  const segs = tr.segments || [];
  const role = (sp) => (tr.customer ? (sp === tr.customer ? 'Customer' : 'Agent') : '');
  const nameOf = (sp) => {
    if (!sp) return 'Speaker';
    const n = sp === 'A' ? tr.speaker_a : tr.speaker_b;
    return 'Speaker ' + sp + (n ? ' · ' + n : '');
  };
  const audio = call.record_name ? h('audio', { controls: true, preload: 'metadata', src: '/api/' + dpath('cdr/' + id + '/recording'), class: 'tr-audio' }) : null;

  // Summary: the call, then each speaker.
  const meter = (score) => {
    const pct = Math.round((Number(score) + 1) * 50);
    return h('div', { class: 'tr-meter', title: trScore(score) }, h('i', { style: 'left:' + Math.max(0, Math.min(100, pct)) + '%' }));
  };
  const card = (head, label, score, extra) => h('div', { class: 'tr-card' },
    h('div', { class: 'tr-card-head' }, head), h('div', { class: 'tr-card-val' }, sentimentBadge(label, score), h('span', { class: 'muted small' }, trScore(score))),
    meter(score), extra ? h('div', { class: 'muted small' }, extra) : null);
  const speakers = (tr.speakers || []).map((sp) => card(
    [h('span', { class: 'tr-dot s-' + (sp.speaker || 'x').toLowerCase() }), h('span', { class: 'tr-name', title: nameOf(sp.speaker) }, nameOf(sp.speaker)),
      role(sp.speaker) ? h('span', { class: 'tr-role' }, role(sp.speaker)) : null],
    sp.label, sp.sentiment,
    fmtDur(sp.talk_seconds) + ' talking · ending ' + trScore(sp.ending) + ' · ' + sp.positive + ' positive, ' + sp.negative + ' negative'));
  const overall = card('Whole call', tr.sentiment_label, tr.sentiment,
    (tr.language ? 'Language ' + tr.language.toUpperCase() + ' · ' : '') + fmtDur(tr.duration) + (tr.stereo === false ? ' · recorded in mono: speakers not separated' : ''));

  // The conversation.
  const lines = segs.map((s, i) => {
    const l = TR_LABEL[sentimentLabel(s.sentiment)] || TR_LABEL.neutral;
    return h('div', { class: 'tr-line s-' + (s.speaker || 'x').toLowerCase(), 'data-i': String(i),
      title: audio ? 'Play from here' : null, onclick: () => { if (audio) { audio.currentTime = s.start; audio.play().catch(() => {}); } } },
    h('div', { class: 'tr-who' }, h('b', null, nameOf(s.speaker)), role(s.speaker) ? h('span', { class: 'tr-role' }, role(s.speaker)) : null,
      h('span', { class: 'muted small' }, trClock(s.start)),
      h('span', { class: 'tr-sent ' + l[1], title: l[0] + ' ' + trScore(s.sentiment) })),
    h('div', { class: 'tr-text' }, s.text));
  });
  const list = h('div', { class: 'tr-list' }, lines.length ? lines : h('div', { class: 'muted' }, tr.status === 'empty' ? 'No speech was found in the recording.' : 'Nothing was transcribed.'));
  if (audio) {
    audio.addEventListener('timeupdate', () => {
      const t = audio.currentTime;
      lines.forEach((el, i) => el.classList.toggle('playing', t >= segs[i].start && t < segs[i].end + 0.3));
    });
  }
  const text = () => segs.map((s) => '[' + trClock(s.start) + '] ' + nameOf(s.speaker) + (role(s.speaker) ? ' (' + role(s.speaker) + ')' : '') + ': ' + s.text).join('\n');
  let close = null;
  const again = async () => {
    try {
      await api('POST', dpath('transcripts/' + id));
      toast('Transcribing the call again…');
      if (close) close();
      if (TR.onUpdate) TR.onUpdate();
    } catch (e) { fail(e); }
  };
  const actions = h('div', { class: 'tr-actions' },
    h('button', { class: 'btn small', type: 'button', onclick: () => { navigator.clipboard.writeText(text()).then(() => toast('Copied'), () => toast('Copy failed', true)); } }, icon('copy', 14), 'Copy'),
    h('button', { class: 'btn small', type: 'button', onclick: () => {
      const a = h('a', { href: URL.createObjectURL(new Blob([title + '\n\n' + text() + '\n'], { type: 'text/plain' })), download: 'transcript-' + id + '.txt' });
      document.body.append(a); a.click(); a.remove();
    } }, icon('download', 14), 'Download'),
    isSupervisor() && call.record_name ? h('button', { class: 'btn small', type: 'button', title: 'Transcribe this call again (with the current model)', onclick: again }, icon('refresh', 14), 'Transcribe again') : null);
  const body = h('div', { class: 'tr-view' },
    h('p', { class: 'muted tr-sub' }, title),
    h('div', { class: 'tr-cards' }, overall, speakers), audio, list, actions);
  close = modal('Call transcript', body, false);
  const box = body.closest('.modal');
  if (box) box.classList.add('tr-modal');
}

// ---------------------------------------------------------------------------
// Voicemail messages: their text in the list, and a window to read it.

// vmTextCell is a voicemail message's text in the list (two lines; a click
// opens it all), or where its transcription is at.
function vmTextCell(r, reload) {
  const ask = async (ev) => {
    ev.stopPropagation();
    try { await api('POST', dpath('voicemail-messages/' + r.uuid + '/transcript')); toast('Transcribing the message…'); if (reload) reload(); } catch (e) { fail(e); }
  };
  if (r.transcript_text) {
    return h('button', { class: 'vm-snippet', type: 'button', title: 'Read the message', onclick: (ev) => { ev.stopPropagation(); openVoicemailTranscript(r); } }, r.transcript_text);
  }
  switch (r.transcript) {
    case 'queued':
    case 'processing':
      return h('span', { class: 'tr-wait muted small' }, icon('clock', 15), r.transcript === 'queued' ? 'Waiting…' : 'Transcribing…');
    case 'empty':
      return h('span', { class: 'muted small' }, 'No speech');
    case 'failed':
      return h('span', { class: 'tr-fail' }, h('span', { class: 'badge red', title: 'The transcription failed' }, 'Failed'),
        h('button', { class: 'icon-btn', type: 'button', title: 'Try again', 'aria-label': 'Try again', onclick: ask }, icon('refresh', 15)));
  }
  return h('button', { class: 'btn small tr-ask', type: 'button', title: 'Write this message down', onclick: ask }, 'Transcribe');
}

// openVoicemailTranscript shows a voicemail message: who left it, the
// recording and what they said (a click on a line plays from there).
async function openVoicemailTranscript(r) {
  let tr = null;
  try { tr = (await api('GET', dpath('voicemail-messages/' + r.uuid + '/transcript'))).transcript; } catch (e) { /* only the text the RingQ database keeps */ }
  const segs = (tr && tr.segments) || [];
  const audio = h('audio', { controls: true, preload: 'metadata', src: '/api/' + dpath('voicemail-messages/' + r.uuid + '/audio'), class: 'tr-audio' });
  audio.addEventListener('play', () => vmHeard(r), { once: true });
  const lines = segs.length ? segs.map((sg) => {
    const l = TR_LABEL[sentimentLabel(sg.sentiment)] || TR_LABEL.neutral;
    return h('div', { class: 'tr-line vm-line', title: 'Play from here', onclick: () => { audio.currentTime = sg.start; audio.play().catch(() => {}); } },
      h('div', { class: 'tr-who' }, h('span', { class: 'muted small' }, trClock(sg.start)), h('span', { class: 'tr-sent ' + l[1], title: l[0] + ' ' + trScore(sg.sentiment) })),
      h('div', { class: 'tr-text' }, sg.text));
  }) : [h('div', { class: 'tr-line vm-line' }, h('div', { class: 'tr-text' }, r.transcript_text || (tr && tr.status === 'empty' ? 'No speech was found in the message.' : 'Not transcribed yet.')))];
  if (segs.length) {
    audio.addEventListener('timeupdate', () => {
      const t = audio.currentTime;
      lines.forEach((el, i) => el.classList.toggle('playing', t >= segs[i].start && t < segs[i].end + 0.3));
    });
  }
  const from = party(r.caller_id_name, r.caller_id_number) || 'Unknown caller';
  const title = from + ' → voicemail ' + r.box + ' · ' + fmtTime(r.created);
  const text = () => (segs.length ? segs.map((sg) => '[' + trClock(sg.start) + '] ' + sg.text).join('\n') : (r.transcript_text || ''));
  let close = null;
  const again = async () => {
    try { await api('POST', dpath('voicemail-messages/' + r.uuid + '/transcript')); toast('Transcribing the message again…'); if (close) close(); if (TR.onUpdate) TR.onUpdate(); } catch (e) { fail(e); }
  };
  const label = (tr && tr.sentiment_label) || r.sentiment;
  const body = h('div', { class: 'tr-view' },
    h('p', { class: 'muted tr-sub' }, title),
    h('div', { class: 'vm-meta' }, h('span', null, icon('clock', 14), fmtDur(r.length)), label ? sentimentBadge(label, (tr && tr.sentiment) || r.sentiment_score) : null,
      tr && tr.language ? h('span', { class: 'muted small' }, 'Language ' + tr.language.toUpperCase()) : null),
    audio, h('div', { class: 'tr-list' }, lines),
    h('div', { class: 'tr-actions' },
      h('button', { class: 'btn small', type: 'button', onclick: () => { navigator.clipboard.writeText(text()).then(() => toast('Copied'), () => toast('Copy failed', true)); } }, icon('copy', 14), 'Copy'),
      h('button', { class: 'btn small', type: 'button', onclick: () => {
        const a = h('a', { href: URL.createObjectURL(new Blob([title + '\n\n' + text() + '\n'], { type: 'text/plain' })), download: 'voicemail-' + r.uuid + '.txt' });
        document.body.append(a); a.click(); a.remove();
      } }, icon('download', 14), 'Download'),
      h('button', { class: 'btn small', type: 'button', title: 'Transcribe the message again (with the current model)', onclick: again }, icon('refresh', 14), 'Transcribe again')));
  close = modal('Voicemail message', body, false);
  const box = body.closest('.modal');
  if (box) box.classList.add('tr-modal');
}

function sentimentLabel(v) {
  const n = Number(v);
  return n >= 0.05 ? 'positive' : n <= -0.05 ? 'negative' : 'neutral';
}

// ---------------------------------------------------------------------------
// Settings → Call transcription

const TR_LANGUAGES = [['auto', 'Detect automatically'], ['en', 'English'], ['ms', 'Malay'], ['ta', 'Tamil'], ['zh', 'Chinese'],
  ['hi', 'Hindi'], ['id', 'Indonesian'], ['th', 'Thai'], ['vi', 'Vietnamese'], ['tl', 'Tagalog'], ['ar', 'Arabic'], ['ja', 'Japanese'],
  ['ko', 'Korean'], ['fr', 'French'], ['es', 'Spanish'], ['de', 'German'], ['pt', 'Portuguese'], ['bn', 'Bengali'], ['ur', 'Urdu']];

function fmtSize(b) {
  const n = Number(b) || 0;
  return n >= 1e9 ? (n / 1e9).toFixed(1) + ' GB' : Math.round(n / 1e6) + ' MB';
}

PAGES.transcription = async () => {
  const main = $('#main');
  let data = await api('GET', dpath('transcription'));
  const st = data.settings;
  const enabled = h('input', { type: 'checkbox', checked: !!st.enabled, 'aria-label': 'Transcribe calls' });
  const vm = h('input', { type: 'checkbox', checked: st.voicemail !== false, 'aria-label': 'Transcribe voicemail messages' });
  const live = h('input', { type: 'checkbox', checked: !!st.live, 'aria-label': 'Live transcription during calls' });
  const scope = selectOf([['external', 'Incoming and outgoing calls (through a trunk)'], ['all', 'All calls, including calls between extensions']], st.scope);
  const lang = selectOf(TR_LANGUAGES, st.language);
  const model = h('select');
  let chosen = st.model || '';
  const fillModels = () => {
    const opts = [['', 'Server default' + (data.default_model ? ' (' + data.default_model + ')' : '')]];
    data.models.filter((m) => m.installed).forEach((m) => opts.push([m.name, m.title + (m.title !== m.name ? ' (' + m.name + ')' : '')]));
    if (chosen && !opts.some((o) => o[0] === chosen)) opts.push([chosen, chosen + ' (not installed: the default is used)']);
    model.replaceChildren(...opts.map(([v, l]) => h('option', { value: v, selected: v === chosen }, l)));
  };
  model.addEventListener('change', () => { chosen = model.value; });
  const liveModel = h('select');
  let liveChosen = st.live_model || '';
  const fillLiveModels = () => {
    const opts = [['', 'Fastest installed' + (data.live_default_model ? ' (' + data.live_default_model + ')' : '')]];
    data.models.filter((m) => m.installed).forEach((m) => opts.push([m.name, m.title + (m.title !== m.name ? ' (' + m.name + ')' : '')]));
    if (liveChosen && !opts.some((o) => o[0] === liveChosen)) opts.push([liveChosen, liveChosen + ' (not installed: the fastest is used)']);
    liveModel.replaceChildren(...opts.map(([v, l]) => h('option', { value: v, selected: v === liveChosen }, l)));
  };
  liveModel.addEventListener('change', () => { liveChosen = liveModel.value; });
  fillModels();
  fillLiveModels();
  const save = async () => {
    try {
      await api('PUT', dpath('transcription'), { enabled: enabled.checked, scope: scope.value, language: lang.value, model: model.value, voicemail: vm.checked,
        live: live.checked, live_model: liveModel.value });
      toast('Saved');
      await refresh();
    } catch (e) { fail(e); }
  };
  const enginePanel = h('div', { class: 'panel' });
  const modelsPanel = h('div', { class: 'panel' });
  const countsPanel = h('div', { class: 'panel' });
  const act = async (method, m) => {
    if (method === 'DELETE' && !confirm('Delete the ' + m.title + ' model (' + fmtSize(m.size) + ')?')) return;
    try { await api(method, dpath('transcription/models/' + encodeURIComponent(m.name))); await refresh(); } catch (e) { fail(e); }
  };
  // the live recogniser: running with the live model (started as soon as live
  // transcription is on, so calls do not wait for the model to load).
  const liveState = () => {
    const model = h('code', null, data.live_model_in_use || '');
    if (!data.live_server) {
      return [badge('Slower', 'amber'), h('span', { class: 'muted small' }, ' the live recogniser is not installed, so each sentence loads the model again (a few seconds late). Add it by running the install script again:')];
    }
    if (!data.settings.live) return [badge('Ready', 'green'), ' live recogniser, model ', model, h('span', { class: 'muted small' }, ' · starts when live transcription is turned on')];
    if (data.live_running) return [badge('Running', 'green'), ' live recogniser, model ', model, h('span', { class: 'muted small' }, ' · loaded and waiting for calls')];
    if (data.live_error) return [badge('Not running', 'red'), ' live recogniser, model ', model, h('span', { class: 'muted small' }, ' · ' + data.live_error)];
    return [badge('Starting', 'amber'), ' the live recogniser is loading the model ', model, '…'];
  };
  // Live text must keep up: a model larger than Base is slower to answer.
  const liveTip = () => {
    const slow = /^(small|medium|large)/.test(data.live_model_in_use || '');
    if (!slow) return null;
    const base = data.models.find((m) => m.name === 'base');
    return h('p', { class: 'muted small' }, icon('info', 13), ' ', data.live_model_in_use + ' is accurate but slower for live text; Base answers about 3 times faster. ',
      base && !base.installed && data.can_manage_models && !(base.download && !base.download.error)
        ? h('button', { class: 'btn small', type: 'button', onclick: () => act('POST', base) }, icon('download', 14), 'Download Base')
        : base && base.installed ? 'Choose it under "Live transcription model".' : 'Ask the administrator to download Base.');
  };
  const drawEngine = () => {
    const eng = data.engine;
    fill(enginePanel, h('h2', null, 'Speech recogniser'),
      eng.ok
        ? h('p', null, badge('Ready', 'green'), ' speech recogniser, model ', h('code', null, data.model_in_use || ''),
          h('span', { class: 'muted small' }, ' · ' + data.threads + ' CPU threads per call, ' + data.workers + ' call(s) at a time, calls shorter than ' + data.min_seconds + ' s are skipped'))
        : null,
      eng.ok ? h('p', null, 'Live transcription: ', liveState()) : null,
      eng.ok && data.live_server ? liveTip() : null,
      eng.ok && !data.live_server ? h('pre', { class: 'tr-cmd' }, 'sudo bash /usr/local/share/ringqswitch/install-transcription.sh') : null,
      eng.ok ? null : h('div', null, h('p', null, badge('Not installed', 'amber'), ' ', eng.error),
          h('p', null, 'Install it once on the server (it downloads and builds the speech recogniser and a speech model, about 5 minutes):'),
          h('pre', { class: 'tr-cmd' }, 'sudo bash /usr/local/share/ringqswitch/install-transcription.sh'),
          h('p', { class: 'muted small' }, 'Then this page shows "Ready" within a minute (no restart needed). The script is also in the source folder: packaging/install-transcription.sh.')));
  };
  const drawModels = () => {
    const status = (m) => {
      if (m.download && !m.download.error) {
        const pct = m.download.total > 0 ? Math.floor(100 * m.download.done / m.download.total) : 0;
        return h('div', { class: 'tr-dl' }, h('div', { class: 'tr-dl-bar' }, h('i', { style: 'width:' + pct + '%' })),
          h('span', { class: 'muted small' }, 'Downloading… ' + pct + '% (' + fmtSize(m.download.done) + ')'));
      }
      if (m.download && m.download.error) return h('span', { class: 'tr-fail', title: m.download.error }, badge('Download failed', 'red'), h('span', { class: 'muted small' }, ' ' + m.download.error));
      if (!m.installed) return h('span', { class: 'muted' }, 'Not installed');
      return h('span', null, badge('Installed', 'green'), m.name === data.model_in_use ? [' ', badge('In use', 'blue')] : null,
        data.settings.live && m.name === data.live_model_in_use ? [' ', badge('Live', 'blue')] : null);
    };
    const actions = (m) => {
      if (!data.can_manage_models) return '';
      if (m.download && !m.download.error) return '';
      if (!m.installed) return h('button', { class: 'btn small', type: 'button', onclick: () => act('POST', m) }, icon('download', 14), m.download ? 'Try again' : 'Download');
      if (m.removable) return h('button', { class: 'btn small danger', type: 'button', onclick: () => act('DELETE', m) }, 'Delete');
      return h('span', { class: 'muted small', title: m.path }, 'Installed on the server');
    };
    fill(modelsPanel, h('h2', null, 'Speech models'),
      h('p', { class: 'muted small' }, 'Larger models understand more (accents, noisy lines, other languages) but take longer per call. ' +
        (data.can_manage_models ? 'Downloads go to ' + data.model_dir + '; they come from the speech model library on the internet, so the server needs internet access. Choose the model this domain uses above, under "Speech model".'
          : 'Choose the model this domain uses above, under "Speech model". The administrator downloads more models.')),
      table([
        { label: 'Model', render: (m) => h('div', null, h('b', null, m.title), m.title !== m.name ? h('div', { class: 'muted small' }, m.name) : null) },
        { label: 'Size', render: (m) => fmtSize(m.size), num: 1 },
        { label: 'About', render: (m) => m.note || '', wrap: 1 },
        { label: 'Status', render: status },
        { label: '', actions: 1, render: actions },
      ], data.models, { empty: 'No models.' }));
  };
  const drawCounts = () => {
    const c = data.counts || {};
    fill(countsPanel, h('h2', null, 'Last 24 hours'),
      h('div', { class: 'tr-counts' }, [['queued', 'Waiting'], ['processing', 'Transcribing'], ['done', 'Transcribed'], ['empty', 'No speech'], ['failed', 'Failed']].map(([k, l]) =>
        h('div', { class: 'tr-count' + (k === 'failed' && c[k] ? ' bad' : '') + (k === 'processing' && c[k] ? ' busy' : '') }, h('b', null, String(c[k] || 0)), h('span', null, l)))));
  };
  const draw = () => { drawEngine(); drawModels(); drawCounts(); fillModels(); fillLiveModels(); };
  // The counts and downloads change while the page is open: transcripts
  // push an event, downloads are polled.
  const refresh = async () => {
    if (currentPage() !== 'transcription') return;
    data = await api('GET', dpath('transcription'));
    draw();
  };
  TR.onUpdate = () => { refresh().catch(() => {}); };
  every(2000, () => {
    const busy = data.models.some((m) => m.download && !m.download.error) || (data.counts && (data.counts.queued || data.counts.processing)) ||
      (data.settings.live && data.live_server && !data.live_running && !data.live_error);
    if (busy) refresh().catch(() => {});
  });
  fill(main, pageHead('Call transcription'),
    h('p', { class: 'page-intro' }, 'After each call, RingQswitch writes down who said what (Speaker A, the caller; Speaker B, who answered) and how they felt (sentiment). ' +
      'The transcript opens from the icon in Call history and Queue calls. Everything runs on this server: no call audio leaves it.'),
    h('div', { class: 'panel tr-settings' },
      h('div', { class: 'tr-row' }, h('label', { class: 'tgl' }, enabled, h('span')), h('div', null, h('b', null, 'Transcribe calls'),
        h('div', { class: 'muted small' }, 'Calls are recorded (each party on its own channel, so the transcript tells them apart) and transcribed after the hang-up. ' +
          'Let callers know calls are recorded.'))),
      h('div', { class: 'tr-row' }, h('label', { class: 'tgl' }, vm, h('span')), h('div', null, h('b', null, 'Transcribe voicemail messages'),
        h('div', { class: 'muted small' }, 'Each message left in a voicemail box is written down: its text shows on the Voicemail page and in the dialer\'s Voicemail tab ' +
          '(and in the RingQ portal\'s voicemail, which reads the same field).'))),
      h('div', { class: 'tr-row' }, h('label', { class: 'tgl' }, live, h('span')), h('div', null, h('b', null, 'Live transcription during calls'),
        h('div', { class: 'muted small' }, 'While a call goes on, what both parties say is written down as they speak, split by speaker, in a Live transcript panel ' +
          'beside the dialer of the extension on the call (its web page must be open). It is not saved. Each call being written down uses about one CPU core.'))),
      h('div', { class: 'form' },
        h('div', { class: 'field' }, h('label', null, 'Which calls'), scope),
        h('div', { class: 'field' }, h('label', null, 'Language spoken'), lang,
          h('div', { class: 'help' }, 'Detecting it works for most calls (each speaker\'s language is found on their longest turn); choose the language when calls are mostly in one. Sentiment is scored for English. ' +
            'Live text is clearest with the language chosen: when detecting, a call\'s language is fixed by its first clear sentence, and words so far show only after that.')),
        h('div', { class: 'field' }, h('label', null, 'Speech model'), model,
          h('div', { class: 'help' }, 'The installed models are listed below with their size and speed.')),
        h('div', { class: 'field' }, h('label', null, 'Live transcription model'), liveModel,
          h('div', { class: 'help' }, 'Live text must keep up with the conversation: Base is a good balance; Small is more accurate but needs a fast CPU.'))),
      h('div', { class: 'form-actions' }, h('button', { class: 'btn primary', type: 'button', onclick: save }, 'Save'))),
    enginePanel, modelsPanel, countsPanel);
  draw();
};
