# Running RingQswitch on the RingQ portal database

RingQswitch reads and writes the PostgreSQL tables of the RingQ portal. Your
portal, API and database stay as they are; RingQswitch is the switch behind
them:

```
 RingQ portal / API ── PostgreSQL ───────────┐
        │  (event socket :8021)               │ reads config, writes state + CDRs
        ▼                                     ▼
                 RingQswitch (one Go binary)
```

| Portal data | What RingQswitch does with it |
|---|---|
| Domains and extensions (`v_domains`, `v_extensions`) | the directory; every extension column becomes a directory variable |
| Dialplans (`v_dialplans.dialplan_xml`) | loads the same XML, same contexts and order |
| Gateways (`v_gateways`) | trunks, named by `gateway_uuid` as the portal does |
| SIP profiles (`v_sip_profiles`, `v_sip_profile_settings`) | one SIP port per enabled profile (5060, 5080...) |
| Variables (`v_vars`) | loaded as `$${globals}` |
| Ring groups (`v_ring_groups`), dialplan action `lua app.lua ring_groups` | native ring groups, all strategies |
| IVR menus (`v_ivr_menus`) | native IVR menus |
| Forwarding, dialplan action `lua app.lua failure_handler` | native busy / no answer / not registered forwarding |
| The "providers" access control list | trusted carrier addresses (calls go to `public` without auth) |
| `registrations`, `channels`, `calls` | written live, same columns |
| CDR table `v_xml_cdr` | inserted at hangup (A legs), plus any table with the same columns (e.g. `q_ringq_cdr`) |
| `memcache delete`, `reloadxml` from the portal | reload the configuration; the database is also polled for changes |
| Event socket | the protocol, commands and event names the portal uses ([API.md](API.md)) |

## 1. Before you start

> New to this? Follow [STEP-BY-STEP.md](STEP-BY-STEP.md) first: it sets up a
> test database with sample extensions on a test server in one command
> (`packaging/setup-test-db.sh`), then a copy of your real data, before you
> touch production.

* The RingQ server (or a copy of it). RingQswitch runs on the same box and
  connects to PostgreSQL locally.
* RingQswitch built and installed as in [INSTALL.md](../INSTALL.md)
  (`make build && sudo make install`).
* The database credentials. The portal keeps them in its `config.conf`:

  ```bash
  sudo grep -sE '^database\.0\.(host|port|name|username|password)' /etc/*/config.conf
  ```
* The event socket password your RingQ backend uses to connect to the
  switch (it is in the backend's settings).
* **A backup**, so you can always go back:

  ```bash
  sudo -u postgres pg_dump -Fc DBNAME > /root/ringq-before-ringqswitch.dump
  ```

## 2. Configure

```bash
sudo cp /etc/ringqswitch/ringqswitch.xml /etc/ringqswitch/ringqswitch.xml.orig
sudo cp conf/examples/ringq-postgres.xml /etc/ringqswitch/ringqswitch.xml
sudo nano /etc/ringqswitch/ringqswitch.xml
```

Set at least:

| Setting | Value |
|---|---|
| `<database>` `dsn` | `postgres://USER:PASSWORD@127.0.0.1:5432/DBNAME?sslmode=disable` (URL-encode special characters in the password, write `&` as `&amp;`) |
| `event-socket-password` | the password your backend uses |
| `event-socket-listen` | `127.0.0.1:8021`, or `0.0.0.0:8021` if the backend runs on another server (then allow only that server in the firewall) |
| `recordings-dir` | the folder your portal reads recordings from (the sample already points at the RingQ server's existing recordings folder) |
| `sounds-dir` | the prompts folder (the sample points at the existing one, else `/var/lib/ringqswitch/sounds`) |
| `fax-dir` | faxes sent and received (eFax); default: `fax` next to `recordings-dir` |
| `cdr-tables` | `v_xml_cdr`, plus `q_ringq_cdr` if nothing else fills that table (see section 6) |
| `<web>` `listen`, `admin-password` | the web portal ([WEB-UI.md](WEB-UI.md)) |

The service runs as user `ringq`; give it access to the recordings:

```bash
sudo usermod -aG www-data ringq        # or the group owning the recordings
sudo chmod -R g+rwX <recordings-dir>
```

With a database configured, the sample users and dialplan in
`/etc/ringqswitch/directory` and `/etc/ringqswitch/dialplan` are ignored
(set `xml-directory` / `xml-dialplan` to `true` to load them as well).

## 3. Check before switching over

The previous switch can keep running during these checks.

```bash
ringqswitch -conf /etc/ringqswitch -check
# level=INFO msg="database configuration loaded" domains=3 extensions=120 gateways=2 dialplans=96 ...
# configuration OK: 3 domain(s), 120 users, 5 dialplan contexts, 2 gateways, 4 ring groups, 2 IVR menus

ringqswitch -conf /etc/ringqswitch -compat
```

`-compat` lists every dialplan application and `${function}` used in your
`v_dialplans` that RingQswitch does not support, with the dialplans that
use them. Applications in the "not supported yet" section are skipped and
the call continues with the next action. **Send this report to us**: it is
the to-do list for full support of your data.

Warnings such as `dialplan "X" has no dialplan_xml` mean that row has never
been saved by the portal in a version that generates XML; open it and save
it.

## 4. Switch over

```bash
systemctl list-units --type=service --state=running   # the previous switch's service name
sudo systemctl disable --now <previous switch service> # keep it installed for rollback
sudo ss -lunp | grep -E ':(5060|5080|8021)\b'          # must print nothing

sudo systemctl enable --now ringqswitch
sudo journalctl -u ringqswitch -f
```

Phones re-register within their registration interval (force it by
rebooting them or toggling the account). Then:

```bash
sudo rq_cli -x "domains"
sudo rq_cli -x "show registrations"
sudo rq_cli -x "sip status"            # gateways: REGED / NOREG
```

Test in this order: extension to extension, an outbound call through each
gateway, each DID, a ring group, an IVR, a queue. Check that the calls
appear in the portal's call history and that the active calls and
registrations pages work.

**Rollback** at any time:

```bash
sudo systemctl disable --now ringqswitch
sudo systemctl enable --now <previous switch service>
```

RingQswitch writes `registrations`, `channels`, `calls` (rows of its own
host name), CDR rows and the tables listed in section 6. It changes
configuration only where section 6 says so (for example do-not-disturb set
from a phone) and when you save something in its web portal.

## 5. What is read

| Table | Used for | Notes |
|---|---|---|
| `v_domains` | tenants | `domain_name` = SIP realm = dialplan context. Disabled domains are skipped |
| `v_extensions` | users | `extension`, `password`, `number_alias`, `enabled`, `max_registrations`; every other column is a variable (`${user_data 1001@domain var forward_busy_enabled}` works). `user_context` defaults to the domain |
| `v_gateways` | trunks | `proxy`, `outbound_proxy`, `register_proxy`, `username`, `auth_username`, `password`, `realm`, `from_user`, `from_domain`, `register`, `register_transport` (udp/tcp), `expire_seconds`, `retry_seconds`, `context`, `extension`, `caller_id_in_from`, `enabled`, `hostname`, `profile` (the SIP profile whose port it sends from) |
| `v_sip_profiles`, `v_sip_profile_settings` | SIP ports | enabled profiles for this host; settings `sip-port` (`$${internal_sip_port}` = 5060, `$${external_sip_port}` = 5080), `sip-ip` (bound only when it is an address of this server), `ext-sip-ip`, `bind-params` (`transport=udp` = no TCP), `tls` (not served). Each enabled profile on another port than the main one opens that port |
| `v_dialplans` | dialplan | `dialplan_xml` of enabled rows for this host, by `dialplan_order`. Context `${domain_name}`/`global` rows apply to every domain (or to their own domain when `domain_uuid` is set); `public` is the inbound context |
| `v_vars` | globals | enabled `set` rows for this host; values in `ringqswitch.xml` win |
| `v_ring_groups`, `v_ring_group_destinations` | ring groups | strategies simultaneous, enterprise, sequence, random, rollover; per destination delay and timeout; caller ID prefixes; forward; greeting; timeout action |
| `v_ivr_menus`, `v_ivr_menu_options` | IVR | greetings (long/short), invalid and exit sounds, timeouts, max failures/timeouts, digit length, direct dial, options `menu-exec-app`, `menu-sub`, `menu-top`, `menu-back`, `menu-exit`, `menu-play-sound`, exit action |
| `v_access_controls` / `_nodes` | carriers | `allow` nodes of the list named `providers` |
| `v_call_center_queues`, `v_call_center_agents`, `v_call_center_tiers` | call centre | queue strategy, music, greeting, announcements, max wait times, tier rules, exit keys, record template; agent contact (`user/<ext>@<domain>`), call timeout, wrap-up, delays, max no answer; tier level and position. Agent status survives restarts (`rq_agent_state`, else `agents`) |
| `v_voicemails`, `v_voicemail_messages` | voicemail | enabled boxes and their new/heard counts (message lights) |
| `v_conferences` | conferences | room number, PIN, `record` flag |
| `v_destinations` | DIDs | read by the web interface; calls are routed by the DID's dialplan in `public` |
| `v_domain_settings` | time zone, language | category `domain`: `time_zone` / `name` (e.g. `Asia/Singapore`: calls of the domain are routed in its time) and `language` / `code` (e.g. `en-us`: the sound pack) |
| `q_time_schedules`, `q_holidays`, `q_holiday_exemptions` | office hours | RingQ's tables: `work_schedule` (`day-close-open`, minutes, day 1 = Sunday; a day not listed is open all day; close = open: closed all day), `work_default` (the action outside office hours, `""` = none), `break_schedule` (`day-start-end`), `break_default`, `special_filename` (the holiday sound), `special_default`, `work_extensions` / `break_extensions` / `special_extensions` (when set, only calls to these numbers); holidays `month`/`day`/`year` to `month2`/`day2`/`year2`, `recurring`, `time` (`HH:MM-HH:MM`, empty: all day), `holiday_greeting`, `exemptions`, `extensions` (when set, the holiday closes only these numbers); exemptions `extension` (a number never closed on holidays) |
| `q_security_restrictions`, `q_security_rules`, `v_pin_numbers` | calls out | `blocked_countries` (ISO codes; calling codes are read too); `pin_all_outbound`, `pin_prefix_outbound`, `pin_no_outbound`, `prefixes`, `trusted_device` (extensions, comma separated); enabled PINs and their `accountcode` |
| `rq_settings`, `sip_security_blocks` | SIP Guard | `server` / `sip_guard` (JSON: enabled and the protections), `server` / `restrict_sip`, `<domain uuid>` / `web_idle_logout`; bans with `status = 'banned'` whose `metadata->>'until'` is still ahead are restored at start |
| `rq_aux_codes`, `rq_outbound_routes`, `rq_time_conditions` | RingQswitch | AUX codes (with "block direct calls"), outbound routes and office hours made in the web interface (their dialplans are in `v_dialplans`) |

**When changes apply.** The configuration tables are fingerprinted every
`poll-seconds` (10 in the sample configuration); any insert, update or delete triggers a reload,
whether or not the application sets `update_date`. `reloadxml`,
`memcache delete ...`, `memcache flush`, `sip profile ... rescan` (also
sent as `sofia profile ... rescan`), `fsctl send_sighup` and `kill -HUP`
reload immediately. A reload never drops calls
in progress, and a configuration that fails to load is not applied (the
previous one stays active and the error is logged). Gateways are added,
removed or re-registered only when their row changed.

## 6. What is written

| Table | When | Columns |
|---|---|---|
| `registrations` | REGISTER, unregister, expiry | `reg_user`, `realm`, `token` (Call-ID), `url` (`sofia/internal/<contact>`, the format the portal reads), `expires` (epoch), `network_ip`, `network_port`, `network_proto`, `hostname`, `metadata` (User-Agent) |
| `channels` | channel create, answer, state and application changes; deleted at hangup | the table's columns (`uuid`, `direction`, `created`, `name`, `state`, `callstate`, `cid_*`, `dest`, `application`, `context`, `read_codec`, `presence_id`, `call_uuid`, `callee_*`, `initial_*` ...) |
| `calls` | bridge; deleted at unbridge/hangup | `call_uuid`, `call_created`, `caller_uuid`, `callee_uuid`, `hostname` |
| `v_xml_cdr` (+ `cdr-tables`) | hangup of every A leg (`cdr-b-leg=true` adds B legs) | `xml_cdr_uuid` (= channel uuid), `domain_uuid`, `domain_name`, `extension_uuid`, `direction` (`call_direction`), `caller_id_*`, `caller_destination`, `destination_number`, `start/answer/end_stamp` and `_epoch`, `duration`, `billsec`, `mduration`, `billmsec`, `waitsec`, `hangup_cause`, `hangup_cause_q850`, `sip_hangup_disposition`, `bridge_uuid`, codecs, `remote_media_ip`, `network_addr`, `record_path`, `record_name`, `record_length`, `leg`, `originating_leg_uuid`, `pdd_ms`, `last_app`, `last_arg`, `missed_call`, `status` (answered: someone answered; missed: nobody did, also a queue call no agent answered; voicemail: a message was left; cancelled, busy, failed), `ring_group_uuid`, `ivr_menu_uuid`, `json` (all channel variables; `variables.rq_call_flow` is the call's path shown in Call history: JSON steps `{t, k, x}`) |

| `agents`, `members` | agent status/state changes; queue callers | the call centre's runtime tables, in the format the portal reads |
| `rq_agent_state` | every agent status change | the current status, AUX code and since when (restored after a restart) |
| `rq_agent_status_log` | every agent status change | agent, extension, new status and AUX code, previous status and AUX code, how long the previous status lasted, source (`web:<user>`, `phone`, `api` (the event socket), `system`) |
| `q_auxcodes_list` | every agent status change | RingQ's AUX history: `auxcode_status` (the AUX code, or the status), `extension`, `username` |
| `rq_queue_calls`, `q_call_queues_list` | a caller leaves a queue | queue, caller, joined/answered/ended, wait and talk seconds, result (`answered`, `abandoned`, `timeout`, `no_agent_timeout`, `exit_with_key`, `break_out`), agent, agents tried; `rq_queue_calls.call_uuid` is the caller's call (its `v_xml_cdr` row and transcript) |
| `rq_sso_providers`, `rq_signin_options` | single sign-on is set up (Settings → Integrations) | per domain and provider: client ID, client secret (encrypted with the data-dir key), tenant ID, on/off; whether web passwords may sign in. `rq_web_users.sso_email` is an extension's sign-in email |
| `fax_jobs`, `fax_domain_settings` | a fax is queued, sent or received (eFax); the station id and header are saved | the RingQ fax engine's own tables (same columns): direction, numbers, pages, status, file, call, error; per-domain `fax_ident`, `fax_header`. `v_fax` holds the fax extensions |
| `rq_hotel_stays` | a guest checks in or out (Hotel front desk) | room (extension), guest first and last name, checked in at / by, checked out at / by |
| `rq_wakeup_calls` | a wake-up call is scheduled, rung or cancelled | room, stay, time, sound, `status` (`scheduled`, `calling`, `answered`, `no_answer`, `cancelled`), tries, last result |
| `rq_transcription_settings` | Settings → Call transcription is saved | per domain: `enabled`, `scope` (`external`: calls through a trunk; `all`), `language` (`auto` or a code), `model` (a speech model's name; empty: the server's default), `voicemail` (transcribe voicemail messages), `live` (live transcription during calls), `live_model` (the speech model for it; empty: the fastest installed) |
| `q_blfs`, `q_company_blfs` | phone keys are saved (an extension's page, Settings → Phone keys) | RingQ's tables (created when missing): `q_blfs` one row per extension (`user_uuid`: RingQ's user of the extension, else the extension), `types` and `"values"` as JSON lists (`["blf","park",...]`, `["1045","2",...]`); `q_company_blfs` the domain's keys with `show_dialer` |
| `v_extensions` DND and forwarding | a phone dials `*78`/`*79`/`*77`, presses its DND or forward key (as-feature-event), or the dialer changes DND | `do_not_disturb`, `forward_all_enabled`, `forward_all_destination`, `forward_busy_*`, `forward_no_answer_*` |
| `q_time_schedules`, `q_holidays`, `q_holiday_exemptions`, `v_domain_settings` | Language & office hours is changed | the columns read above (created when missing, as in the RingQ database) |
| `q_security_restrictions`, `q_security_rules`, `v_pin_numbers`, `rq_settings` | Security settings is changed | the columns read above |
| `sip_security_blocks`, `security_audit_log` | SIP Guard bans an address, a ban is lifted or ends | RingQ's tables: one row per address and protection (`ip_address`, `category` = `jail_name`, `reason`, `failure_count`, `first_seen_at`, `last_seen_at`, `blocked_at`, `unblocked_at`, `status` (`banned`, `unbanned`, `expired`), `source` (`auto`, `manual`), `metadata` `{until, since, actor}`); the history: `occurred_at`, `actor`, `action` (`ban`, `unban`, `expire`), `ip_address`, `category`, `reason` |
| `v_xml_cdr` `pin_number`, `accountcode` | a call out with a PIN | the PIN entered and its account code |
| `v_voicemail_messages` (`message_status`) | messages are heard (`saved`) or deleted by phone | the same columns as the web interface |
| `rq_ai_agents`, `rq_ai_agent_keywords` | an AI agent is saved (menu AI agents) | per agent: `agent_name`, `agent_extension` (its number, unique among the domain's numbers), `agent_enabled`, `agent_voice`, `agent_noise` (`near`, `far`), `agent_languages` (codes, comma separated), `agent_greetings` (JSON list), `agent_prompt`, `agent_context`, `knowledge_urls` (one per line), `knowledge_text`, `max_minutes`, `allow_end_call`, `fallback_action`, `dialplan_uuid`; keywords: `keyword`, `destination` (`transfer:1000 XML <domain>`), `keyword_order` |
| `rq_ai_knowledge` | an AI agent's web pages are read | `ai_agent_uuid`, `source` (the page's address), `title`, `chunk_no`, `content` (about 900 characters of the page's text), `fetched_at` |
| `rq_ai_calls` | an AI agent answers a call, and when it ends | `ai_agent_uuid`, `call_uuid` (= `v_xml_cdr.xml_cdr_uuid`), `caller_number`, `caller_name`, `destination_number` (the DID), `started_at`, `ended_at`, `duration`, `outcome` (`active`, `completed`, `transferred`, `failed`, `interrupted`), `transferred_to`, `turns` (what the caller said, counted), `transcript` (JSON: `[{role: agent \| caller \| event, text, t}]`), `error` |
| `rq_ai_settings` | Settings → AI agent service is saved | per domain: `api_key_enc` (the voice AI service's API key, encrypted with the data-dir key), `model` (empty: the service's default voice model), `base_url` (empty: the default service address) |
| `rq_transcripts` | a transcribed call ends or a voicemail message is left (queued), is transcribed, or is transcribed again | `call_uuid` (= `v_xml_cdr.xml_cdr_uuid`, or `v_voicemail_messages.voicemail_message_uuid`), `kind` (`call`, `voicemail`), `domain_uuid`, `status` (`queued`, `processing`, `done`, `empty`, `failed`), `direction`, `speaker_a`, `speaker_b`, `customer` (`A`, `B` or empty), `recording` (path), `language`, `duration` (s), `stereo`, `segments` (jsonb: `[{speaker, start, end, text, sentiment}]`), `speakers` (jsonb: per speaker `talk_seconds`, `words`, `sentiment`, `label`, `ending`, `positive`, `neutral`, `negative`), `sentiment` and `sentiment_label` (the call), `customer_sentiment` and `customer_label`, `engine`, `error` |
| `v_voicemail_messages` | a voicemail is left (and transcribed) | message uuid, box, caller, length; the audio is `voicemail-dir/<domain>/<box>/msg_<uuid>.wav`; `message_transcription`: the message's text, when voicemail transcription is on |
| `v_xml_cdr` call-centre columns | queue calls | `call_center_queue_uuid`, `cc_queue`, `cc_agent`, `cc_agent_uuid`, `cc_cause`, `cc_cancel_reason`, `cc_queue_*_epoch`; `voicemail_message`; status `voicemail` |

**Tables RingQswitch creates.** At start (`manage-schema`, default on) it
creates its own `rq_*` tables when missing. `ringqswitch -init-db` (and the
web interface's *Repair defaults*) also creates the RingQ tables listed above
**that do not exist** (all of them in a new database, none in a complete
RingQ database), the default dialplans (feature codes `*22`,
`*23`, `*97`, `*99<ext>`, `*59xx`, `**<ext>`, `*8`, `*80<ext>`, extension
calls) when no dialplan with that name or number exists, default AUX codes
per domain, and dialplans for ring groups, IVR menus, queues and conferences
that have none. Existing rows and tables are never changed.

**Who may read those tables.** The tables belong to the database user in
`dsn`. At start RingQswitch lets the database's owner and the owner of
`v_domains` read and change them too (also tables it creates later), plus
the users listed in `<param name="grant-to" value="ringq,portal"/>`. So a
database tool or the RingQ portal signed in as another user does not get
"permission denied for table rq_…". To fix it at once by hand, as the
`postgres` user:

```bash
sudo -u postgres psql DBNAME -c "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ringq" \
  -c "GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO ringq"
```

Rows written at startup by a previous run of the same host are removed
first. Only columns that exist are written, so older and newer schemas work.
An insert that conflicts with an existing row (same `xml_cdr_uuid`) is
skipped, so a second writer cannot cause duplicates with the same uuid.

**`q_ringq_cdr`.** It has the `v_xml_cdr` columns. Find out what fills it
today before adding it to `cdr-tables`: if your backend inserts it from
`CHANNEL_HANGUP_COMPLETE` events, it keeps working unchanged (RingQswitch
sends those events with all `variable_*` headers); if a script inside the
previous switch did it, add `q_ringq_cdr` to `cdr-tables`.

## 7. Phones and domains

* The digest realm is the domain name. Phones configured with the domain
  (`1001@pbx.example.com`) work in multi-tenant setups.
* Phones configured with the server's IP address register to the
  *default domain*: the only domain when there is one, otherwise the one
  named by `default-domain`.
* The same extension number can exist in several domains.
* `max_registrations` limits contacts per extension (oldest replaced),
  disabled extensions cannot register or be called.
* Do-not-disturb (`do_not_disturb=true`) answers callers with busy;
  `forward_all_*` forwards before ringing; `forward_busy_*`,
  `forward_no_answer_*` and `forward_user_not_registered_*` apply after the
  bridge through `failure_handler`.

## 8. Carriers

* **Registered gateways**: RingQswitch registers with Contact
  `sip:gw+<gateway_uuid>@<server>`, so calls the carrier sends to that
  contact are recognised. Calls from the gateway's proxy address are
  recognised too. Both go to the gateway's `context` (default `public`).
* **IP-authenticated carriers** (no registration): add their addresses to
  the portal's `providers` access control list (Advanced > Access Controls),
  in Security → Trusted carriers, or as `<acl>` nodes.
* **Ports**: every enabled SIP profile opens its own port. The standard
  `external` profile listens on **5080**, so carriers that send calls to
  5080 keep working; Settings → Global → SIP Profile → *Add standard
  profiles* creates it when the database has none. A gateway sends from its
  `profile`'s port.
* Dial strings `ringq/gateway/<uuid or name>/<number>` and
  `gateway/<name>/<number>` work, and so does the older
  `sofia/gateway/<uuid or name>/<number>` that existing dialplans contain.

## 9. Dialplan support

Supported in stored dialplans:

* Conditions: `field` (variables, `${...}` expressions, caller profile
  fields such as `ani`, `chan_name`), `expression`, `break`, anti-actions,
  `regex="all|any|xor"` with `<regex>` children, nested conditions, time
  rules (`wday`, `mday`, `mon`, `year`, `yday`, `week`, `mweek`, `hour`,
  `minute`, `minute-of-day`, `time-of-day`, `date-time`) in the domain's
  time zone, `inline="true"`.
* Expansion: `${var}` (falling back to globals), `$${global}`,
  `${var:offset:length}`, and the functions `user_exists`, `user_data`,
  `domain_exists`, `cond`, `strftime`, `strepoch`, `create_uuid`, `regex`,
  `expr`, `sofia_contact`, `sofia_count_reg`, `hash`/`db` select,
  `uuid_getvar`, `global_getvar`, `url_encode`, `url_decode`, `md5`.
* Applications: `answer`, `pre_answer`, `ring_ready`, `hangup`, `respond`,
  `redirect`, `set`, `unset`, `export` (incl. `nolocal:`), `multiset`,
  `set_global`, `push`, `unshift`, `set_user`, `set_profile_var`, `log`,
  `info`, `event`, `sleep`, `playback`, `endless_playback`, `gentones`,
  `echo`, `park`, `valet_park`, `bridge` (`user/`, `ringq/gateway/`,
  `gateway/`, `ringq/<profile>/`, the older `sofia/...` spellings, `sip:`,
  `loopback/`, `error/`; `,` `|` `{}` `[]`, `leg_timeout`,
  `leg_delay_start`, `originate_timeout`, `ignore_early_media`,
  `continue_on_fail`, `hangup_after_bridge`, `sip_h_*`), `transfer`,
  `play_and_get_digits`, `read`, `record`, `record_session`,
  `stop_record_session`, `voicemail`, `callcenter`, `conference`,
  `intercept`, `rq_dnd`, `rq_call_return`, `rq_redial`, `rq_pickup`,
  `rq_agent_status` (desk phone codes), `lua do_not_disturb.lua`,
  `intercept.lua`, `intercept_group.lua`, `ai_agent` (a RingQswitch AI
  agent answers: see WEB-UI.md 12E), `rxfax`, `txfax` (fax on G.711, or
  T.38 when the far end re-INVITEs to it unless `fax_enable_t38=false`:
  `fax_ident`, `fax_header`; results in `fax_success`, `fax_result_code`,
  `fax_result_text`, `fax_document_transferred_pages`... and the
  `spandsp::rxfaxresult` / `txfaxresult` events the portal listens for),
  `send_dtmf`, `hash`, `db`, `limit`, `sched_hangup`, `sched_transfer`,
  `ivr`, `ring_group`, and `lua` for `app.lua ring_groups` and
  `app.lua failure_handler`. Applications that only tune switch internals
  (`bind_meta_app`, `presence`, `privacy`, `jitterbuffer`...) are accepted
  and ignored.
* `api_on_answer` and `execute_on_answer` (for `uuid_record`,
  `record_session`, `sched_hangup`...).

Not yet supported (logged, then the call continues): `fifo`, `eavesdrop`
(use the portal's Listen / Whisper / Barge), `att_xfer` (attended transfer
from the phone works), `say`/`phrase`/`speak` (text-to-speech), `system`,
other Lua scripts. `-compat` tells you which of them your data uses.

Recordings are 8 kHz WAV (stereo with `record_stereo=true`), written where
the dialplan says (`record_path`/`record_name` end up in the CDR). G.711
calls are recorded; calls bridged with other codecs are recorded silent,
so prefer PCMU/PCMA when recording matters.

## 10. Troubleshooting

| Symptom | Check |
|---|---|
| `database: ... password authentication failed` | DSN user/password; `pg_hba.conf` allows `127.0.0.1` with md5/scram |
| `-check` shows 0 extensions | wrong database name, or the domain is disabled in `v_domains` |
| Phone gets 403 | `rq_cli -x "domains"`: is the phone's domain listed? With several domains and an IP-configured phone set `default-domain`. Wrong password shows `authentication failed` in the journal; 10 failures block the IP (`rq_cli -x blocked`, `-x "unblock IP"`, or Security → SIP Guard) |
| Calls to an extension fail with 404 / no route | `rq_cli -x "user_exists id 1001 domain"`; the dialplan context is the domain name: check `-compat` and the journal for `no route` |
| Outbound call fails with `INVALID_GATEWAY` | the dialplan references a gateway uuid that is disabled, missing, or for another `hostname` |
| Portal change not applied | wait `poll-seconds`, or `rq_cli -x reloadxml`; the journal shows `database configuration changed; reloading` |
| Backend cannot connect to 8021 | `event-socket-listen` address and `event-socket-password`; the previous switch must be stopped |
| No CDR rows | journal `database write failed`; table names in `cdr-tables`; the database user needs INSERT on them and on `registrations`, `channels`, `calls` |
