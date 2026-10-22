'use strict';
// The top bar's buttons (like RingQ's): meetings calendar, Meet, chat,
// clear the cache, call status, wallboard, language, theme and the user
// menu (who I am, my status, the devices signed in as my extension, my
// profile, sign out, and the version with its previous builds).

// The builds of RingQswitch, newest first (Previous builds).
const BUILDS = [
  ['0.10.1', '2026-10-22', 'RingQswitch branding in every guide, sample file, script and portal text; complete API reference (commands, events, WebSocket messages, listening ports); the speech recogniser installed as rq-speech; make interop; use-certificate.sh.'],
  ['0.10.0', '2026-10-21', 'Language & office hours as in RingQ (time zone, office and break hours with actions, holidays with greetings and MP3 uploads, exemptions); Security settings in tabs: Allowed countries with flags, PIN-based dialing, web Allow/Deny and auto logout, global IP restriction, SIP Guard (protections with failures, window and ban; live bans; history and stats).'],
  ['0.9.1', '2026-10-21', 'Dialer: BLF and Parked slide out on the dialer\'s left as in the RingQ apps (a handle on its edge, BLF and Parked tabs with the count of parked calls), in place of the Keys tab.'],
  ['0.9.0', '2026-10-21', 'Desk phone keys: BLF lamps for desk phones and the dialer (extensions, parking slots 1-5), message lamp, DND key in step with the apps; phone keys per extension and company with Reboot phone; dialer Keys tab and DTMF sequences; dial codes *8 pickup, *591-*595 park, *26N status, *78/*79/*77 DND, own extension = voicemail menu, *69, *870, *88.'],
  ['0.8.0', '2026-10-16', 'AI agents: a number answered by a voice AI that greets callers, answers from your prompt, FAQ and web pages in their language, and connects them to people; DIDs reach it in Call Flow (IVR card). Settings → AI agent service. Call Flow\'s + opens the new module\'s form in a window.'],
  ['0.7.4', '2026-10-15', 'Call history: queue calls given up while an agent\'s phone rang show as missed too; call records wait for the queue\'s result (why the caller left); the missed-call notification shows the number once.'],
  ['0.7.3', '2026-10-15', 'Call history: queue calls nobody answered (the caller hung up while waiting) show as missed instead of answered, calls that ended in voicemail as voicemail; earlier calls shown the same way.'],
  ['0.7.2', '2026-10-15', 'Live transcription: clean text on noisy lines (the background is no longer taken for speech), unsure text, loops and wrong-language guesses left out, one language per call; the transcript panel and its tab placed right after the dialer reopens.'],
  ['0.7.1', '2026-10-15', 'Live transcription about 7 times faster, with words shown while people speak and the live recogniser loaded before the first call; the transcript panel stays beside the dialer and below the top bar; drag the dialer anywhere (it stays there), it opens out of its button, with a light border and soft shadow.'],
  ['0.7.0', '2026-10-14', 'Live transcription during calls: what both parties say is written down as they speak, split by speaker, in a Live transcript panel beside the dialer (speaking dots, words typed out, Copy); the live recogniser keeps the model loaded so text keeps up; turn it on in Settings → Call transcription.'],
  ['0.6.3', '2026-10-14', 'Voicemail transcription: each message written down, its text on the Voicemail page and the dialer, a window to read and play it; voicemail play, download and delete as icons.'],
  ['0.6.2', '2026-10-14', 'Queues: callers hear "no agent is available" and go to voicemail when every agent is on a break or offline; agents with no phone online are not rung; voicemail greeting; notifications close by themselves; names and avatars from first and last name.'],
  ['0.6.1', '2026-10-14', 'Transcripts keep the speakers in order (each turn recognised on its own; hums ignored); choose and download speech models in Settings; Call history shows each call\'s path (click a call), icon play/download, fits without sideways scrolling; all reports paged (50 rows) with Period All.'],
  ['0.6.0', '2026-10-14', 'Call transcription with sentiment: after each call, who said what (Speaker A and Speaker B, Customer and Agent) and how they felt, from the Transcript icon in Call history and Queue calls (Settings → Call transcription). Esc closes windows.'],
  ['0.5.14', '2026-10-14', 'eFax for an extension (Call Flow → Others), the dialer opens the microphone only during calls, a visible menu collapse button, an animated dashboard.'],
  ['0.5.13', '2026-10-13', 'T.38 fax: a carrier switching a fax call to T.38 now gets it (the fax no longer stops); SIP answers reach carriers that listen on their Via port; calls show as ringq/gateway/... instead of sofia/....'],
  ['0.5.12', '2026-10-13', 'Fax troubleshooting: what each fax call hears is in the log; a failed fax keeps its audio (download from the Status log).'],
  ['0.5.11', '2026-10-13', 'Fax DIDs answer as a fax machine with the RingQ fax dialplan. Call Flow Others also offers extensions.'],
  ['0.5.10', '2026-10-10', 'Fax extensions save on RingQ databases (the v_fax table is created on start).'],
  ['0.5.9', '2026-10-10', 'eFax: send faxes from your DID numbers and receive them on fax extensions (Call Flow → Others → Fax), with Inbox and Status log. Fax built in (rxfax / txfax).'],
  ['0.5.8', '2026-10-09', 'Single sign-on with Microsoft, Google and LinkedIn (Settings → Integrations). Country on DIDs. Assign instead of Link on Call Flow.'],
  ['0.5.7', '2026-10-09', 'Signing in to the portal logs the queue agent in (the wallboard counts everyone signed in); signing out of one of two browsers keeps it in.'],
  ['0.5.6', '2026-10-09', 'Call Flow drag and drop both ways: drag a linked DID back to Available DID Numbers to unlink it.'],
  ['0.5.5', '2026-10-09', 'Call Flow like RingQ: SIP trunks, gateways and DIDs on one page; drag a DID onto IVR, Queue, Extension or Others and choose which one in a window. Hotel front desk: check-in, check-out and wake-up calls. The dialer fits the window without scrolling.'],
  ['0.5.4', '2026-10-09', 'rq_cli console (banner, no password, live colour log with the dialplan trace). Web passwords can be seen on the extension page. The browser dialer registers by itself after sign-in. Signing out of the portal logs an agent out of its queues. Real wait times in reports. Dialer without a scroll bar.'],
  ['0.5.3', '2026-10-08', 'Top bar like RingQ: user menu with connected devices, Call status, clear the cache, languages. Notifications at the top right. The organizer admits meeting attendees. Standard SIP profiles. Passwords hidden with an eye.'],
  ['0.5.2', '2026-10-08', 'Global settings, outbound rules and SIP profiles like RingQ. Queue details on the wallboard. Database tools can read RingQswitch tables.'],
  ['0.5.1', '2026-10-08', 'Meet window like RingQ, the guest page and meeting content (chat, whiteboard, files, transcript). Dialer audio check.'],
  ['0.5.0', '2026-10-08', 'User roles board and the Manager role, team directory, contacts, the RingQ look (Inter font).'],
  ['0.4.2', '2026-10-08', 'Dialer and meeting audio fix; audio over TCP on the web port when UDP is blocked.'],
  ['0.4.1', '2026-10-08', 'RingQ-style portal, floating dialer, dashboard and wallboard redesign.'],
  ['0.4.0', '2026-10-07', 'Extension logins, browser dialer, chat, meetings, wallboard, call monitoring, security, Let\'s Encrypt.'],
  ['0.3.0', '2026-10-07', 'Web interface, call centre, voicemail and the PBX features.'],
  ['0.2.1', '2026-10-07', 'The RingQ database; a test database in one command.'],
];

const Topbar = (() => {
  let devices = null; // the last answer of /api/devices (null: not loaded)
  let devErr = '';

  function presence() {
    const pr = $('#top-presence');
    const st = pr ? pr.dataset.status || 'online' : 'online';
    return { online: ['Online', 'green'], break: ['On break', 'amber'], dnd: ['Do not disturb', 'red'] }[st] || ['Online', 'green'];
  }

  async function loadDevices() {
    devErr = '';
    try { devices = await api('GET', 'devices'); } catch (e) { devices = []; devErr = e.message; }
  }

  function since(sec) {
    const d = new Date(sec * 1000);
    const today = new Date().toDateString() === d.toDateString();
    return (today ? '' : d.toLocaleDateString() + ' ') + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }

  function deviceName(r) {
    const ua = r.user_agent || '';
    if (/web dialer/i.test(ua)) return t('Browser dialer');
    return ua || t('SIP phone');
  }

  function drawUserMenu() {
    const box = $('#user-menu');
    const ext = isExt();
    const name = $('#top-name').textContent;
    const [stText, stColor] = presence();
    const devList = !ext ? null : devices === null ? h('div', { class: 'um-muted' }, t('Loading…'))
      : devErr ? h('div', { class: 'um-muted' }, t('Live connections unavailable right now.'))
        : !devices.length ? h('div', { class: 'um-muted' }, t('No phone or app is signed in right now.'))
          : devices.map((r) => h('div', { class: 'um-dev' }, icon(/web dialer/i.test(r.user_agent || '') ? 'monitor' : 'device', 16),
            h('div', null, h('b', null, deviceName(r)), h('span', null, r.source.replace(/^udp:|^tcp:|^ws[s]?:/i, '') + ' · ' + t('Signed in at') + ' ' + since(r.since)))));
    fill(box,
      h('div', { class: 'um-head' }, h('span', { class: 'avatar big' }, $('#side-avatar').textContent),
        h('div', null, h('b', null, name), h('span', null, S.domain || S.me.hostname || ''))),
      ext ? h('div', { class: 'um-row' }, h('span', null, t('Ext') + ' ' + S.me.extension), h('span', { class: 'um-pill ' + stColor }, h('i'), t(stText))) :
        h('div', { class: 'um-row' }, h('span', null, t({ admin: 'Administrator', owner: 'Owner', manager: 'Manager', user: 'User' }[S.me.role] || ''))),
      ext ? h('div', { class: 'um-sec' },
        h('div', { class: 'um-sec-head' }, h('span', null, t('Connected devices')),
          h('button', { type: 'button', class: 'um-refresh', title: t('Refresh'), 'aria-label': t('Refresh'), onclick: async (e) => {
            e.stopPropagation(); devices = null; drawUserMenu(); await loadDevices(); drawUserMenu();
          } }, icon('refresh', 15))),
        devList) : null,
      h('div', { class: 'um-sec links' },
        h('a', { href: '#/profile', role: 'menuitem' }, icon('user', 17), t('View profile')),
        h('button', { type: 'button', role: 'menuitem', class: 'um-logout', onclick: signOut }, icon('logout', 17), t('Logout'))),
      h('div', { class: 'um-foot' }, h('span', null, (S.me.brand || 'RingQswitch') + ' ' + S.me.version),
        h('button', { type: 'button', class: 'mt-link um-builds', onclick: (e) => { e.stopPropagation(); closeMenus(); showBuilds(); } }, t('Previous builds'))));
  }

  function showBuilds() {
    const back = h('div', { class: 'modal-back' });
    const close = () => back.remove();
    back.addEventListener('click', (e) => { if (e.target === back) close(); });
    back.append(h('div', { class: 'modal builds', role: 'dialog', 'aria-label': t('Previous builds') },
      h('div', { class: 'builds-head' }, h('h2', null, t('Previous builds')), h('button', { type: 'button', class: 'note-x', 'aria-label': t('Close'), onclick: close }, icon('x', 16))),
      h('p', { class: 'muted' }, t('This server runs') + ' ', h('b', null, (S.me.brand || 'RingQswitch') + ' ' + S.me.version), '.'),
      h('ol', { class: 'builds-list' }, BUILDS.map(([v, d, what]) => h('li', { class: v === S.me.version ? 'now' : '' },
        h('div', null, h('b', null, 'RingQswitch ' + v), v === S.me.version ? h('span', { class: 'badge green' }, t('Installed')) : null, h('span', { class: 'muted small' }, d)),
        h('p', null, what))))));
    document.body.append(back);
  }

  function drawLangMenu() {
    const cur = I18N.lang();
    fill($('#lang-menu'), h('div', { class: 'lang-head' }, t('Language')),
      I18N.LANGS.map(([code, en, native]) => h('button', { type: 'button', role: 'menuitemradio', 'aria-checked': String(code === cur), class: code === cur ? 'on' : '',
        onclick: () => { closeMenus(); I18N.set(code); } }, h('b', null, en), code === 'en' ? null : h('span', null, native))));
  }

  function closeMenus() {
    $('#user-menu').classList.add('hidden');
    $('#lang-menu').classList.add('hidden');
  }

  async function clearCache() {
    const btn = $('#cache-btn');
    if (btn.classList.contains('spin')) return;
    btn.classList.add('spin');
    try {
      const r = await api('POST', 'cache/clear');
      await loadMeta();
      route();
      Notes.show({ key: 'cache', icon: 'refresh', tone: 'green', title: t('Cache cleared'),
        body: t('Reloaded from the database:') + ' ' + r.domains + ' ' + t('domains') + ', ' + r.extensions + ' ' + t('extensions') + ', ' + r.dialplans + ' ' + t('dialplans') + ', ' + r.gateways + ' ' + t('gateways') + ' (' + r.took_ms + ' ms)' });
    } catch (e) { fail(e); } finally { setTimeout(() => btn.classList.remove('spin'), 400); }
  }

  function init() {
    $('#cal-btn').prepend(icon('calendar', 20));
    $('#cache-btn').prepend(icon('refresh', 20));
    $('#status-btn').prepend(icon('tower', 20));
    $('#lang-btn').prepend(icon('globe', 20));
    $('#cache-btn').addEventListener('click', clearCache);
    $('#user-btn').addEventListener('click', async (e) => {
      e.stopPropagation();
      const box = $('#user-menu');
      const open = box.classList.contains('hidden');
      closeMenus();
      if (!open) return;
      drawUserMenu();
      box.classList.remove('hidden');
      if (isExt()) { await loadDevices(); if (!box.classList.contains('hidden')) drawUserMenu(); }
    });
    $('#lang-btn').addEventListener('click', (e) => {
      e.stopPropagation();
      const box = $('#lang-menu');
      const open = box.classList.contains('hidden');
      closeMenus();
      if (!open) return;
      drawLangMenu();
      // Under the globe.
      const b = $('#lang-btn').getBoundingClientRect();
      const bar = $('.top-right').getBoundingClientRect();
      if (document.documentElement.dir === 'rtl') { box.style.left = Math.max(0, b.left - bar.left) + 'px'; box.style.right = 'auto'; }
      else { box.style.right = Math.max(0, bar.right - b.right) + 'px'; box.style.left = 'auto'; }
      box.classList.remove('hidden');
    });
    $('#user-menu').addEventListener('click', (e) => e.stopPropagation());
    $('#lang-menu').addEventListener('click', (e) => e.stopPropagation());
    document.addEventListener('click', closeMenus);
    document.addEventListener('keydown', (e) => { if (e.key === 'Escape') closeMenus(); });
  }

  // update shows the buttons this user may use (after signing in).
  function update() {
    devices = null;
    $('#cal-btn').classList.toggle('hidden', !isExt() || !S.me.database);
    $('#cache-btn').classList.toggle('hidden', !isOwner());
    $('#status-btn').classList.toggle('hidden', !isSupervisor());
  }

  function mark(page) {
    $('#cal-btn').classList.toggle('on', page === 'meetings');
    $('#status-btn').classList.toggle('on', page === 'callstatus');
  }

  init();
  return { update, mark, closeMenus };
})();
window.Topbar = Topbar;
