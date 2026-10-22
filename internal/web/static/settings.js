'use strict';
// Global settings (RingQ style tiles), SIP profiles, network, and the
// outbound rule form.

// ---------------------------------------------------------------------------
// Global settings

const GLOBAL_TILES = [
  ['Setup', 'blue', [
    ['routes', 'Outbound Rules', 'outgoing'], ['officehours', 'Language & Office Hours', 'clock'], ['sounds', 'Music On Hold', 'music'],
    ['security', 'Security', 'shield'], ['system', 'Service Restart', 'power', 'server'], ['domains', 'Domains', 'globe', 'server'],
    ['integrations', 'Integrations', 'plug']]],
  ['Contact Center', 'purple', [
    ['auxcodes', 'Aux Codes', 'coffee'], ['queues', 'Queues', 'list'], ['contacts', 'Contacts', 'contacts', 'all'],
    ['network', 'Network', 'plug', 'server'], ['sipprofiles', 'SIP Profile', 'monitor', 'server']]],
  ['Tools', 'green', [
    ['dialplans', 'Dialplans', 'code'], ['callflow', 'Call Flow', 'route'], ['timeconditions', 'Time Conditions', 'clock'], ['roles', 'User Roles', 'roles'], ['transcription', 'Call Transcription', 'captions']]],
  ['Troubleshooting', 'orange', [
    ['logs', 'Audit Logs', 'history', 'server'], ['cdr', 'Call History', 'log', 'all'], ['callcentre', 'Call Centre (live)', 'activity', 'all']]],
  ['Advanced', 'indigo', [
    ['callflow', 'Call Landing', 'incoming'], ['rep-queues', 'Reports', 'chart', 'all'], ['meetings', 'Meetings', 'video', 'ext']]],
];

PAGES.global = async () => {
  const main = $('#main');
  const ok = (who) => allowed(who || 'owner');
  fill(main,
    h('div', { class: 'page-head' }, h('h1', null, 'Global Settings ', h('span', { class: 'info-dot', title: 'Settings for the whole phone system' }, icon('info', 15)))),
    GLOBAL_TILES.map(([group, color, tiles]) => {
      const shown = tiles.filter(([id, , , who]) => ok(who) && (S.me.database || !DB_ONLY.has(id)));
      if (!shown.length) return null;
      return h('section', { class: 'gs-group' }, h('h2', null, group),
        h('div', { class: 'gs-tiles' }, shown.map(([id, label, ic]) => h('a', { class: 'gs-tile', href: '#/' + id },
          h('span', { class: 'gs-icon ' + color }, icon(ic, 20)), h('span', null, label)))));
    }));
};

// ---------------------------------------------------------------------------
// SIP profiles

PAGES.sipprofiles = async () => {
  const main = $('#main');
  const data = await api('GET', 'sipprofiles');
  const rows = data.profiles;
  const edit = (p) => {
    const f = {
      name: h('input', { value: p ? p.name : '', placeholder: 'e.g. external', required: true }),
      description: h('input', { value: p ? p.description : '' }),
      port: h('input', { type: 'number', min: '1024', max: '65535', value: p ? p.port : 5080, required: true }),
      protocol: h('select', null, h('option', { value: 'udp,tcp' }, 'UDP and TCP'), h('option', { value: 'udp' }, 'UDP only')),
      sip_ip: h('input', { value: p && p.sip_ip ? p.sip_ip : '', placeholder: 'This server (automatic)' }),
      ext_sip_ip: h('input', { value: p && p.ext_sip_ip ? p.ext_sip_ip : '', placeholder: 'This server\'s public address (automatic)' }),
      enabled: h('input', { type: 'checkbox', checked: p ? p.enabled : true }),
      tls: h('input', { type: 'checkbox', disabled: true }),
    };
    f.protocol.value = p ? p.protocol : 'udp,tcp';
    // The main port comes from ringqswitch.xml (sip-port): the rest can change.
    const mainPort = p && p.main;
    if (mainPort) f.port.disabled = true;
    const field = (label, el, help, full) => h('div', { class: 'field' + (full ? ' full' : '') }, h('label', null, label), el, help ? h('div', { class: 'help' }, help) : null);
    modal(p ? 'Edit SIP profile ' + p.name : 'Add SIP profile', h('div', { class: 'form' },
      field('Profile name', f.name), field('Port', f.port, mainPort ? 'The main port: change it with sip-port in /etc/ringqswitch/ringqswitch.xml and restart.' : 'e.g. 5080 for carriers (the "external" profile); open it in the firewall.'),
      field('Protocol', f.protocol), field('SIP IP', f.sip_ip, 'Empty: all addresses of this server.'),
      field('Ext. IP', f.ext_sip_ip, 'Kept for RingQ; RingQswitch always gives out this server\'s own address.'),
      field('Description', f.description),
      h('div', { class: 'field' }, h('label', { class: 'switch' }, f.enabled, 'Enabled: open this port')),
      h('div', { class: 'field' }, h('label', { class: 'switch muted' }, f.tls, 'TLS (not supported by RingQswitch yet)'))),
    p ? 'Save' : 'Add profile', async () => {
      const inDB = p && p.uuid; // the main port without a database row gets one
      await api(inDB ? 'PUT' : 'POST', 'sipprofiles' + (inDB ? '/' + p.uuid : ''), { name: f.name.value, description: f.description.value,
        port: f.port.value, protocol: f.protocol.value, sip_ip: f.sip_ip.value, ext_sip_ip: f.ext_sip_ip.value, enabled: f.enabled.checked });
      toast('Saved. The port opens or closes at once.');
      PAGES.sipprofiles();
    });
  };
  const ipCell = (p) => {
    if (!p.sip_ip) return h('span', { class: 'mono' }, p.bind_ip || 'all addresses');
    return h('span', { class: 'mono' }, p.sip_ip, p.ip_here ? null : h('span', { class: 'warn-ic', title: 'Not an address of this server: RingQswitch listens on ' + (p.bind_ip || 'all addresses') + ' instead' }, icon('info', 14)));
  };
  fill(main,
    h('div', { class: 'page-head' }, h('h1', null, 'SIP Profile ', h('span', { class: 'info-dot', title: 'Each enabled profile opens a SIP port (standard: internal 5060, external 5080)' }, icon('info', 15)),
      h('span', { class: 'count-pill' }, String(rows.length))),
    h('span', { class: 'spacer' }),
    data.database ? h('button', { class: 'btn primary', onclick: () => edit(null) }, icon('plus', 16), 'Add profile') : null,
    h('a', { class: 'btn', href: '#/global' }, icon('left', 16), 'Back')),
    data.database && !data.in_database ? h('div', { class: 'panel notice-panel' },
      h('div', null, h('b', null, 'No SIP profiles in the database yet'),
        h('p', { class: 'muted' }, 'RingQ uses two: internal on ' + (rows[0] ? rows[0].port : 5060) + ' for phones, and external on 5080 for carriers. Add them to see and edit them here; the external port opens at once.')),
      h('button', { class: 'btn primary', onclick: async (e) => {
        e.currentTarget.disabled = true;
        try { const r = await api('POST', 'sipprofiles/defaults'); toast('Added: ' + (r.added.join(', ') || 'nothing')); PAGES.sipprofiles(); } catch (err) { fail(err); }
      } }, icon('plus', 16), 'Add the standard profiles')) : null,
    table([
      { label: 'Profile name', render: (p) => h('span', null, h('b', null, p.name), p.description ? h('div', { class: 'muted small' }, p.description) : null) },
      { label: 'SIP IP', render: ipCell },
      { label: 'Port', render: (p) => h('span', { class: 'mono' }, String(p.port)) },
      { label: 'Protocol', render: (p) => h('span', { class: 'mono' }, p.protocol) },
      { label: 'Ext. IP', render: (p) => h('span', { class: 'mono', title: 'Given out to phones and carriers: ' + p.advertise }, p.ext_sip_ip || p.advertise || '—') },
      { label: 'TLS', render: (p) => p.tls ? h('span', { class: 'muted', title: 'Set in the database; RingQswitch does not serve SIP over TLS yet' }, '✓ (not served)') : h('span', { class: 'muted' }, '—') },
      { label: 'Status', render: (p) => !p.enabled ? badge('Disabled') : p.listening ? badge(p.main ? 'Listening (main port)' : 'Listening', 'green') : badge(p.error || 'Not open', 'red') },
      { label: 'Actions', actions: 1, render: (p) => h('div', { class: 'btn-group' },
          data.database ? h('button', { class: 'icon-btn', title: 'Edit', 'aria-label': 'Edit ' + p.name, onclick: () => edit(p) }, icon('edit', 16)) : null,
          h('button', { class: 'icon-btn danger', title: p.main ? 'The main port cannot be deleted' : 'Delete', 'aria-label': 'Delete ' + p.name, disabled: p.main || !p.uuid, onclick: async () => {
            if (!confirm('Delete the SIP profile ' + p.name + '? Its port closes.')) return;
            try { await api('DELETE', 'sipprofiles/' + p.uuid); toast('Deleted'); PAGES.sipprofiles(); } catch (e) { fail(e); }
          } }, icon('trash', 16))) },
    ], rows, { empty: 'No profiles.' }),
    h('p', { class: 'page-intro' }, 'Calls and registrations are answered from the port they arrive on, and gateways set to a profile ("external") send from its port. ',
      'Open each port in the firewall, e.g. ', h('code', null, 'sudo ufw allow 5080'), '.'));
};

// ---------------------------------------------------------------------------
// Network

PAGES.network = async () => {
  const main = $('#main');
  const sys = await api('GET', 'system');
  const n = sys.network;
  const kv = (k, v, help) => h('div', { class: 'kv' }, h('span', null, k), h('b', { class: 'mono' }, v), help ? h('div', { class: 'muted small' }, help) : null);
  const sipPorts = [n.sip_port].concat(n.profile_ports || []);
  const rules = [];
  for (const p of sipPorts) rules.push('sudo ufw allow ' + p + '/udp' + (n.sip_tcp ? ' && sudo ufw allow ' + p + '/tcp' : ''));
  rules.push('sudo ufw allow ' + n.rtp_start + ':' + n.rtp_end + '/udp');
  if (n.web_port) rules.push('sudo ufw allow ' + n.web_port + '/tcp');
  if (n.webrtc_on && n.webrtc_port) rules.push('sudo ufw allow ' + n.webrtc_port + '/udp');
  fill(main,
    h('div', { class: 'page-head' }, h('h1', null, 'Network'), h('span', { class: 'spacer' }), h('a', { class: 'btn', href: '#/global' }, icon('left', 16), 'Back')),
    h('div', { class: 'net-grid' },
      h('div', { class: 'panel' }, h('h2', null, 'Phones and carriers (SIP)'),
        kv('SIP address', n.sip_ip || '—', n.sip_bind_ip ? 'Listening on ' + n.sip_bind_ip : 'Listening on all addresses'),
        kv('Public address given out', n.ext_sip_ip || n.sip_ip || '—', n.ext_sip_ip ? 'ext-sip-ip' : 'No ext-sip-ip: the address above is given out'),
        kv('SIP ports', sipPorts.join(', ') + (n.sip_tcp ? ' (UDP and TCP)' : ' (UDP)'), 'More ports: Global Settings → SIP Profile')),
      h('div', { class: 'panel' }, h('h2', null, 'Call audio (RTP)'),
        kv('Audio address', n.ext_rtp_ip || n.rtp_ip || '—', n.ext_rtp_ip ? 'ext-rtp-ip' : 'No ext-rtp-ip set'),
        kv('Audio ports', n.rtp_start + '–' + n.rtp_end + ' UDP', 'Phones and carriers send their audio here: when closed, you hear nothing (one-way audio).')),
      h('div', { class: 'panel' }, h('h2', null, 'Web, dialer and meetings'),
        kv('Web interface', n.web_port ? 'TCP ' + n.web_port : 'off'),
        kv('Browser audio and video', n.webrtc_on ? 'UDP ' + n.webrtc_port + (n.webrtc_tcp ? ', or TCP ' + n.webrtc_tcp + ' when UDP is blocked' : '') : 'off',
          n.public_ip ? 'public-ip ' + n.public_ip : null))),
    h('div', { class: 'panel' }, h('h2', null, 'Firewall'),
      h('p', null, 'These ports must be open on the server\'s firewall and in your cloud provider\'s firewall if you use one:'),
      h('pre', { class: 'code-block' }, rules.join('\n')),
      h('p', { class: 'muted small' }, 'Addresses and ports are set in /etc/ringqswitch/ringqswitch.xml (sip-ip, ext-sip-ip, rtp-start-port…) and SIP profiles.')));
};

// ---------------------------------------------------------------------------
// Outbound rule (RingQ's "New outbound rule")

async function outboundRuleForm(id) {
  const main = $('#main');
  await loadMeta();
  const row = id ? await api('GET', dpath('routes/' + id)) : {
    route_strip: '0', route_order: '100', route_enabled: 'true', route_block: 'false',
    sip_from_user: 'cid', sip_from_display: 'cid_name', sip_from_host: 'domain', cid_type: 'pid',
  };
  let dids = [];
  try { dids = await api('GET', dpath('dids')); } catch (e) { /* no caller ID list */ }
  const gws = gatewayOptions();
  const info = (text) => h('span', { class: 'info-dot', title: text }, icon('info', 14));
  const sel = (opts, value) => { const s = h('select', null, opts.map(([v, l]) => h('option', { value: v }, l))); s.value = value || ''; if (s.value !== (value || '')) s.value = opts[0][0]; return s; };
  const field = (label, el, help, req) => h('div', { class: 'or-field' }, h('label', { class: req ? 'req' : null }, label), el, help ? h('div', { class: 'help' }, help) : null);

  // Matching calls
  const name = h('input', { value: row.route_name || '', placeholder: 'Enter a rule name' });
  const fromExts = (row.route_from_extensions || '').split(',').filter(Boolean);
  const extMode = sel([['all', 'All extensions'], ['some', 'Selected extensions']], fromExts.length ? 'some' : 'all');
  const extBox = h('div', { class: 'chip-pick' + (fromExts.length ? '' : ' hidden') },
    (S.meta.extensions || []).map((e) => h('label', { class: 'pick' }, h('input', { type: 'checkbox', value: e.extension, checked: fromExts.includes(e.extension) }),
      ' ' + e.extension + (e.name && e.name !== e.extension ? ' · ' + e.name : ''))));
  extMode.addEventListener('change', () => extBox.classList.toggle('hidden', extMode.value !== 'some'));
  const ranges = (row.route_from_ranges || '').split(',').filter(Boolean);
  const chipList = h('div', { class: 'chips' });
  const rangeIn = h('input', { placeholder: 'Enter a range (e.g., 5-10) and press Space to add.' });
  const drawChips = () => fill(chipList, ranges.map((r, i) => h('span', { class: 'chip' }, r, h('button', { type: 'button', 'aria-label': 'Remove ' + r, onclick: () => { ranges.splice(i, 1); drawChips(); } }, '×'))));
  const addRange = () => {
    const v = rangeIn.value.trim().replace(/\s+/g, '');
    if (!v) return;
    if (!/^\d{1,9}(-\d{1,9})?$/.test(v)) { toast('Write a range like 100-199', true); return; }
    if (!ranges.includes(v)) ranges.push(v);
    rangeIn.value = '';
    drawChips();
  };
  rangeIn.addEventListener('keydown', (e) => { if (e.key === ' ' || e.key === 'Enter' || e.key === ',') { e.preventDefault(); addRange(); } });
  rangeIn.addEventListener('blur', addRange);
  drawChips();
  const prefix = h('input', { value: row.route_prefix || '', placeholder: 'e.g. 6 or +65', inputmode: 'tel' });
  const length = h('input', { value: row.route_length || '', placeholder: 'e.g. 8, or 8,10, or 7-15' });
  const custom = !row.route_prefix && !row.route_length && row.route_pattern;
  const pattern = h('input', { class: 'mono', value: custom ? row.route_pattern : '', placeholder: '^\\+?(\\d{7,15})$' });
  const patBox = h('div', { class: 'or-field' + (custom ? '' : ' hidden') }, h('label', null, 'Or a pattern (advanced)'), pattern,
    h('div', { class: 'help' }, 'A regular expression. Used only when prefix and length are empty.'));
  const patLink = h('button', { class: 'link-btn', type: 'button', onclick: () => { patBox.classList.remove('hidden'); patLink.remove(); } }, 'Use a pattern instead');

  // Route settings
  const gwList = (row.gateway_uuids || '').split(',').filter(Boolean);
  const route = sel([['', 'Choose a gateway'], ['BLOCK', 'BLOCK']].concat(gws), truthy(row.route_block) ? 'BLOCK' : gwList[0] || '');
  const backup = sel([['', 'None']].concat(gws), gwList[1] || '');
  const strip = sel(Array.from({ length: 11 }, (_, i) => [String(i), i ? String(i) : 'None']), String(row.route_strip || '0'));
  const prepend = h('input', { value: row.route_prepend || '', placeholder: 'e.g. + or 65' });
  const cidOpts = [['', 'Extension\'s caller ID']].concat(dids.map((d) => [d.destination_number, d.destination_number + (d.destination_description ? ' · ' + d.destination_description : '')]));
  if (row.caller_id_number && !cidOpts.some(([v]) => v === row.caller_id_number)) cidOpts.push([row.caller_id_number, row.caller_id_number]);
  const cid = sel(cidOpts, row.caller_id_number || '');
  const order = h('input', { type: 'number', value: row.route_order || '100', min: '0', max: '400' });
  const enabled = h('input', { type: 'checkbox', checked: row.route_enabled !== 'false' });

  // Outbound call settings
  const fromUser = sel([['cid', 'Caller ID from Outbound rule'], ['', 'Gateway account'], ['extension', 'Extension number']], row.sip_from_user);
  const fromDisplay = sel([['cid_name', 'Caller ID from Outbound rule'], ['cid_number', 'Caller ID number'], ['extension', 'Extension name'], ['', 'Default']], row.sip_from_display);
  const fromHost = sel([['domain', 'Domain'], ['', 'Gateway']], row.sip_from_host);
  const cidType = sel([['pid', 'PAI'], ['rpid', 'RPID'], ['none', 'None'], ['', 'Gateway default']], row.cid_type);

  const err = h('div', { class: 'error-text' });
  const save = async (btn) => {
    err.textContent = '';
    const blocked = route.value === 'BLOCK';
    const body = {
      route_name: name.value, route_prefix: prefix.value.trim(), route_length: length.value.trim(), route_pattern: pattern.value.trim(),
      route_from_extensions: extMode.value === 'some' ? $$('input:checked', extBox).map((c) => c.value).join(',') : '',
      route_from_ranges: ranges.join(','), route_block: String(blocked),
      gateway_uuids: blocked ? '' : [route.value, backup.value].filter(Boolean).join(','),
      route_strip: strip.value, route_prepend: prepend.value.trim(), caller_id_number: cid.value,
      route_order: order.value, route_enabled: String(enabled.checked),
      sip_from_user: fromUser.value, sip_from_display: fromDisplay.value, sip_from_host: fromHost.value, cid_type: cidType.value,
    };
    if (body.route_name.trim().length < 4 && body.route_name.trim() !== (row.route_name || '')) { err.textContent = 'Rule name: at least 4 characters.'; name.focus(); return; }
    try {
      btn.disabled = true;
      await api(id ? 'PUT' : 'POST', dpath('routes' + (id ? '/' + id : '')), body);
      toast('Saved');
      location.hash = '#/routes';
    } catch (e) { err.textContent = e.message; } finally { btn.disabled = false; }
  };
  const saveBtn = h('button', { class: 'btn primary', type: 'button', onclick: (e) => save(e.currentTarget) }, id ? 'Save rule' : 'Create rule');
  fill(main,
    h('div', { class: 'or-page' },
      h('div', { class: 'page-head' }, h('h1', null, (id ? 'Edit outbound rule ' : 'New outbound rule '), info('Which outside calls this rule takes, and how they leave')),
        h('span', { class: 'spacer' }), h('a', { class: 'btn', href: '#/routes' }, icon('left', 16), 'Back')),
      h('section', { class: 'or-card' }, h('h2', null, 'Matching calls ', info('A call uses the first enabled rule (lowest order) that matches it')),
        h('div', { class: 'or-grid two' },
          h('div', null,
            field('Rule name', name, 'At least 4 characters. Shown in the outbound rules list.', true),
            field('Calls from extensions', extMode), extBox,
            field('Calls from extension range', h('div', null, chipList, rangeIn), 'Enter a range and press space to add it.')),
          h('div', null,
            field('Calls to numbers starting with prefix', prefix),
            field('Calls to numbers with a length of', length, 'Digits, the prefix included.'),
            patBox, custom ? null : patLink))),
      h('section', { class: 'or-card' }, h('h2', null, 'Route settings ', info('Where the call goes and how its number is changed')),
        h('div', { class: 'or-grid four' },
          field('Route', route), field('Strip digits', strip), field('Prepend', prepend), field('Outbound caller ID', cid)),
        h('div', { class: 'or-grid four' },
          field('Backup route', backup, 'Tried when the route fails.'), field('Order', order, 'Lower runs first.'),
          h('div', { class: 'or-field' }, h('label', null, 'Enabled'), h('label', { class: 'switch' }, enabled, 'Use this rule')))),
      h('section', { class: 'or-card' }, h('h2', null, 'Outbound call settings ', info('How the call presents itself to the carrier (SIP From header and caller ID header)')),
        h('div', { class: 'or-grid four' },
          field('SIP from user', fromUser), field('SIP from display name', fromDisplay), field('SIP from host', fromHost), field('CID type', cidType))),
      err,
      h('div', { class: 'or-actions' },
        id ? h('button', { class: 'btn danger', type: 'button', onclick: async () => {
          if (!confirm('Delete this outbound rule?')) return;
          try { await api('DELETE', dpath('routes/' + id)); toast('Deleted'); location.hash = '#/routes'; } catch (e) { fail(e); }
        } }, 'Delete') : null,
        h('span', { class: 'spacer' }),
        h('a', { class: 'btn', href: '#/routes' }, 'Cancel'), saveBtn)));
  if (!id) name.focus();
}

// The rule's matching, in words, for the list.
function ruleSummary(r) {
  const parts = [];
  const exts = (r.route_from_extensions || '').split(',').filter(Boolean);
  const ranges = (r.route_from_ranges || '').split(',').filter(Boolean);
  parts.push(exts.length || ranges.length ? 'From ' + exts.concat(ranges).join(', ') : 'All extensions');
  if (r.route_prefix) parts.push('prefix ' + r.route_prefix);
  if (r.route_length) parts.push('length ' + r.route_length);
  if (!r.route_prefix && !r.route_length) parts.push(r.route_pattern);
  return parts.join(' · ');
}
