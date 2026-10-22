'use strict';
// AI agents (menu AI agents): numbers answered by a voice AI. Callers hear
// a greeting and talk with the agent, which answers from its prompt,
// context, FAQ and web pages, and connects them to people when they ask
// (connect keywords). Point a DID at one in Call Flow (IVR card). The voice
// service (a cloud voice AI service, the domain's API key) is set up under
// Settings → AI agent service.

const AI_VOICES = [['marin', 'Marin · most natural'], ['cedar', 'Cedar · most natural'], ['alloy', 'Alloy'], ['ash', 'Ash'], ['ballad', 'Ballad'],
  ['coral', 'Coral'], ['echo', 'Echo'], ['sage', 'Sage'], ['shimmer', 'Shimmer'], ['verse', 'Verse']];
const AI_LANGS = [['en', 'English'], ['es', 'Spanish'], ['fr', 'French'], ['de', 'German'], ['ja', 'Japanese'], ['ko', 'Korean'], ['zh', 'Chinese'],
  ['fil', 'Filipino'], ['ta', 'Tamil'], ['ms', 'Malay'], ['hi', 'Hindi'], ['id', 'Indonesian'], ['th', 'Thai'], ['vi', 'Vietnamese'], ['ar', 'Arabic'],
  ['pt', 'Portuguese'], ['it', 'Italian'], ['nl', 'Dutch']];
const AI_DEFAULT_GREETING = 'Hi there! Welcome. How can I help you today?';
const AI_DEFAULT_PROMPT = 'You are the friendly voice assistant of our company. Answer the caller\'s questions about our products and services clearly and briefly. When you do not know the answer, say so and offer to connect the caller to a person.';
const AI_OUTCOMES = { active: ['In progress', 'blue'], completed: ['Completed', 'green'], transferred: ['Transferred', 'blue'], failed: ['Failed', 'red'],
  interrupted: ['Interrupted', 'amber'] };

const aiLangNames = (v) => String(v || '').split(',').map((c) => (AI_LANGS.find((l) => l[0] === c.trim()) || [c, c])[1]).filter(Boolean).join(', ');
const aiVoiceName = (v) => (AI_VOICES.find((x) => x[0] === v) || [v, v || ''])[1].split(' ·')[0];
function aiGreetings(v) {
  try { const g = JSON.parse(v || '[]'); if (Array.isArray(g)) return g.filter((x) => String(x).trim()); } catch (e) { /* lines */ }
  return String(v || '').split('\n').map((x) => x.trim()).filter(Boolean);
}

// ---- widgets ({ el, get() } like the form widgets)

// The languages the agent speaks: chips to switch on and off.
function aiLangChips(value) {
  const on = new Set(String(value || 'en').split(',').map((s) => s.trim()).filter(Boolean));
  const el = h('div', { class: 'ai-chips', role: 'group', 'aria-label': 'Languages' });
  const draw = () => fill(el, AI_LANGS.map(([code, name]) => h('button', { type: 'button', class: 'ai-chip' + (on.has(code) ? ' on' : ''),
    'aria-pressed': String(on.has(code)), onclick: () => {
      if (on.has(code) && on.size > 1) on.delete(code); else on.add(code);
      draw();
    } }, on.has(code) ? icon('check', 13) : null, name)));
  draw();
  return { el, get: () => AI_LANGS.map((l) => l[0]).filter((c) => on.has(c)).join(',') };
}

// Greetings: one is picked at random for each call.
function aiGreetingList(value) {
  const box = h('div', { class: 'ai-list' });
  const addRow = (text) => {
    const inp = h('input', { type: 'text', value: text || '', placeholder: 'e.g. Hi there! Welcome to RingQ. How can I help you?', maxlength: '600' });
    inp.value = text || '';
    const row = h('div', { class: 'ai-list-row' }, inp, h('button', { type: 'button', class: 'icon-btn', title: 'Remove', 'aria-label': 'Remove greeting',
      onclick: () => { row.remove(); if (!$('.ai-list-row', box)) addRow(''); } }, icon('trash', 15)));
    box.insertBefore(row, add);
    return inp;
  };
  const add = h('button', { type: 'button', class: 'btn small', onclick: () => addRow('').focus() }, icon('plus', 14), 'Add a greeting');
  box.append(add);
  const list = aiGreetings(value);
  (list.length ? list : ['']).forEach(addRow);
  return { el: box, get: () => JSON.stringify($$('input', box).map((i) => i.value.trim()).filter(Boolean)), first: () => ($('input', box) || {}).value || '' };
}

// Connect keywords: what the caller asks for → where the call goes.
function aiKeywordList(rows) {
  const box = h('div', { class: 'ai-list' });
  const addRow = (k) => {
    k = k || {};
    const word = h('input', { type: 'text', value: k.keyword || '', placeholder: 'e.g. agent, sales, support', maxlength: '80', 'aria-label': 'Keyword' });
    word.value = k.keyword || '';
    const dest = destOptions(k.destination || '');
    dest.setAttribute('aria-label', 'Connects to');
    const row = h('div', { class: 'ai-list-row kw' }, word, h('span', { class: 'ai-arrow' }, icon('right', 15)), dest,
      h('button', { type: 'button', class: 'icon-btn', title: 'Remove', 'aria-label': 'Remove keyword', onclick: () => row.remove() }, icon('trash', 15)));
    row._get = () => ({ keyword: word.value.trim(), destination: dest.value });
    box.insertBefore(row, add);
    return word;
  };
  const add = h('button', { type: 'button', class: 'btn small', onclick: () => addRow({}).focus() }, icon('plus', 14), 'Add a keyword');
  box.append(add);
  (rows || []).forEach(addRow);
  return { el: box, get: () => $$('.ai-list-row', box).map((r) => r._get()).filter((k) => k.keyword || k.destination) };
}

function aiTextarea(value, rows, ph) {
  const el = h('textarea', { rows: String(rows), placeholder: ph || '' });
  el.value = value || '';
  return { el, get: () => el.value };
}

// aiPlay plays a voice saying a text (the service makes it).
async function aiPlay(btn, voice, text) {
  const was = btn.innerHTML;
  btn.disabled = true;
  btn.textContent = t('Preparing…');
  try {
    const r = await fetch('/api/' + dpath('ai/preview'), { method: 'POST', credentials: 'same-origin',
      headers: { 'X-RQ': '1', 'Content-Type': 'application/json' }, body: JSON.stringify({ voice, text }) });
    if (!r.ok) { const d = await r.json().catch(() => ({})); throw new Error(d.error || r.statusText); }
    const url = URL.createObjectURL(await r.blob());
    const audio = new Audio(url);
    audio.addEventListener('ended', () => URL.revokeObjectURL(url));
    await audio.play();
  } catch (e) { fail(e); } finally { btn.disabled = false; btn.innerHTML = was; }
}

// ---- list and quick form

R.aiagents = {
  title: 'AI agents', one: 'AI agent', key: 'ai_agent_uuid', name: 'agent_name',
  intro: 'An AI agent answers calls with a natural voice: it greets callers, answers their questions from what you give it (prompt, context, FAQ, your web pages) and connects them to people when they ask. Send a DID to it in Call Flow (IVR card).',
  cols: [
    { label: 'Number', key: 'agent_extension' },
    { label: 'Name', render: (r) => h('span', { class: 'ai-name' }, h('span', { class: 'ai-avatar' }, icon('bot', 15)), r.agent_name) },
    { label: 'Voice', render: (r) => aiVoiceName(r.agent_voice) },
    { label: 'Languages', render: (r) => aiLangNames(r.agent_languages), wrap: 1 },
    { label: 'Web pages', render: (r) => String((r.knowledge_urls || '').split(/\s+/).filter(Boolean).length) },
    { label: 'Status', render: (r) => truthy(r.agent_enabled === '' ? 'true' : r.agent_enabled) ? badge('Enabled', 'green') : badge('Disabled') },
  ],
  defaults: () => ({ agent_enabled: 'true', agent_voice: 'marin', agent_noise: 'near', agent_languages: 'en', agent_greetings: JSON.stringify([AI_DEFAULT_GREETING]),
    agent_prompt: AI_DEFAULT_PROMPT, max_minutes: '10', allow_end_call: 'true', keywords: [] }),
  // The window of Call Flow's +: what an agent needs to take calls.
  quickIntro: 'Knowledge (web pages, FAQ), connect keywords and the call log are on the AI agents page.',
  quick: [
    { k: 'agent_name', l: 'Name', req: 1, ph: 'e.g. RingQ Assistant' },
    { k: 'agent_extension', l: 'Number', req: 1, help: 'e.g. 5000. Send a DID here to have the agent answer it.' },
    { k: 'agent_voice', l: 'Voice', t: 'select', o: AI_VOICES },
    { k: 'agent_languages', l: 'Languages', full: 1, w: (v) => aiLangChips(v) },
    { k: 'agent_greetings', l: 'Greeting', full: 1, w: (v) => {
      const inp = h('input', { type: 'text', maxlength: '600' });
      inp.value = aiGreetings(v)[0] || '';
      return { el: inp, get: () => JSON.stringify(inp.value.trim() ? [inp.value.trim()] : []) };
    } },
    { k: 'agent_prompt', l: 'Prompt', full: 1, w: (v) => aiTextarea(v, 4), help: 'Who the agent is and how it helps callers.' },
  ],
  form: (id) => aiAgentForm(id),
};

// ---- the agent's page: Voice, Knowledge, Settings, Status & log

async function aiAgentForm(id) {
  const main = $('#main');
  await loadMeta();
  const row = id ? await api('GET', dpath('aiagents/' + id)) : R.aiagents.defaults();
  let tab = 'voice';
  try { tab = sessionStorage.getItem('rq-ai-tab') || 'voice'; } catch (e) { /* private mode */ }
  if (!id && tab === 'log') tab = 'voice';
  const w = {
    langs: aiLangChips(row.agent_languages),
    voice: selectOf(AI_VOICES, row.agent_voice || 'marin'),
    noise: selectOf([['near', 'Near · callers on a handset or headset'], ['far', 'Far · callers on a speakerphone or in a car']], row.agent_noise || 'near'),
    keywords: aiKeywordList(row.keywords),
    greetings: aiGreetingList(row.agent_greetings),
    prompt: aiTextarea(row.agent_prompt, 8, AI_DEFAULT_PROMPT),
    context: aiTextarea(row.agent_context, 6, 'e.g. RingQ is a cloud contact centre from Cal4Care. Office hours: Monday to Friday, 9am to 6pm (Singapore time). Website: ringq.io.'),
    urls: aiTextarea(row.knowledge_urls, 4, 'https://www.example.com/\nhttps://www.example.com/pricing'),
    faq: aiTextarea(row.knowledge_text, 12, 'Q: What are your office hours?\nA: Monday to Friday, 9am to 6pm.\n\nQ: Do you offer a free trial?\nA: Yes, 14 days, no card needed.'),
    name: h('input', { type: 'text', value: row.agent_name || '', placeholder: 'e.g. RingQ Assistant' }),
    number: h('input', { type: 'text', value: row.agent_extension || '', placeholder: 'e.g. 5000', inputmode: 'numeric' }),
    enabled: h('input', { type: 'checkbox', checked: truthy(row.agent_enabled === undefined || row.agent_enabled === '' ? 'true' : row.agent_enabled) }),
    max: h('input', { type: 'number', min: '1', max: '120', value: row.max_minutes || '10' }),
    end: h('input', { type: 'checkbox', checked: truthy(row.allow_end_call === undefined || row.allow_end_call === '' ? 'true' : row.allow_end_call) }),
    fallback: destOptions(row.fallback_action || ''),
    desc: h('input', { type: 'text', value: row.agent_description || '' }),
  };
  w.name.value = row.agent_name || '';
  w.number.value = row.agent_extension || '';
  w.desc.value = row.agent_description || '';
  const field = (label, el, help, cls) => h('div', { class: 'field ' + (cls || '') }, h('label', null, label), el, help ? h('div', { class: 'help' }, help) : null);
  const play = h('button', { type: 'button', class: 'btn', title: 'Hear this voice say your first greeting' }, icon('play', 15), 'Preview');
  play.addEventListener('click', () => aiPlay(play, w.voice.value, w.greetings.first() || AI_DEFAULT_GREETING));

  const panes = {
    voice: h('div', { class: 'ai-cols' },
      h('div', { class: 'panel ai-col' },
        field('Languages', w.langs.el, 'The agent answers in the language the caller speaks (when it is one of these), else in the first one.'),
        field('Agent voice', h('div', { class: 'ai-row' }, w.voice, play), 'Marin and Cedar sound the most like a real person.'),
        field('Noise field', w.noise, 'Filters background noise for how callers usually call.'),
        field('Connect keywords', w.keywords.el, 'When the caller asks for one of these (or says it), the agent tells them and connects the call. Pressing 0 connects to the first one.')),
      h('div', { class: 'panel ai-col' },
        field('Custom greetings', w.greetings.el, 'What callers hear first. With several, each call gets one of them.'),
        field('Prompt', w.prompt.el, 'Who the agent is, what it does and how it speaks.'),
        field('Context', w.context.el, 'Facts the agent should always know: company, products, hours, addresses, policies.'))),
    knowledge: h('div', { class: 'ai-cols' },
      h('div', { class: 'panel ai-col' },
        field('Web pages', w.urls.el, 'One address per line (up to 10). Each page and the pages it links to on the same site are read (up to 40 pages) when you save, so the agent can answer from them.'),
        h('div', { class: 'ai-pages' })),
      h('div', { class: 'panel ai-col' },
        field('Questions and answers', w.faq.el, 'Anything callers ask about, in your own words. Leave a blank line between topics.'),
        h('div', { class: 'ai-note' }, h('div', { class: 'ai-note-ic' }, icon('info', 16)), h('div', null,
          t('A small amount of knowledge stays in the agent\'s mind for every call; with more, the agent looks up the right part the moment the caller asks. It never makes up prices or facts: when it does not know, it says so and offers a person.'))))),
    settings: h('div', { class: 'panel' }, h('div', { class: 'form' },
      field('Name', w.name, null),
      field('Number', w.number, 'Send a DID or an IVR key to this number.'),
      field('Status', h('label', { class: 'switch' }, w.enabled, t('Enabled (answers calls)'))),
      field('Longest call (minutes)', w.max, 'The agent says goodbye when the call reaches this length.'),
      field('Ending calls', h('label', { class: 'switch' }, w.end, t('The agent may hang up after saying goodbye'))),
      field('When the AI service cannot be reached, go to', w.fallback, 'E.g. the reception extension or a queue. Without one the call ends.'),
      field('Description', w.desc, null, 'full'),
      h('div', { class: 'field full' }, h('div', { class: 'muted small' }, t('The voice service (API key, model) is set under'), ' ',
        h('a', { href: '#/ai-settings' }, t('Settings → AI agent service')), '.')))),
    log: h('div'),
  };

  const tabs = h('div', { class: 'ai-tabs', role: 'tablist' });
  const paneBox = h('div');
  const show = (k) => {
    tab = k;
    try { sessionStorage.setItem('rq-ai-tab', k); } catch (e) { /* private mode */ }
    fill(tabs, [['voice', 'Voice'], ['knowledge', 'Knowledge'], ['settings', 'Settings'], ['log', 'Status & log']].filter(([k2]) => id || k2 !== 'log').map(([k2, l]) =>
      h('button', { type: 'button', role: 'tab', class: k2 === k ? 'on' : '', 'aria-selected': String(k2 === k), onclick: () => show(k2) }, t(l))));
    fill(paneBox, panes[k]);
    if (k === 'log') aiLogPane(panes.log, id);
    if (k === 'knowledge') aiPagesPane($('.ai-pages', panes.knowledge), id);
  };

  const err = h('div', { class: 'error-text ai-err' });
  const saveBtn = h('button', { class: 'btn primary', type: 'button' }, icon('check', 16), 'Save');
  saveBtn.addEventListener('click', async () => {
    err.textContent = '';
    const body = {
      agent_name: w.name.value.trim(), agent_extension: w.number.value.trim(), agent_enabled: String(w.enabled.checked),
      agent_voice: w.voice.value, agent_noise: w.noise.value, agent_languages: w.langs.get(), agent_greetings: w.greetings.get(),
      agent_prompt: w.prompt.get(), agent_context: w.context.get(), knowledge_urls: w.urls.get(), knowledge_text: w.faq.get(),
      max_minutes: w.max.value, allow_end_call: String(w.end.checked), fallback_action: w.fallback.value, agent_description: w.desc.value.trim(),
      keywords: w.keywords.get(),
    };
    if (!body.agent_name || !body.agent_extension) { show('settings'); err.textContent = t('Enter the agent\'s name and number (Settings).'); return; }
    saveBtn.disabled = true;
    try {
      const saved = await api(id ? 'PUT' : 'POST', dpath('aiagents' + (id ? '/' + id : '')), body);
      toast('Saved');
      await loadMeta();
      if (!id) { location.hash = '#/aiagents/' + saved.ai_agent_uuid; return; }
      Object.assign(row, saved);
      title.textContent = saved.agent_name;
      if (tab === 'knowledge') aiPagesPane($('.ai-pages', panes.knowledge), id);
    } catch (e) { err.textContent = e.message; } finally { saveBtn.disabled = false; }
  });
  const del = id ? h('button', { class: 'btn danger', type: 'button', onclick: async () => {
    if (!confirm(t('Delete this AI agent? DIDs sent to its number stop being answered.'))) return;
    try { await api('DELETE', dpath('aiagents/' + id)); toast('Deleted'); await loadMeta(); location.hash = '#/aiagents'; } catch (e) { fail(e); }
  } }, 'Delete') : null;
  const title = h('span', null, id ? row.agent_name : t('New AI agent'));
  fill(main,
    h('div', { class: 'page-head' },
      h('a', { class: 'icon-btn', href: '#/aiagents', title: t('Back to AI agents'), 'aria-label': t('Back to AI agents') }, icon('left', 18)),
      h('h1', { class: 'ai-title' }, h('span', { class: 'ai-avatar big' }, icon('bot', 20)), title,
        id ? h('span', { class: 'count-pill' }, row.agent_extension) : null),
      h('span', { class: 'spacer' }), del, saveBtn),
    tabs, err, paneBox);
  show(tab);
  if (!id) w.name.focus();
}

// The fetched web pages of an agent, with Fetch now.
async function aiPagesPane(box, id) {
  if (!box) return;
  if (!id) { fill(box, h('p', { class: 'muted small' }, t('The pages are read after you save the agent.'))); return; }
  let timer = null;
  const load = async () => {
    if (!box.isConnected) { clearInterval(timer); return; }
    let k;
    try { k = await api('GET', dpath('ai/knowledge/' + id)); } catch (e) { fill(box, h('p', { class: 'error-text' }, e.message)); return; }
    const st = k.status || {};
    const fetchBtn = h('button', { type: 'button', class: 'btn small', disabled: st.running, onclick: async () => {
      try { await api('POST', dpath('ai/knowledge/' + id)); toast('Reading the pages…'); setTimeout(load, 400); } catch (e) { fail(e); }
    } }, icon('refresh', 14), st.running ? t('Reading…') : t('Fetch now'));
    const total = k.pages.reduce((n, p) => n + Number(p.chars || 0), 0);
    fill(box,
      h('div', { class: 'ai-pages-head' }, h('b', null, t('Pages read')), h('span', { class: 'count-pill' }, String(k.pages.length)),
        total ? h('span', { class: 'muted small' }, Math.round(total / 1000) + 'k ' + t('characters')) : null, h('span', { class: 'spacer' }), fetchBtn),
      st.running ? h('div', { class: 'muted small' }, h('span', { class: 'ai-spin' }), ' ', t('Reading the web pages…')) : null,
      st.error ? h('div', { class: 'error-text small' }, st.error) : null,
      k.pages.length ? h('div', { class: 'ai-page-list' }, k.pages.map((p) => h('div', { class: 'ai-page' },
        h('div', { class: 'ai-page-title' }, p.title || p.source),
        h('a', { class: 'muted small', href: p.source, target: '_blank', rel: 'noopener noreferrer' }, p.source),
        h('span', { class: 'muted small' }, p.chunks + ' ' + t('parts') + (p.fetched_at ? ' · ' + fmtTime(p.fetched_at) : '')))))
        : (st.running ? null : h('p', { class: 'muted small' }, t('No pages yet. Add web addresses above and save.'))));
    if (!st.running && timer) { clearInterval(timer); timer = null; }
    if (st.running && !timer) timer = setInterval(load, 2000);
  };
  load();
}

// Status & log: the agent's numbers and the calls it took.
async function aiLogPane(box, id) {
  fill(box, h('div', { class: 'muted' }, t('Loading…')));
  let offset = 0;
  const limit = 50;
  const draw = async () => {
    let st, calls;
    try {
      [st, calls] = await Promise.all([api('GET', dpath('ai/status')), api('GET', dpath('ai/calls?agent=' + id + '&limit=' + limit + '&offset=' + offset))]);
    } catch (e) { fill(box, h('div', { class: 'panel error-text' }, e.message)); return; }
    const a = (st.agents || []).find((x) => x.ai_agent_uuid === id) || {};
    const tile = (num, label, color) => h('div', { class: 'ai-tile ' + (color || '') }, h('div', { class: 'num' }, String(num)), h('div', { class: 'lbl' }, t(label)));
    const outcome = (o) => { const x = AI_OUTCOMES[o] || [o || '', '']; return badge(t(x[0]), x[1]); };
    const pages = Math.max(1, Math.ceil(calls.total / limit));
    fill(box,
      st.configured ? null : h('div', { class: 'ai-note amber' }, h('div', { class: 'ai-note-ic' }, icon('info', 16)), h('div', null,
        t('The AI service has no API key yet, so the agent cannot answer calls.'), ' ', h('a', { href: '#/ai-settings' }, t('Set it up')), '.')),
      h('div', { class: 'ai-tiles' },
        tile(a.active || 0, 'On a call now', Number(a.active) ? 'green' : ''), tile(a.today || 0, 'Calls today'), tile(a.calls || 0, 'Last 30 days'),
        tile(a.transferred || 0, 'Connected to people'), tile(fmtDur(Number(a.avg_duration || 0)), 'Average call'), tile(a.failed || 0, 'Failed', Number(a.failed) ? 'red' : '')),
      h('div', { class: 'panel' },
        h('div', { class: 'panel-head' }, h('h2', null, t('Calls')), h('span', { class: 'count-pill' }, String(calls.total)), h('span', { class: 'spacer' }),
          h('button', { type: 'button', class: 'icon-btn', title: t('Refresh'), 'aria-label': t('Refresh'), onclick: draw }, icon('refresh', 16))),
        table([
          { label: 'Time', render: (r) => fmtTime(r.started_at) },
          { label: 'Caller', render: (r) => party(r.caller_name, r.caller_number) },
          { label: 'Called', key: 'destination_number' },
          { label: 'Length', render: (r) => r.ended_at ? fmtDur(Number(r.duration || 0)) : '' },
          { label: 'Result', render: (r) => outcome(r.outcome) },
          { label: 'Connected to', key: 'transferred_to' },
          { label: 'Turns', key: 'turns', num: 1 },
          { label: '', render: (r) => h('button', { type: 'button', class: 'icon-btn', title: t('What was said'), 'aria-label': t('What was said'),
            onclick: (e) => { e.stopPropagation(); aiTranscript(r); } }, icon('transcript', 16)) },
        ], calls.rows, { empty: 'No calls yet. Call the agent\'s number to try it.', click: (r) => aiTranscript(r) }),
        pages > 1 ? h('div', { class: 'pager' }, h('span', { class: 'muted' }, (offset + 1) + '–' + Math.min(calls.total, offset + limit) + ' / ' + calls.total),
          h('span', { class: 'spacer' }),
          h('button', { class: 'btn small', type: 'button', disabled: offset === 0, onclick: () => { offset -= limit; draw(); } }, 'Previous'),
          h('button', { class: 'btn small', type: 'button', disabled: offset + limit >= calls.total, onclick: () => { offset += limit; draw(); } }, 'Next')) : null));
  };
  draw();
}

// What was said on a call, as a conversation.
async function aiTranscript(r) {
  let c;
  try { c = await api('GET', dpath('ai/calls/' + r.ai_call_uuid)); } catch (e) { fail(e); return; }
  const turns = c.transcript || [];
  const at = (s) => { s = Math.round(Number(s) || 0); return Math.floor(s / 60) + ':' + pad(s % 60); };
  modal(t('Call with') + ' ' + (c.agent_name || t('the AI agent')), h('div', { class: 'ai-convo' },
    h('div', { class: 'muted small ai-convo-head' }, party(c.caller_name, c.caller_number), ' · ', fmtTime(c.started_at),
      c.ended_at ? ' · ' + fmtDur(Number(c.duration || 0)) : '', c.error ? h('div', { class: 'error-text small' }, c.error) : null),
    turns.length ? turns.map((x) => x.role === 'event'
      ? h('div', { class: 'ai-evt' }, h('span', null, at(x.t) + ' · ' + x.text))
      : h('div', { class: 'ai-msg ' + (x.role === 'agent' ? 'agent' : 'caller') },
        h('div', { class: 'ai-who' }, x.role === 'agent' ? [icon('bot', 13), ' ', c.agent_name || t('Agent')] : t('Caller'), h('span', { class: 'muted' }, ' · ' + at(x.t))),
        h('div', { class: 'ai-bubble' }, x.text)))
      : h('p', { class: 'muted' }, t('Nothing was said.'))), false);
  const box = $$('.modal').pop();
  if (box) box.classList.add('wide');
}

// ---- Settings → AI agent service

PAGES['ai-settings'] = async () => {
  const main = $('#main');
  const st = await api('GET', dpath('ai/settings'));
  const key = h('input', { type: 'password', autocomplete: 'off', spellcheck: 'false',
    placeholder: st.has_key ? t('Saved') + ' (' + st.key_hint + ') · ' + t('enter a new key to replace it') : 'sk-…' });
  const model = h('input', { type: 'text', value: st.model || '', placeholder: st.default_model });
  model.value = st.model || '';
  const base = h('input', { type: 'text', value: st.base_url || '', placeholder: st.default_base_url });
  base.value = st.base_url || '';
  const result = h('div', { class: 'ai-test' });
  const state = () => st.has_key ? [badge('Key saved', 'green'), ' ', h('span', { class: 'muted small' }, st.key_hint)]
    : st.env_key ? [badge('Server key', 'blue'), ' ', h('span', { class: 'muted small' }, t('The server\'s RQ_AI_API_KEY is used.'))]
      : st.key_lost ? [badge('Key unreadable', 'red'), ' ', h('span', { class: 'muted small' }, t('The server\'s key file changed: enter the API key again.'))]
        : [badge('No key', 'amber'), ' ', h('span', { class: 'muted small' }, t('AI agents cannot answer calls until a key is saved.'))];
  const test = async (btn) => {
    btn.disabled = true;
    fill(result, h('span', { class: 'muted' }, h('span', { class: 'ai-spin' }), ' ', t('Connecting…')));
    try {
      const r = await api('POST', dpath('ai/settings/test'), { api_key: key.value.trim(), model: model.value.trim(), base_url: base.value.trim() });
      fill(result, r.ok ? [badge('Connected', 'green'), ' ', h('span', { class: 'muted small' }, r.model + ' · ' + r.ms + ' ms')]
        : [badge('Failed', 'red'), ' ', h('span', { class: 'error-text small' }, r.error)]);
    } catch (e) { fill(result, [badge('Failed', 'red'), ' ', h('span', { class: 'error-text small' }, e.message)]); } finally { btn.disabled = false; }
  };
  const save = async (clear) => {
    try {
      await api('PUT', dpath('ai/settings'), { api_key: clear ? '' : key.value.trim(), clear_key: !!clear, model: model.value.trim(), base_url: base.value.trim() });
      toast('Saved');
      route();
    } catch (e) { fail(e); }
  };
  const testBtn = h('button', { type: 'button', class: 'btn' }, icon('plug', 15), 'Test connection');
  testBtn.addEventListener('click', () => test(testBtn));
  fill(main,
    pageHead('AI agent service'),
    h('div', { class: 'panel' },
      h('p', { class: 'muted' }, t('AI agents talk with callers through a cloud voice AI service that works in real time: it hears the caller and answers in a natural voice within a moment. The server needs internet access, and the service charges your account per minute of audio.')),
      h('div', { class: 'form' },
        h('div', { class: 'field full' }, h('label', null, t('Status')), h('div', null, state())),
        h('div', { class: 'field full' }, h('label', null, t('API key')), eyeToggle(key),
          h('div', { class: 'help' }, t('Create one in your voice AI service account (developer platform → API keys). It is stored encrypted and never shown again.'))),
        h('div', { class: 'field' }, h('label', null, t('Model')), model, h('div', { class: 'help' }, t('Leave empty for') + ' ' + st.default_model + '.')),
        h('div', { class: 'field' }, h('label', null, t('Service address')), base, h('div', { class: 'help' }, t('Leave empty for the default service. Another address only for a compatible service.')))),
      h('div', { class: 'form-actions' }, h('button', { type: 'button', class: 'btn primary', onclick: () => save(false) }, 'Save'), testBtn,
        st.has_key ? h('button', { type: 'button', class: 'btn danger', onclick: () => { if (confirm(t('Remove the saved API key?'))) save(true); } }, 'Remove key') : null,
        result)),
    h('div', { class: 'panel' }, h('h2', null, t('How calls reach an AI agent')),
      h('ol', { class: 'ai-steps' },
        h('li', null, t('Save the API key here and test the connection.')),
        h('li', null, h('a', { href: '#/aiagents/new' }, t('Create an AI agent')), ' ', t('with its greeting, prompt and knowledge.')),
        h('li', null, t('In'), ' ', h('a', { href: '#/callflow' }, t('Call Flow')), ', ', t('drag a DID number onto the IVR card and choose the AI agent (or choose it as an IVR key).')),
        h('li', null, t('Call the number: the agent greets you. Every call is listed under the agent\'s Status & log, with what was said.')))));
};
