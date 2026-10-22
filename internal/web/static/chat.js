'use strict';
// Internal chat: one-to-one and group conversations between extensions,
// with history, unread counts, files and typing notices.

const Chat = (() => {
  let rooms = [];
  let current = null; // room uuid
  let messages = [];
  let dir = [];
  let typing = {}; // room -> {name, until}
  let lastTypingSent = 0;
  let view = null; // DOM nodes of the open page

  function setBadge() {
    const n = rooms.reduce((t, r) => t + (r.uuid === current && view ? 0 : r.unread), 0);
    S.unread = n;
    for (const id of ['#chat-badge', '#nav-chat-badge']) {
      const b = $(id);
      if (b) { b.textContent = n > 99 ? '99+' : String(n); b.classList.toggle('hidden', n === 0); }
    }
    document.title = (n ? '(' + n + ') ' : '') + (S.me ? S.me.brand : 'RingQswitch');
  }

  async function loadRooms() {
    try { rooms = await api('GET', 'chat/rooms'); } catch (e) { return; }
    setBadge();
    if (view) drawRooms();
  }

  let inited = false;
  function init() {
    loadRooms();
    if (inited) return; // signed in again in the same page
    inited = true;
    on('chat.msg', (m) => {
      const msg = m.message;
      const room = rooms.find((r) => r.uuid === msg.room);
      if (!room) { loadRooms(); } else {
        room.last = msg;
        room.last_time = msg.time;
        if (msg.sender !== S.me.extension && !(view && current === msg.room && !document.hidden)) room.unread++;
        rooms.sort((a, b) => (b.last_time || '').localeCompare(a.last_time || ''));
      }
      if (view && current === msg.room) {
        if (!messages.some((x) => x.uuid === msg.uuid)) messages.push(msg);
        delete typing[msg.room];
        drawMessages(true);
        if (!document.hidden) wsSend({ t: 'chat.read', room: msg.room });
      }
      if (msg.sender !== S.me.extension && (document.hidden || !(view && current === msg.room))) {
        notify(msg);
      }
      setBadge();
      if (view) drawRooms();
    });
    on('chat.rooms', loadRooms);
    on('chat.typing', (m) => {
      typing[m.room] = { name: m.name || m.from, until: Date.now() + 4000 };
      if (view && current === m.room) drawTyping();
      setTimeout(() => { if (view && current === m.room) drawTyping(); }, 4100);
    });
    on('presence', () => { if (view) loadDir(); });
    document.addEventListener('visibilitychange', () => {
      if (!document.hidden && view && current) markRead(current);
    });
  }

  // A new message: a notification at the top right (and the system one
  // when the tab is in the background); a click opens the conversation.
  function notify(msg) {
    const room = rooms.find((r) => r.uuid === msg.room);
    const who = msg.sender_name || msg.sender;
    Notes.show({ key: 'chat-' + msg.room, icon: 'chat', tone: 'blue',
      title: room && room.type !== 'direct' ? who + ' in ' + room.name : who,
      body: msg.file_name ? '📎 ' + msg.file_name : msg.body,
      click: () => goTo(msg.room), actions: [{ label: 'Reply', kind: 'primary', fn: () => goTo(msg.room) }] });
  }
  function goTo(room) {
    current = room;
    if (view) open(room); else location.hash = '#/chat';
  }

  async function loadDir() {
    try { dir = await api('GET', dpath('directory')); } catch (e) { dir = []; }
    if (view) drawRooms();
  }

  function markRead(room) {
    const r = rooms.find((x) => x.uuid === room);
    if (r && r.unread) { r.unread = 0; setBadge(); drawRooms(); }
    wsSend({ t: 'chat.read', room });
  }

  async function open(room) {
    current = room;
    messages = [];
    if (view) {
      fill(view.msgs, h('div', { class: 'muted small' }, 'Loading…'));
      drawHeader();
    }
    try { messages = await api('GET', 'chat/rooms/' + room + '/messages?limit=100'); } catch (e) { fail(e); }
    markRead(room);
    if (view) { drawHeader(); drawMessages(true); view.input.focus(); }
  }

  async function openDirect(ext) {
    try {
      const r = await api('POST', 'chat/direct', { extension: ext });
      await loadRooms();
      await open(r.room);
    } catch (e) { fail(e); }
  }

  function presenceOf(ext) {
    const d = dir.find((x) => x.extension === ext);
    if (!d) return ['', ''];
    if (d.on_call) return ['amber', 'on a call'];
    if (d.dnd) return ['red', 'do not disturb'];
    if (d.agent_status === 'On Break') return ['amber', 'break' + (d.aux ? ': ' + d.aux : '')];
    if (d.web || d.phone) return ['green', 'online'];
    return ['', 'offline'];
  }

  function roomTitle(r) {
    if (!r) return '';
    return r.type === 'direct' ? r.name : r.name + ' (' + r.members.length + ')';
  }

  function drawRooms() {
    if (!view) return;
    const f = view.search.value.toLowerCase();
    const listed = new Set();
    const items = rooms.filter((r) => roomTitle(r).toLowerCase().includes(f)).map((r) => {
      let other = null;
      if (r.type === 'direct') {
        other = r.members.find((m) => m !== S.me.extension) || S.me.extension;
        listed.add(other);
      }
      const pres = other ? presenceOf(other) : ['blue', ''];
      const last = r.last ? (r.last.sender === S.me.extension ? 'You: ' : '') + (r.last.file_name ? '📎 ' + r.last.file_name : r.last.body) : 'No messages yet';
      return h('div', { class: 'croom' + (r.uuid === current ? ' on' : ''), onclick: () => open(r.uuid) },
        h('span', { class: 'dot ' + pres[0], title: pres[1] }),
        h('div', { class: 'grow' }, h('div', { class: 'croom-name' }, roomTitle(r)), h('div', { class: 'muted small ellipsis' }, last)),
        r.unread ? h('span', { class: 'nav-badge' }, r.unread) : null);
    });
    const people = dir.filter((d) => d.extension !== S.me.extension && !listed.has(d.extension) && (d.extension + ' ' + d.name).toLowerCase().includes(f))
      .map((d) => {
        const pres = presenceOf(d.extension);
        return h('div', { class: 'croom', onclick: () => openDirect(d.extension) }, h('span', { class: 'dot ' + pres[0], title: pres[1] }),
          h('div', { class: 'grow' }, h('div', { class: 'croom-name' }, d.extension + ' ' + (d.name || '')), h('div', { class: 'muted small' }, pres[1])));
      });
    fill(view.rooms, items.length ? items : h('div', { class: 'muted small pad' }, 'No conversations yet.'),
      people.length ? [h('div', { class: 'group-label' }, 'Start a chat'), people] : null);
  }

  function drawHeader() {
    if (!view) return;
    const r = rooms.find((x) => x.uuid === current);
    if (!r) { fill(view.head, h('span', { class: 'muted' }, 'Choose a conversation')); return; }
    const other = r.type === 'direct' ? r.members.find((m) => m !== S.me.extension) : null;
    const pres = other ? presenceOf(other) : null;
    fill(view.head, h('b', null, roomTitle(r)), pres ? h('span', { class: 'muted small' }, ' · ' + pres[1]) : null,
      h('span', { class: 'spacer' }),
      other && window.Phone ? h('button', { class: 'btn small', onclick: () => { Phone.toggle(true); Phone.dial(other); } }, 'Call') : null,
      r.type === 'group' ? [
        h('button', { class: 'btn small', onclick: () => editGroup(r) }, 'Members'),
        h('button', { class: 'btn small danger', onclick: async () => {
          if (!confirm('Leave the group ' + r.name + '?')) return;
          try { await api('DELETE', 'chat/rooms/' + r.uuid); current = null; await loadRooms(); drawHeader(); fill(view.msgs); } catch (e) { fail(e); }
        } }, 'Leave'),
      ] : null);
  }

  function linkify(text) {
    const out = [];
    const re = /(https?:\/\/[^\s<]+)/g;
    let last = 0;
    let m;
    while ((m = re.exec(text))) {
      if (m.index > last) out.push(text.slice(last, m.index));
      out.push(h('a', { href: m[1], target: '_blank', rel: 'noopener noreferrer' }, m[1]));
      last = re.lastIndex;
    }
    if (last < text.length) out.push(text.slice(last));
    return out;
  }

  function fileBlock(m) {
    const url = '/api/chat/files/' + m.uuid;
    const isImg = /^image\/(png|jpeg|gif|webp)$/.test(m.file_type || '');
    return h('div', { class: 'cfile' },
      isImg ? h('a', { href: url, target: '_blank', rel: 'noopener' }, h('img', { src: url, alt: m.file_name, class: 'cimg' })) : null,
      h('a', { href: url + '?download=1' }, '📎 ' + m.file_name), h('span', { class: 'muted small' }, ' ' + Math.max(1, Math.round(m.file_size / 1024)) + ' KB'));
  }

  function drawMessages(scroll) {
    if (!view) return;
    let lastDay = '';
    const nodes = [];
    for (const m of messages) {
      const d = new Date(m.time);
      const day = d.toDateString();
      if (day !== lastDay) { nodes.push(h('div', { class: 'cday' }, d.toLocaleDateString())); lastDay = day; }
      const mine = m.sender === S.me.extension;
      nodes.push(h('div', { class: 'cmsg' + (mine ? ' mine' : '') },
        mine ? null : h('div', { class: 'cfrom' }, (m.sender_name || '') + ' (' + m.sender + ')'),
        m.body ? h('div', { class: 'cbody' }, linkify(m.body)) : null,
        m.file_name ? fileBlock(m) : null,
        h('div', { class: 'ctime' }, d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }))));
    }
    fill(view.msgs, nodes.length ? nodes : h('div', { class: 'muted small pad' }, current ? 'Say hello 👋' : ''));
    drawTyping();
    if (scroll) view.msgs.scrollTop = view.msgs.scrollHeight;
  }

  function drawTyping() {
    if (!view) return;
    const t = typing[current];
    view.typing.textContent = t && t.until > Date.now() ? t.name + ' is typing…' : '';
  }

  async function sendText() {
    const text = view.input.value.trim();
    if (!text || !current) return;
    view.input.value = '';
    try { await wsSend({ t: 'chat.send', room: current, body: text }, true); } catch (e) { view.input.value = text; fail(e); }
  }

  async function sendFile(file) {
    if (!file || !current) return;
    if (file.size > 20 * 1024 * 1024) { toast('Files up to 20 MB', true); return; }
    try {
      await api('POST', 'chat/rooms/' + current + '/files?name=' + encodeURIComponent(file.name), await file.arrayBuffer(), true);
    } catch (e) { fail(e); }
  }

  function memberPicker(selected) {
    const box = h('div', { class: 'cmembers' });
    for (const d of dir) {
      if (d.extension === S.me.extension) continue;
      box.append(h('label', null, h('input', { type: 'checkbox', value: d.extension, checked: selected.includes(d.extension) }), ' ' + d.extension + ' ' + (d.name || '')));
    }
    return box;
  }

  function dialog(title, body, onOK) {
    const back = h('div', { class: 'modal-back' });
    const close = () => back.remove();
    const err = h('div', { class: 'error-text' });
    back.append(h('div', { class: 'modal' }, h('h2', null, title), body, err,
      h('div', { class: 'form-actions' }, h('button', { class: 'btn primary', onclick: async () => {
        try { await onOK(); close(); } catch (e) { err.textContent = e.message; }
      } }, 'Save'), h('button', { class: 'btn', onclick: close }, 'Cancel'))));
    back.addEventListener('click', (e) => { if (e.target === back) close(); });
    document.body.append(back);
  }

  function newGroup() {
    const name = h('input', { placeholder: 'Group name, e.g. Support team' });
    const picker = memberPicker([]);
    dialog('New group', h('div', null, h('label', null, 'Name', name), h('p', { class: 'small muted' }, 'Members'), picker), async () => {
      const members = $$('input:checked', picker).map((c) => c.value);
      const r = await api('POST', 'chat/rooms', { name: name.value, members });
      await loadRooms();
      await open(r.room);
    });
  }

  function editGroup(r) {
    const name = h('input', { value: r.name });
    const picker = memberPicker(r.members);
    dialog('Group members', h('div', null, h('label', null, 'Name', name), h('p', { class: 'small muted' }, 'Members'), picker), async () => {
      const members = $$('input:checked', picker).map((c) => c.value);
      await api('PUT', 'chat/rooms/' + r.uuid, { name: name.value, members });
      await loadRooms();
      drawHeader();
    });
  }

  async function page(args) {
    const main = $('#main');
    const search = h('input', { type: 'search', placeholder: 'Search people and chats' });
    const input = h('textarea', { class: 'cinput', placeholder: 'Write a message… (Enter to send, Shift+Enter for a new line)', rows: 2 });
    const fileInput = h('input', { type: 'file', class: 'hidden' });
    view = { search, input, rooms: h('div', { class: 'crooms' }), head: h('div', { class: 'chead' }), msgs: h('div', { class: 'cmsgs' }), typing: h('div', { class: 'ctyping muted small' }) };
    search.addEventListener('input', drawRooms);
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendText(); return; }
      if (current && Date.now() - lastTypingSent > 2500) { lastTypingSent = Date.now(); wsSend({ t: 'chat.typing', room: current }); }
    });
    fileInput.addEventListener('change', () => { sendFile(fileInput.files[0]); fileInput.value = ''; });
    view.msgs.addEventListener('dragover', (e) => e.preventDefault());
    view.msgs.addEventListener('drop', (e) => { e.preventDefault(); if (e.dataTransfer.files[0]) sendFile(e.dataTransfer.files[0]); });
    fill(main,
      pageHead('Chat', h('button', { class: 'btn', onclick: newGroup }, '+ New group')),
      h('div', { class: 'chat' },
        h('div', { class: 'cside' }, search, view.rooms),
        h('div', { class: 'cmain' }, view.head, view.msgs, view.typing,
          h('div', { class: 'cbar' }, h('button', { class: 'btn', title: 'Send a file', onclick: () => { if (current) fileInput.click(); } }, '📎'), input,
            h('button', { class: 'btn primary', onclick: sendText }, 'Send'), fileInput))));
    if (window.Notification && Notification.permission === 'default') Notification.requestPermission().catch(() => {});
    await Promise.all([loadRooms(), loadDir()]);
    drawRooms();
    if (args && args[0]) {
      await openDirect(args[0]);
    } else if (current && rooms.some((r) => r.uuid === current)) {
      await open(current);
    } else if (rooms[0]) {
      await open(rooms[0].uuid);
    } else {
      drawHeader();
      drawMessages();
    }
    // The page closes when the route changes.
    const cleanup = () => { view = null; window.removeEventListener('hashchange', cleanup); setBadge(); };
    window.addEventListener('hashchange', cleanup);
  }

  return { init, page };
})();
window.Chat = Chat;
PAGES.chat = (args) => Chat.page(args);
