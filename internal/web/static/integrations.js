'use strict';
// Settings → Integrations (like RingQ's): single sign-on with Microsoft
// (Entra ID), Google (Workspace) and LinkedIn, set up per domain in
// windows, and the sign-in options. Also the provider buttons of the
// sign-in page.

// The providers' marks (inline SVG, drawn here: the page loads nothing
// from other sites).
const SSO_LOGOS = {
  microsoft: '<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="2" y="2" width="9.5" height="9.5" fill="#f25022"/><rect x="12.5" y="2" width="9.5" height="9.5" fill="#7fba00"/><rect x="2" y="12.5" width="9.5" height="9.5" fill="#00a4ef"/><rect x="12.5" y="12.5" width="9.5" height="9.5" fill="#ffb900"/></svg>',
  google: '<svg viewBox="0 0 24 24" aria-hidden="true"><path fill="#4285F4" d="M23.5 12.27c0-.85-.08-1.67-.22-2.45H12v4.64h6.45a5.5 5.5 0 0 1-2.39 3.62v3h3.87c2.26-2.09 3.57-5.17 3.57-8.81z"/><path fill="#34A853" d="M12 24c3.24 0 5.95-1.07 7.93-2.9l-3.87-3c-1.07.72-2.45 1.15-4.06 1.15-3.12 0-5.77-2.11-6.71-4.95H1.29v3.1A12 12 0 0 0 12 24z"/><path fill="#FBBC05" d="M5.29 14.3a7.2 7.2 0 0 1 0-4.6V6.6H1.29a12 12 0 0 0 0 10.8l4-3.1z"/><path fill="#EA4335" d="M12 4.75c1.76 0 3.34.6 4.58 1.8l3.43-3.43A11.5 11.5 0 0 0 12 0 12 12 0 0 0 1.29 6.6l4 3.1C6.23 6.86 8.88 4.75 12 4.75z"/></svg>',
  linkedin: '<svg viewBox="0 0 24 24" aria-hidden="true"><rect width="24" height="24" rx="4" fill="#0A66C2"/><path fill="#fff" d="M7.1 9.4H4.6V19h2.5zM5.9 5a1.45 1.45 0 1 0 0 2.9 1.45 1.45 0 0 0 0-2.9zM19.4 13.6c0-2.6-1.4-4.4-3.8-4.4-1.2 0-2 .6-2.4 1.2V9.4h-2.4V19h2.5v-5c0-1.3.6-2.1 1.7-2.1 1 0 1.5.7 1.5 2.1v5h2.5z"/></svg>',
  ringq: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="11" fill="#1a8fe3"/><path fill="#fff" d="M16.6 14.4v1.7a1.1 1.1 0 0 1-1.2 1.1 11 11 0 0 1-4.8-1.7 10.8 10.8 0 0 1-3.3-3.3A11 11 0 0 1 5.6 7.4a1.1 1.1 0 0 1 1.1-1.2h1.7a1.1 1.1 0 0 1 1.1 1 7 7 0 0 0 .4 1.6 1.1 1.1 0 0 1-.3 1.2l-.7.7a8.9 8.9 0 0 0 3.3 3.3l.7-.7a1.1 1.1 0 0 1 1.2-.3 7 7 0 0 0 1.6.4 1.1 1.1 0 0 1 .9 1.1z"/></svg>',
};

function ssoLogo(id, size) {
  const span = h('span', { class: 'sso-logo', style: 'width:' + (size || 22) + 'px;height:' + (size || 22) + 'px' });
  span.innerHTML = SSO_LOGOS[id] || ''; // fixed markup from this file
  return span;
}

const SSO_TEXT = {
  microsoft: { sub: 'Sign in with Microsoft Entra ID', long: 'Let users sign in with their Microsoft Entra ID account.', action: 'Configure' },
  google: { sub: 'Sign in with Google Workspace', long: 'Let users sign in with their Google Workspace account.', action: 'Configure' },
  linkedin: { sub: 'Sign in with LinkedIn (OpenID Connect)', long: 'Let users sign in with their LinkedIn account.', action: 'Connect' },
};

const SSO_STEPS = {
  microsoft: [
    ['Open the ', ['Microsoft Entra admin center', 'https://entra.microsoft.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade'], ' → App registrations → New registration.'],
    ['Name it (e.g. RingQswitch), choose who can use it (usually this organization only).'],
    ['Under Redirect URI choose Web and paste the address below.'],
    ['Copy the Application (client) ID and the Directory (tenant) ID from the Overview.'],
    ['Certificates & secrets → New client secret: copy its Value (not the Secret ID).'],
  ],
  google: [
    ['Open the ', ['Google Cloud console', 'https://console.cloud.google.com/apis/credentials'], ' → APIs & Services → Credentials.'],
    ['Create credentials → OAuth client ID → Web application (set up the consent screen first if asked; Internal limits it to your Workspace).'],
    ['Under Authorised redirect URIs add the address below.'],
    ['Copy the client ID and the client secret.'],
  ],
  linkedin: [
    ['Go to the ', ['LinkedIn Developer Portal', 'https://www.linkedin.com/developers/apps'], '.'],
    ['Create a new app and fill in the required details.'],
    ['In the Products tab, add Sign In with LinkedIn using OpenID Connect (it gives the openid, profile and email scopes).'],
    ['Open the Auth tab of your new app.'],
    ['Under OAuth 2.0 settings, add this redirect URI:', 'uri'],
    ['Copy the client ID and client secret from the Auth tab and paste them below.'],
  ],
};

function copyBox(text) {
  const btn = h('button', { type: 'button', class: 'icon-btn', title: 'Copy', 'aria-label': 'Copy' }, icon('copy', 16));
  btn.addEventListener('click', async () => {
    try { await navigator.clipboard.writeText(text); toast(t('Copied')); } catch (e) {
      const r = document.createRange(); r.selectNodeContents(box.firstChild); const sel = getSelection(); sel.removeAllRanges(); sel.addRange(r);
    }
  });
  const box = h('div', { class: 'sso-uri' }, h('code', null, text), btn);
  return box;
}

PAGES.integrations = async () => {
  const main = $('#main');
  let data = null;
  let webPassword = true;
  const opts = h('div');
  const cards = h('div', { class: 'sso-cards' });

  async function load() {
    data = await api('GET', dpath('sso'));
    webPassword = data.web_password;
  }

  function steps(id, uri) {
    return h('ol', { class: 'sso-steps' }, SSO_STEPS[id].map((st) => h('li', null, st.map((x) => x === 'uri' ? copyBox(uri) :
      Array.isArray(x) ? h('a', { href: x[1], target: '_blank', rel: 'noopener noreferrer' }, x[0], ' ', icon('link', 13)) : x))));
  }

  function configure(p) {
    const id = h('input', { value: p.client_id || '', placeholder: 'Enter client ID', autocomplete: 'off', spellcheck: 'false' });
    const secret = h('input', { type: 'password', value: '', placeholder: p.secret_set ? 'Saved (type to replace)' : 'Enter client secret', autocomplete: 'new-password' });
    const tenant = h('input', { value: p.tenant_id || '', placeholder: 'Enter tenant ID', autocomplete: 'off', spellcheck: 'false' });
    const on = h('input', { type: 'checkbox', checked: p.id === 'linkedin' ? true : (p.configured ? p.enabled : true) });
    const field = (label, el, req) => h('div', { class: 'field' }, h('label', { class: req ? 'req' : null }, t(label)), el);
    const head = h('div', { class: 'sso-mhead' }, ssoLogo(p.id, 34), h('div', null, h('p', { class: 'muted' }, t(SSO_TEXT[p.id].long))));
    let body;
    if (p.id === 'linkedin') {
      body = h('div', { class: 'sso-modal' }, head,
        h('h3', null, t('Setup instructions')), steps('linkedin', p.redirect_uri),
        h('h3', null, t('App credentials')),
        h('div', { class: 'form sso-form' }, field('Client ID', id, 1), field('Client secret', secret, 1)),
        p.configured ? h('label', { class: 'switch sso-enable' }, on, t('Show LinkedIn as a sign-in option')) : null);
    } else {
      body = h('div', { class: 'sso-modal' }, head,
        h('details', { class: 'sso-help' }, h('summary', null, t('Setup instructions')), steps(p.id, p.redirect_uri)),
        h('section', { class: 'sso-box' }, h('h3', null, t('API credentials')),
          h('div', { class: 'form sso-form' }, field('Client ID', id, 1), field('Client secret', secret, 1),
            p.needs_tenant ? h('div', { class: 'field full' }, h('label', { class: 'req' }, t('Tenant ID')), tenant) : null,
            h('div', { class: 'field full' }, h('label', null, t('Redirect URI')), copyBox(p.redirect_uri),
              h('div', { class: 'help' }, t(p.id === 'microsoft' ? 'Add this redirect URI to your Microsoft Entra ID app registration (platform: Web).' : 'Add this redirect URI to your Google OAuth client.'))))),
        h('section', { class: 'sso-box sso-row' }, h('div', null, h('b', null, t('Enable SSO')), h('div', { class: 'muted small' }, t('Show') + ' ' + p.name + ' ' + t('as a sign-in option.'))),
          h('label', { class: 'cf-toggle' }, on, h('span'))));
    }
    const ok = p.id === 'linkedin' && !p.configured ? 'Connect LinkedIn' : 'Save configuration';
    const close = modal(t(p.id === 'linkedin' ? 'Connect LinkedIn' : p.name + ' SSO'), body, t(ok), async () => {
      await api('PUT', dpath('sso/' + p.id), { client_id: id.value.trim(), client_secret: secret.value.trim(), tenant_id: tenant.value.trim(), enabled: on.checked });
      toast(p.name + ': ' + t(on.checked ? 'sign-in turned on' : 'saved (sign-in off)'));
      await load(); draw();
    });
    if (p.configured) {
      const rm = h('button', { type: 'button', class: 'btn danger sso-remove' }, icon('trash', 15), t('Remove'));
      rm.addEventListener('click', async () => {
        if (!confirm(t('Remove') + ' ' + p.name + ' ' + t('sign-in?'))) return;
        try { await api('DELETE', dpath('sso/' + p.id)); toast(t('Removed')); close(); await load(); draw(); } catch (e) { fail(e); }
      });
      $('.modal .form-actions').append(rm);
    }
  }

  function card(p) {
    const status = p.enabled ? badge(t('Enabled'), 'green') : p.configured ? badge(t('Off')) : null;
    return h('div', { class: 'sso-card' + (p.enabled ? ' on' : '') },
      h('div', { class: 'sso-card-top' }, ssoLogo(p.id, 30), h('div', null, h('b', null, p.name), h('span', { class: 'muted' }, t(SSO_TEXT[p.id].sub)))),
      h('div', { class: 'sso-card-foot' }, status, h('span', { class: 'spacer' }),
        h('button', { type: 'button', class: 'mt-link sso-go', onclick: () => configure(p) }, t(p.configured ? 'Configure' : SSO_TEXT[p.id].action), ' ', icon('right', 15))));
  }

  function draw() {
    const builtIn = h('div', { class: 'sso-card on' },
      h('div', { class: 'sso-card-top' }, ssoLogo('ringq', 30), h('div', null, h('b', null, S.me.brand || 'RingQswitch'), h('span', { class: 'muted' }, t('Sign in with the extension and its web password')))),
      h('div', { class: 'sso-card-foot' }, badge(t('Built-in'), 'blue'), h('span', { class: 'spacer' }),
        h('button', { type: 'button', class: 'mt-link sso-go', onclick: () => opts.scrollIntoView({ behavior: 'smooth' }) }, t('Options'), ' ', icon('right', 15))));
    const order = ['linkedin', 'microsoft', 'google'];
    fill(cards, builtIn, order.map((id) => data.providers.find((p) => p.id === id)).filter(Boolean).map(card));
    const anyOn = data.providers.some((p) => p.enabled);
    const cb = h('input', { type: 'checkbox', checked: webPassword });
    cb.addEventListener('change', () => { webPassword = cb.checked; save.disabled = webPassword === data.web_password; });
    const save = h('button', { type: 'button', class: 'btn primary', disabled: true }, t('Save'));
    save.addEventListener('click', async () => {
      try {
        await api('PUT', dpath('sso/options'), { web_password: webPassword });
        toast(t('Saved')); await load(); draw();
      } catch (e) { fail(e); }
    });
    fill(opts, h('h2', { class: 'sso-h' }, t('Sign-in options'), h('span', { class: 'info-dot', title: t('How people of this domain sign in to the portal') }, icon('info', 15))),
      h('div', { class: 'sso-opts' },
        h('div', { class: 'sso-opt' }, h('div', null, h('b', null, t('Web password')), h('div', { class: 'muted small' }, t('Allow sign-in with the extension number and its web password.'),
          !anyOn ? ' ' + t('(Turn on a provider above before turning this off.)') : '')),
        h('label', { class: 'cf-toggle' }, cb, h('span')))),
      h('div', { class: 'sso-save' }, h('span', { class: 'muted small' }, t('The administrator account always signs in with its password.')), h('span', { class: 'spacer' }),
        h('button', { type: 'button', class: 'btn', onclick: () => { webPassword = data.web_password; draw(); } }, t('Cancel')), save));
  }

  fill(main, h('div', { class: 'muted' }, t('Loading…')));
  await load();
  fill(main,
    h('div', { class: 'page-head' }, h('h1', null, t('Integrations'), h('span', { class: 'info-dot', title: t('Connect RingQswitch with other services') }, icon('info', 15)))),
    h('div', { class: 'ht-tabs sso-tabs', role: 'tablist' }, h('button', { type: 'button', class: 'on', role: 'tab', 'aria-selected': 'true' }, icon('shield', 16), ' SSO')),
    h('h2', { class: 'sso-h' }, t('Single sign-on'), h('span', { class: 'info-dot', title: t('People sign in with their work account; its email chooses the extension (the sign-in email on the extension page, else their RingQ user or voicemail email)') }, icon('info', 15))),
    cards, opts);
  draw();
};

// The sign-in page's provider buttons (and the reason a sign-in failed).
const SSOLogin = (() => {
  async function render() {
    const box = $('#login-sso');
    if (!box) return;
    let list = [];
    try {
      const r = await fetch('/api/sso/providers', { credentials: 'same-origin' });
      if (r.ok) list = await r.json();
    } catch (e) { /* no buttons */ }
    if (!list.length) { box.classList.add('hidden'); return; }
    const needDomain = list.some((p) => p.need_domain);
    const dom = h('input', { placeholder: t('Company domain, e.g. pbx.example.com'), 'aria-label': t('Company domain'), autocomplete: 'organization' });
    fill(box, h('div', { class: 'login-or' }, h('span', null, t('or'))),
      needDomain ? dom : null,
      list.map((p) => {
        const a = h('a', { class: 'btn wide sso-btn', href: '/api/sso/start/' + p.id }, ssoLogo(p.id, 18), t('Sign in with') + ' ' + p.name);
        a.addEventListener('click', (e) => {
          if (!p.need_domain) return;
          e.preventDefault();
          if (!dom.value.trim()) { $('#login-error').textContent = t('Type your company domain first.'); dom.focus(); return; }
          location.href = '/api/sso/start/' + p.id + '?domain=' + encodeURIComponent(dom.value.trim());
        });
        return a;
      }));
    box.classList.remove('hidden');
  }
  // A failed single sign-on comes back as #sso-error=...
  function takeError() {
    const m = /^#sso-error=(.*)$/.exec(location.hash);
    if (!m) return '';
    history.replaceState(null, '', location.pathname);
    try { return decodeURIComponent(m[1].replace(/\+/g, ' ')); } catch (e) { return m[1]; }
  }
  return { render, takeError };
})();
window.SSOLogin = SSOLogin;
