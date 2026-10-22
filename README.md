# RingQswitch

**RingQswitch** is the telephony switch of the RingQ platform: a complete
software PBX and contact-centre engine in a single program. It handles SIP
phones and trunks, routes and records calls, runs queues, IVR menus,
voicemail, conferences and fax, and ships with its own web portal: call flow
designer, browser dialer, chat, video meetings, live wallboard, reports,
call transcription, AI voice agents and security centre.

It runs on one Linux server, keeps its configuration in the RingQ database,
and is managed entirely from the browser.

> **Version 0.10.1.** Calls, queues, voicemail, conferences, the web portal,
> the browser dialer, chat, meetings, fax, call transcription, AI agents,
> desk phone keys, office hours and the security centre work end to end and
> are covered by automated tests. Field-test with your own phones and
> carriers before production.

---

## Contents

1. [Highlights](#1-highlights)
2. [Architecture](#2-architecture)
3. [Features and modules](#3-features-and-modules)
4. [Requirements](#4-requirements)
5. [Building from source](#5-building-from-source)
6. [Release packages](#6-release-packages)
7. [Installing and upgrading](#7-installing-and-upgrading)
8. [Configuration](#8-configuration)
9. [Running and operating](#9-running-and-operating)
10. [Testing](#10-testing)
11. [Repository layout](#11-repository-layout)
12. [Documentation](#12-documentation)
13. [License](#13-license)

---

## 1. Highlights

- **One self-contained program.** `ringqswitch` is a single static binary
  (no runtime libraries, no external services needed to make calls). The
  web portal, its fonts, icons and voice prompts are built in.
- **Multi-tenant.** Every company is a domain with its own extensions,
  numbering, dialplan, time zone, office hours and security settings, on one
  server.
- **RingQ database native.** Domains, extensions, trunks, numbers, dialplans,
  queues, agents, voicemail boxes, conferences, phone keys, office hours and
  security rules are read from the RingQ database and reloaded automatically
  when they change; registrations, live calls, agent states and call records
  are written back.
- **Browser first.** The portal is a fast single-page application over a
  JSON API with live updates over WebSocket. Extensions sign in and get a
  softphone, chat and meetings in the browser.
- **Secure by default.** Digest authentication, signed nonces, SIP Guard
  protections that ban attackers automatically, country restrictions and PIN
  dialing against toll fraud, address rules for SIP and the web portal,
  HTTPS everywhere.
- **Open control.** An event socket and console (`rq_cli`) for live control,
  monitoring and integration with other RingQ services.

---

## 2. Architecture

### 2.1 System overview

```
                        ┌──────────────────────────────── RingQswitch (one process) ─────────────────────────────────┐
 SIP phones    ◄─SIP─►  │ SIP stack (UDP/TCP, SIP profiles) ─► transactions ─► CORE ─► dialplan engine               │
 SIP trunks    ◄─SIP─►  │ gateways (registration, authentication)                      └─► applications              │
 RTP audio     ◄─RTP─►  │ media engine (relay, play, record, mix, DTMF, fax)               (bridge, IVR, queue,      │
                        │ registrar · call centre · presence/BLF · voicemail               voicemail, conference,    │
                        │ conferences · SIP Guard · office hours · rules                   fax, record, AI ...)      │
                        │                                                                                            │
                        │ event bus ─┬─► event socket (port 8021) ─► rq_cli, integrations                            │
                        │            ├─► database writer                                                             │
                        │            └─► live feeds for the portal                                                   │
                        │                                                                                            │
 Browsers      ◄HTTPS►  │ web server: portal · JSON API · WebSocket · WebRTC gateway                                 │
 (portal,     ◄WebRTC►  │             meetings (SFU) · transcription jobs · AI agent sessions                        │
  dialer)               │                                                                                            │
                        └──────────────────────────────┬───────────────────────────────▲─────────────────────────────┘
                                                       │                               │
                                                       ▼ configuration                 │ registrations, calls,
                                                         (read; reloaded on change)    │ agent states, call records
                                               ┌────────────────────────────────────────────────────────────────┐
                                               │                         RingQ database                         │
                                               │ domains · extensions · gateways · numbers · dialplans · queues │
                                               │  agents · voicemail · office hours · security · call records   │
                                               └────────────────────────────────────────────────────────────────┘

 Optional on the same server: the speech recogniser for call transcription (its own program).
 Optional in the cloud: a voice AI service for AI agents (your own API key).
```

### 2.2 Packages

The source is Go, organised so that dependencies point one way: protocol
layers know nothing about the switch, the switch knows nothing about the
database or the web portal.

| Package | Responsibility |
|---|---|
| `internal/sip` | SIP messages, URIs and headers; digest authentication; UDP and TCP transports; the RFC 3261 transaction layer (retransmissions, timers A–K); DNS SRV/A resolution |
| `internal/sdp` | Session descriptions: parsing, offer/answer, codec selection, payload-type mapping, codec passthrough |
| `internal/media` | RTP/RTCP endpoints and port allocator; relay, local (play/record) and echo modes; G.711 μ-law/A-law, WAV and MP3 decoding, tone generation, RFC 4733 DTMF, symmetric RTP latching |
| `internal/dialplan` | XML dialplan loader and hunter: contexts, extensions, regex and time conditions, actions and anti-actions |
| `internal/config` | Configuration model: settings, multi-domain directory, gateways, SIP profiles, ACLs, features (ring groups, IVR, queues, agents, voicemail, conferences), office hours, outbound rules, country table, SIP Guard settings |
| `internal/core` | The switch: registrar, channels and B2BUA call control, dialplan executor, variable expansion, applications, gateways, call centre, voicemail, conferences, park/pickup/intercom, presence and BLF, SIP Guard, office hours, outbound rules, events, API commands, call records |
| `internal/fax` | Fax engine: T.30 protocol, V.21 and V.29 modems, T.4 image coding, T.38 over UDPTL |
| `internal/db` | RingQ database integration: loads configuration into the core (`Overlay`), watches for changes (`Watch`), writes events back (`Writer`), creates missing tables (`EnsureSchema`), grants |
| `internal/esl` | Event socket server and client (commands, events, filters) |
| `internal/web` | Web server: portal, JSON API, sessions and roles, WebSocket hub, browser dialer gateway, meetings (SFU), chat, reports, eFax, transcription jobs, AI agents, office hours, security centre |
| `internal/web/static` | The portal (single-page application): one module per page, embedded in the binary |
| `internal/webphone` | SIP client used inside the switch for browser dialers |
| `internal/transcribe` | Call transcription: audio preparation, voice activity detection, speaker turns, speech recogniser driver, live transcription, sentiment analysis |
| `internal/logbus` | Live log stream for the console and the portal |
| `internal/testua` | Scriptable SIP phone used by the automated tests |
| `cmd/ringqswitch` | The daemon: flags, signals, reload, logging, database and web start-up |
| `cmd/rq_cli` | The console |

### 2.3 Configuration sources and reload

- **XML mode** (no database): `conf/ringqswitch.xml` plus
  `conf/directory/*.xml` (users) and `conf/dialplan/*.xml` (contexts).
- **Database mode** (`<database>` with a `dsn`): the XML settings stay (ports,
  paths, security defaults), and domains, users, gateways, SIP profiles,
  dialplans and features come from the RingQ database (`Overlay`).
- **Reload** happens automatically when database tables change (fingerprinted
  every few seconds), on `reloadxml` from the console, on `SIGHUP`, and after
  every change made in the portal. A reload never drops calls; a
  configuration that fails to load is not applied.

### 2.4 Call model

Every call leg is a **channel**. A basic call has two, bridged by the switch
(back-to-back user agent):

```
 phone A ──SIP dialog 1──► [channel A: inbound]  ══ bridge ══  [channel B: outbound] ──SIP dialog 2──► phone B
 phone A ◄──RTP──► endpoint A ◄═══════════ relay ═══════════► endpoint B ◄──RTP──► phone B
```

- Each channel owns one SIP dialog and one RTP endpoint. Media is always
  anchored on the switch, which makes recording, monitoring (listen, whisper,
  barge), transcription and NAT traversal work without anything on the phone.
- Codecs are passed through when both sides share one (no quality loss);
  G.711 μ-law/A-law is transcoded when needed.
- In-dialog requests go to the address the dialog came from, so phones
  behind NAT work without extra configuration.

**An incoming call**:

1. The SIP stack accepts the INVITE (after the SIP Guard and address rules
   let the source through) and answers `100 Trying`.
2. The source is classified: a gateway (trunk) → its context; a trusted
   carrier address → its context; otherwise digest authentication → the
   extension's context.
3. A channel is created with its variables and an RTP endpoint.
4. A call from outside reaching its company is checked against **office
   hours**: holidays, breaks and closed hours get their configured action.
5. The channel's **executor** hunts the dialplan and runs each action's
   application (bridge, IVR, queue, voicemail...). Transfers re-hunt with the
   new destination.

**An outgoing call** through a trunk: the bridge application checks the
company's **outbound rules** (allowed countries, PIN dialing), resolves the
gateway, and places the outbound leg; the first leg to answer wins.

### 2.5 Concurrency

- One reader per UDP socket and TCP connection feeds the transaction layer;
  new requests are handled on their own goroutines.
- Each channel with an executor has one goroutine running its applications;
  every blocking operation is cancelled on hangup or transfer.
- Each RTP endpoint has its own readers; a 20 ms media clock paces playback.
- The event bus never blocks the call path: slow subscribers drop events and
  the drops are counted.

### 2.6 Security layers

1. **Address rules**: Deny lists for SIP and the web portal; Allow lists that
   are never banned; an optional global IP restriction (only allowed
   addresses, trunks and carriers may send SIP).
2. **SIP Guard**: protections that count failures per address within a
   window and ban the address (SIP or web) for a set time. Bans survive
   restarts.
3. **Authentication**: digest with HMAC-signed nonces and replay
   protection for phones; separate hashed web passwords, roles and optional
   single sign-on for the portal; a password-protected event socket bound to
   the local machine.
4. **Toll fraud controls**: allowed countries and PIN-based dialing per
   company.
5. **Transport**: HTTPS for the portal (self-signed or your own certificate,
   reloaded on renewal); encrypted secrets in the database (API keys, single
   sign-on secrets).

---

## 3. Features and modules

### 3.1 Telephony core

| Area | Details |
|---|---|
| SIP stack | RFC 3261 parser and transaction layer over UDP and TCP, RFC 3581 `rport`, RFC 5626 keep-alives, DNS SRV/A, several SIP profiles (ports) at once |
| Registrar | Digest authentication (MD5, `qop=auth`), signed stateless nonces, nonce-count replay protection, several contacts per user, NAT-aware routing, expiry handling |
| Call control | B2BUA with separate dialogs per leg: ringing and early media, CANCEL, BYE, re-INVITE/UPDATE (hold), SIP INFO and RFC 4733 DTMF, blind transfer (REFER) and attended transfer (REFER with Replaces), session limits, RTP timeout, one-way audio detection |
| Media | RTP/RTCP relay with symmetric latching, codec passthrough (G.722, Opus and others relayed untouched), payload-type remapping, G.711 transcoding, playback of WAV and tones, recording (stereo), conference mixing |
| Dialplan | XML contexts and extensions, regex and nested conditions, captures, variable and function expansion, `continue`/`break`, anti-actions, inline actions, time-of-day, weekday and date rules evaluated in the company's time zone |
| Bridge targets | `user/1000[@domain]`, `ringq/gateway/<name or id>/<number>`, `gateway/<name>/<number>`, `sip:` URIs, `loopback/<number>/<context>`, `error/<cause>`; `,` rings in parallel, `\|` fails over; per-leg variables, call and leg timeouts, delays, early media control |
| Applications | `answer` `pre_answer` `ring_ready` `hangup` `respond` `redirect` `bridge` `transfer` `playback` `echo` `park` `record` `record_session` `rxfax` `txfax` `ai_agent` `play_and_get_digits` `read` `send_dtmf` `sleep` `set` `unset` `export` `multiset` `hash` `db` `limit` `sched_hangup` `log` `info`, `ivr`, `ring_group`, `failure_handler` (forward on busy / no answer) |
| Trunks | Gateways with or without registration, digest authentication, caller ID in From or PAI/RPID, per-gateway SIP profile; inbound calls routed by source address |
| Multi-tenant | Domains with their own realm, users, dialplan context, time zone, language and settings; the same extension number in several domains |

### 3.2 PBX features

| Feature | Details |
|---|---|
| Extensions | SIP accounts with caller ID, call forwarding (all, busy, no answer, not registered), do not disturb, call recording, voicemail, web access and role |
| Ring groups | Simultaneous, enterprise, sequence, random, rollover; per-destination delay and timeout; greeting; timeout action |
| IVR menus | Long and short greetings, invalid and exit sounds, timeouts, direct dial, sub-menus, actions per key |
| Time conditions | A number with its own open days and times and holidays, routing open and closed calls differently |
| Voicemail | Greetings, message lights (MWI), voice menu by phone (listen, save, delete), playback and download in the portal, transcription |
| Conferences | Rooms with PIN, mute, recording |
| Call handling | Music on hold, call park and parking slots, call pickup (group and directed), intercom, call return, redial |
| Recording | Per extension, number or queue; stereo recordings for transcription; playback and download in Call history |

**Feature codes** (dial from any phone or the browser dialer):

| Code | Action |
|---|---|
| `*22` / `*23` | Agent log in / log out; break on / off |
| `*26<n>` | Agent status number *n* (1 is Available, then the AUX codes) |
| `*591`–`*595` | Parking slots 1–5 (park during a call, pick up when idle) |
| `*5901`–`*5999` | Park or retrieve a call |
| `*8` / `*8<ext>` / `**<ext>` | Pick up a call ringing in your group / on an extension |
| `*80<ext>` | Intercom |
| `*78` / `*79` / `*77` | Do not disturb on / off / toggle |
| `*97`, your own extension | Your voicemail |
| `*99<ext>` | Leave a voicemail for an extension |
| `*88<team>` | Leave a message in a team voicemail box |
| `*69` / `*870` | Call back the last caller / redial |

### 3.3 Contact centre

| Feature | Details |
|---|---|
| Queues | Strategies ring-all, longest-idle-agent, round-robin, top-down, agent-with-least-talk-time, agent-with-fewest-calls, sequentially-by-agent-order, random, ring-progressively; tiers and tier rules; wrap-up time; no-answer, busy and reject delays; max-no-answer auto break; music and announcements; exit keys; maximum wait times; no-agent announcement and timeout action |
| Agents | Available, On Break with **AUX codes** (optionally blocking direct calls), Logged Out; status from the phone (`*22`, `*23`, `*26n`, phone keys), the dialer, the portal or the event socket; every change logged |
| Wallboard | Live TV wallboard: KPI tiles (waiting, active, total, abandoned, busy agents, answered %, missed, talk time, average wait and handling, logged in) with thresholds and service level, queue calls with monitoring, agent details with log-in switches, calls per hour |
| Monitoring | Listen, whisper, barge in, divert and hang up live calls from the dashboard |
| Call status | Live board of calls by state |
| Reports | Call history (with each call's path, recordings, transcripts, sentiment), agent status times, agent status log, queue report, agent performance, queue calls; any period; 50 rows per page; CSV export |

### 3.4 Web portal modules

The portal is served over HTTPS by RingQswitch itself (default port 8443).
Sign in as the administrator or as an extension with a web password; roles
**Owner**, **Manager** and **User** are set on the User roles board.

| Menu | Module |
|---|---|
| Dashboard | Active calls (inbound/outbound) with monitoring, system health, extensions, trunks, recent calls |
| Team directory | People cards with presence, call, chat and favourites |
| Accounts | Extensions, User roles, Ring groups, IVR menus, Time conditions, Conference rooms |
| Contact center | Contacts (shared address book, CSV import/export), Call status, Call centre (live), Queues, AUX / break codes |
| Wallboard | Live TV wallboard |
| Communicate | Chat, Meetings, Voicemail |
| Call Flow | Trunks, gateways and DIDs on one page: drag a number onto an IVR, queue, extension, AI agent or other destination |
| AI agents | Voice AI agents and their call logs |
| Hotel front desk | Rooms (extensions), check-in / check-out, guest details, wake-up calls |
| eFax | Send faxes (PDF, TIFF, images) from a number; inbox, status log, resend, station id and header |
| Reports | Call history and contact-centre reports |
| Settings | Global (tile page), **Language & office hours**, Outbound rules, Integrations (single sign-on), Sounds & music, **Security**, Domains, Call transcription, AI agent service, Phone keys, Dialplans, System, Event log, My account |

Portal features: grouped menu with search (Ctrl K) that folds to icons, light
and dark mode, live updates over WebSocket, notifications for chat, calls and
meetings, language selector, lists with search and paging, top bar with
calendar, cache reload and call status.

### 3.5 Browser dialer

A softphone in the portal, registered as the signed-in extension: calls,
hold, mute, DTMF (single digits and sequences), blind and attended transfer,
several calls, contacts with presence, recent calls, voicemail, queue status
with AUX codes, do not disturb. **BLF and Parked** tabs slide out beside the
dialer (extension lamps, parking slots). Audio over one UDP port, or over TCP
on the web port where UDP is blocked; device choice, microphone boost,
speaker volume, level meter, an audio check that shows which direction loses
sound, and live transcription of the call in a panel beside the dialer.

### 3.6 Chat and meetings

- **Chat**: one-to-one and group chat with files, typing indicators and
  unread counts.
- **Meetings**: video meetings (SFU) with a Meet window (link or code,
  preview, devices, effects), guest page with waiting room (admit / decline),
  passcodes, screen sharing, presenting a PDF or picture, shared whiteboard,
  live captions, host controls (lock, mute all, remove, end), chat,
  reactions, raised hands, recording, and meeting history (who came, chat,
  transcript).

### 3.7 Language & office hours

Per company: language (voice prompts) and time zone, with the current time
there; **office hours** and **break hours** as day cards with an action for
calls outside them (hang up, busy, or any destination); **holidays** (one day
or several, every year or once, all day or some hours) with greetings and a
default holiday sound (MP3 or WAV upload); IVR menus, queues and agents
**exempt** from holidays. Applied to calls coming in on the company's numbers.

### 3.8 Security centre

| Tab | What it does |
|---|---|
| Allowed countries | Country cards by continent (with flags and calling codes, plus satellite and premium networks); calls out to a country that is not allowed are refused with a spoken message |
| PIN-based dialing | No PIN, numbers starting with given prefixes, or every call out; trusted extensions; PINs with account codes recorded on the call |
| Webapp & mobile restriction | Allow / Deny addresses for the portal, the dialer and the API; auto logout after a set time without use (with a warning, never during a call) |
| Anti-hacking | Global IP restriction for SIP; Allow / Deny address lists; trusted carriers; SIP scanner User-Agents |
| SIP Guard | Protections with failures, window and ban time: SIP request (numbers that do not exist), SIP registration (unknown users), SIP password (wrong passwords), SIP invite (flooding), web request (pages that do not exist), web traffic (floods), admin login (failed portal sign-ins); live bans with ban by hand and unban; history and statistics |

### 3.9 Desk phone keys

BLF lamps for extensions (idle, ringing with the caller, on a call) and
parking slots, message lamp, do-not-disturb and forwarding keys kept in step
with the portal, pickup from a ringing key, phone keys per extension and per
company (Extension, Speed Dial, Shared Parking, Agent Status, Change Status,
Blank) and a remote **Reboot phone**.

### 3.10 Fax

Built-in fax engine: T.30 over G.711 audio (V.21 control, V.29 pages) and
T.38 when the carrier switches to it. eFax sends documents from a number and
receives faxes on fax extensions into the portal inbox.

### 3.11 Call transcription and sentiment

After each call (calls through a trunk, or all calls), who said what:
**Speaker A** (the caller) and **Speaker B** (who answered), separated by
stereo recording, with sentiment per line, per speaker and per call, and
Customer / Agent tags. Voicemail messages are transcribed too. **Live
transcription** shows both parties' words during the call. Everything runs on
the server (the speech recogniser is installed with
`packaging/install-transcription.sh`); speech models are chosen per company.

### 3.12 AI agents

A number answered by a voice AI: greeting, natural voices, the caller's
language, barge-in, answers from a prompt, context, FAQ and the company's web
pages (read and searched on the server), connect keywords and `0` to reach
people, a fallback when the service is unreachable, and a log of every call
with what was said. Needs an API key for the voice AI service (Settings → AI
agent service).

### 3.13 Hotel front desk

Every extension is a room: check guests in and out, edit guest details,
schedule wake-up calls (three tries, five minutes apart).

### 3.14 Single sign-on

Sign in with your company's identity provider (OpenID Connect), set up per
company under Settings → Integrations; the account's email selects the
extension; web passwords can be turned off.

### 3.15 Operations

Call records to the database and/or CSV, recordings, automatic reload,
structured logging, SIP message tracing, configuration check (`-check`) and
dialplan compatibility report (`-compat`), console with live log, event
socket, systemd service, container image.

---

## 4. Requirements

**To run**

| Item | Requirement |
|---|---|
| Server | 64-bit Linux, x86-64 or ARM64; at least 1 CPU and 512 MB of memory, 2 CPUs and 2 GB for 100+ concurrent calls, 4 CPUs and 4 GB with call transcription |
| Database | PostgreSQL with the RingQ database (needed for the portal's configuration pages; XML mode runs without it) |
| Network | A public or routable address for phones and trunks; the ports below open in the firewall |

| Port | Protocol | Used for |
|---|---|---|
| 5060 (and any extra SIP profile, e.g. 5080) | UDP and TCP | SIP signalling |
| 16384–32768 | UDP | RTP audio (configurable range) |
| 8443 | TCP | Web portal (HTTPS) and browser audio over TCP |
| 8443 | UDP | Browser dialer and meetings audio (`webrtc-port`) |
| 8021 | TCP, local only | Event socket (console and integrations) |
| Free port | TCP, local only | Live speech recogniser, started on demand while live transcription is on |

Every listening socket and every outbound connection is listed in
[docs/API.md](docs/API.md), section 11.

**To build**

| Item | Requirement |
|---|---|
| Go toolchain | 1.24 or newer (official Go downloads) |
| Make | GNU make (optional; the commands below work without it) |
| Network | Not needed: every dependency is vendored in `vendor/` |
| C compiler | Not needed: the build is pure Go (`CGO_ENABLED=0` works) |

---

## 5. Building from source

### 5.1 Quick build

```bash
tar xzf ringqswitch-0.10.1-src.tar.gz
cd ringqswitch-0.10.1
make build
```

This creates:

| File | What it is |
|---|---|
| `bin/ringqswitch` | The switch (daemon) with the portal built in |
| `bin/rq_cli` | The console |

Check the result:

```bash
bin/ringqswitch -version          # RingQswitch 0.10.1
bin/ringqswitch -conf conf -check # validate the configuration
```

### 5.2 What `make build` does

```bash
go build -ldflags "-s -w -X github.com/Cal4Care-Developers/RingQswitch/internal/core.Version=0.10.1" \
  -o bin/ringqswitch ./cmd/ringqswitch
go build -ldflags "-s -w -X github.com/Cal4Care-Developers/RingQswitch/internal/core.Version=0.10.1" \
  -o bin/rq_cli ./cmd/rq_cli
```

- `-s -w` strips debug symbols (smaller binaries).
- `-X ...Version=` stamps the version shown by `-version`, the portal and
  the console. Override it with `make build VERSION=0.10.1`.
- The Makefile puts `/usr/local/go/bin` first in `PATH`, so a Go installed
  there wins over an older system package (also under `sudo`).

### 5.3 Offline and reproducible builds

All dependencies are in `vendor/`. To make sure nothing is downloaded:

```bash
GOFLAGS=-mod=vendor GOPROXY=off make build
```

### 5.4 Static binaries and other architectures

The build is pure Go, so cross-compiling needs no extra tools:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o bin/linux-amd64/ringqswitch ./cmd/ringqswitch
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o bin/linux-amd64/rq_cli ./cmd/rq_cli
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o bin/linux-arm64/ringqswitch ./cmd/ringqswitch
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o bin/linux-arm64/rq_cli ./cmd/rq_cli
```

Add `-X github.com/Cal4Care-Developers/RingQswitch/internal/core.Version=<version>`
to `-ldflags` to stamp the version.

### 5.5 Make targets

| Target | What it does |
|---|---|
| `make build` | Build `bin/ringqswitch` and `bin/rq_cli` |
| `make run` | Build and run with `conf/` |
| `make install` | Build and install on this server (`packaging/install.sh`, needs root) |
| `make test` | Unit and end-to-end tests |
| `make race` | The tests with the race detector |
| `make test-db RINGQ_TEST_DSN=...` | Database integration tests in a throw-away schema |
| `make vet` | Formatting check and static analysis |
| `make interop` | Interoperability tests with an independent SIP traffic generator (`test/interop/`) |
| `make container` | Container image `ringqswitch:<version>` (from `Containerfile`) |
| `make clean` | Remove `bin/` |

### 5.6 Changing the portal

The portal's files are in `internal/web/static/` (one JavaScript module per
page, `app.css`, `index.html`, fonts and icons). They are embedded into the
binary at build time, so after a change run `make build` again; no separate
front-end build step is needed.

---

## 6. Release packages

Each release ships two archives:

| Archive | Contents |
|---|---|
| `ringqswitch-<version>-src.tar.gz` | Full source, vendored dependencies, sample configuration, tests, documentation |
| `ringqswitch-<version>-linux-bin.tar.gz` | Static binaries for `amd64` and `arm64`, configuration, documentation, installer, third-party notices and licenses (no Go needed) |

How they are made (from a clean checkout of the release commit):

```bash
V=0.10.1

# Source package: exactly the committed tree.
git archive --format=tar.gz --prefix=ringqswitch-$V/ -o ringqswitch-$V-src.tar.gz HEAD

# Check that it builds offline.
tar xzf ringqswitch-$V-src.tar.gz -C /tmp
(cd /tmp/ringqswitch-$V && GOFLAGS=-mod=vendor GOPROXY=off make build && bin/ringqswitch -version)

# Binary package.
B=pkg/ringqswitch-$V-linux
mkdir -p $B/bin/linux-amd64 $B/bin/linux-arm64 $B/sounds $B/licenses
for arch in amd64 arm64; do
  for p in ringqswitch rq_cli; do
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
      -ldflags "-s -w -X github.com/Cal4Care-Developers/RingQswitch/internal/core.Version=$V" \
      -o $B/bin/linux-$arch/$p ./cmd/$p
  done
done
cp -r conf docs packaging INSTALL.md README.md THIRD-PARTY-NOTICES.md $B/
cp sounds/README.md $B/sounds/
(cd vendor && find . -name 'LICENSE*' -exec cp --parents {} ../$B/licenses/ \;)
tar czf ringqswitch-$V-linux-bin.tar.gz -C pkg ringqswitch-$V-linux
```

The installer (`packaging/install.sh`) picks `bin/` or `bin/linux-<arch>/`
automatically.

---

## 7. Installing and upgrading

### 7.1 Install from source

```bash
cd /opt
tar xzf /root/ringqswitch-0.10.1-src.tar.gz
cd /opt/ringqswitch-0.10.1
make build
sudo make install
sudo systemctl enable --now ringqswitch
```

### 7.2 Install from the binary package

```bash
tar xzf ringqswitch-0.10.1-linux-bin.tar.gz
cd ringqswitch-0.10.1-linux
sudo bash packaging/install.sh
sudo systemctl enable --now ringqswitch
```

### 7.3 What the installer does

| Step | Result |
|---|---|
| Service user | Creates the system user `ringq` (no login shell) |
| Programs | `/usr/local/bin/ringqswitch`, `/usr/local/bin/rq_cli`, helper scripts in `/usr/local/share/ringqswitch/` |
| Configuration | `/etc/ringqswitch/` (copied from `conf/` on the first install; **kept** on upgrades) |
| Data | `/var/lib/ringqswitch/` (logs, recordings, voicemail, sounds, fax, certificates, speech models) |
| Service | `/etc/systemd/system/ringqswitch.service`, running as `ringq` with a hardened sandbox (only `/var/lib/ringqswitch` writable) |
| Check | Validates the configuration before finishing |

### 7.4 Upgrading

Build or unpack the new version and run the installer again; the
configuration in `/etc/ringqswitch` is kept and missing database tables are
added at start:

```bash
cd /opt
tar xzf /root/ringqswitch-<version>-src.tar.gz
cd /opt/ringqswitch-<version>
make build
sudo make install
sudo systemctl restart ringqswitch
```

Then reload the portal in the browser once (Ctrl+F5).

### 7.5 First-time setup

| Task | Command |
|---|---|
| A test database with sample data (extensions 1000–1004, a ring group, an IVR, feature codes, AUX codes), the portal switched on | `sudo bash packaging/setup-test-db.sh` |
| Add RingQswitch's tables, default dialplans and AUX codes to an existing RingQ database | `sudo -u ringq ringqswitch -conf /etc/ringqswitch -init-db` |
| Give an extension portal access | `sudo -u ringq RQ_WEB_PASSWORD='...' ringqswitch -conf /etc/ringqswitch -web-access 1001@example.com -role owner` |
| Use your own HTTPS certificate (ACME certificate, renewed automatically) | the certificate helper in `packaging/` (see `INSTALL.md`) |
| Install the speech recogniser for call transcription (builds it from source and installs the build tools it needs; about 5 minutes) | `sudo bash packaging/install-transcription.sh` |

---

## 8. Configuration

### 8.1 Files

```
/etc/ringqswitch/ringqswitch.xml        core settings, global variables, gateways, ACLs, database, web
/etc/ringqswitch/directory/*.xml        users (XML mode)
/etc/ringqswitch/dialplan/*.xml         dialplan contexts (XML mode): "default" for extensions, "public" for trunks
/etc/ringqswitch/examples/ringq-postgres.xml   a complete configuration for a RingQ database
```

In the source tree these are under `conf/`. `$${name}` refers to a global
variable defined in `<vars>`.

### 8.2 Main settings (`<settings>`)

| Parameter | Default | Meaning |
|---|---|---|
| `sip-ip`, `sip-port`, `enable-tcp` | auto, 5060, true | SIP address and port |
| `ext-sip-ip`, `ext-rtp-ip` | empty | Public address when the server is behind NAT |
| `rtp-ip`, `rtp-start-port`, `rtp-end-port` | auto, 16384, 32768 | RTP address and port range |
| `rtp-timeout` | 120 | Hang up calls with no audio for this many seconds (0 = off) |
| `codec-prefs` | PCMU,PCMA | Codec preference |
| `auth-calls` | true | Challenge calls from unknown sources |
| `auth-fail-threshold`, `auth-fail-window`, `auth-block-seconds` | 10, 60, 600 | Defaults of the SIP password protection |
| `max-sessions`, `call-timeout` | 1000, 60 | Session limit, default ring time |
| `event-socket-listen`, `event-socket-password` | 127.0.0.1:8021 | Event socket (change the password) |
| `cdr-csv`, `sounds-dir`, `recordings-dir`, `voicemail-dir`, `fax-dir` | | Paths |
| `log-level`, `sip-trace` | info, false | Logging |
| `transcribe-*` | | Call transcription (program, model, threads, workers, minimum length, live server) |

### 8.3 Database (`<database>`)

| Parameter | Default | Meaning |
|---|---|---|
| `dsn` | empty | `postgres://user:password@127.0.0.1:5432/database?sslmode=disable` (switches on database mode) |
| `cdr-tables` | v_xml_cdr | Tables that receive call records |
| `poll-seconds` | 15 | How often configuration changes are checked |
| `write-registrations`, `write-channels`, `write-cdr` | true | What is written back |
| `default-domain` | empty | Domain for phones that register with the server's IP address |
| `manage-schema` | true | Create RingQswitch's tables when missing |
| `grant-to` | empty | Other database users allowed to use RingQswitch's tables |

### 8.4 Web portal (`<web>`)

| Parameter | Default | Meaning |
|---|---|---|
| `listen` | off | e.g. `0.0.0.0:8443` |
| `admin-user`, `admin-password` | admin | The server administrator (password of 8+ characters) |
| `tls` | self-signed | `self-signed`, `off` (behind a local proxy), or `cert.pem,key.pem` |
| `webrtc-port` | 8443 | UDP port for browser audio (0 = no dialer or meetings) |
| `public-ip` | ext-rtp-ip | Address browsers reach for audio |
| `owner-server-access` | true | Let company owners use the server-wide pages; turn off when several companies share a server |
| `brand` | RingQswitch | Product name shown in the portal |
| `data-dir` | /var/lib/ringqswitch | Certificates, keys, speech models |

### 8.5 XML examples

A dialplan extension:

```xml
<extension name="local_extensions">
  <condition field="destination_number" expression="^(10[0-9][0-9])$">
    <action application="set" data="call_timeout=30"/>
    <action application="bridge" data="user/$1"/>
  </condition>
</extension>
```

A trunk and an outbound route:

```xml
<!-- ringqswitch.xml -->
<gateway name="carrier">
  <param name="proxy" value="sip.carrier.example"/>
  <param name="username" value="account"/>
  <param name="password" value="secret"/>
  <param name="register" value="true"/>
</gateway>

<!-- dialplan/default.xml -->
<condition field="destination_number" expression="^9(\d+)$">
  <action application="bridge" data="ringq/gateway/carrier/$1"/>
</condition>
```

In database mode the same things are made in the portal (Call Flow, Outbound
rules) and stored in the RingQ database.

---

## 9. Running and operating

### 9.1 Service

```bash
sudo systemctl status ringqswitch
sudo systemctl restart ringqswitch
sudo systemctl reload ringqswitch        # reload the configuration (SIGHUP)
sudo journalctl -u ringqswitch -f        # follow the log
```

Daemon flags:

| Flag | Meaning |
|---|---|
| `-conf DIR` | Configuration directory (default `/etc/ringqswitch`) |
| `-check` | Validate the configuration (and database) and exit |
| `-compat` | Report dialplan applications and functions that are not supported, and exit |
| `-init-db` | Add RingQswitch's tables, default dialplans and AUX codes, and exit |
| `-web-access ext@domain [-role owner\|manager\|user]` | Give an extension portal access (password in `RQ_WEB_PASSWORD`), and exit |
| `-log-level LEVEL` | Override the log level |
| `-version` | Print the version |

### 9.2 Console

```bash
sudo rq_cli                          # live coloured log and a command prompt
ringqswitch@host> status
ringqswitch@host> show registrations
ringqswitch@host> show channels
ringqswitch@host> originate user/1000 9197     # ring 1000 and connect it to a test tone
ringqswitch@host> uuid_kill <uuid>
ringqswitch@host> reloadxml
ringqswitch@host> blocked                      # SIP Guard bans
ringqswitch@host> unblock 203.0.113.7
ringqswitch@host> /log info                    # less detail (/log debug, /nolog)
ringqswitch@host> /events plain ALL            # the event stream too
```

`rq_cli` reads the event socket address and password from
`/etc/ringqswitch/ringqswitch.xml` (run it as root). `rq_cli -x "show calls"`
runs one command and exits; `-H`, `-P`, `-p` and `-conf` connect to another
server.

### 9.3 Quick test (XML mode)

Point two SIP phones at the server with extensions `1000` and `1001` and the
`default_password` from `ringqswitch.xml`, then dial:

| Dial | Result |
|---|---|
| `1001` | Calls the other phone |
| `9196` | Echo test |
| `9197` | Test tone |
| `9198` | Records 20 seconds, then plays it back |
| `5000` | Demo IVR |
| `2000` / `2001` | Ring group / hunt group |

### 9.4 Behind NAT

Set `ext-sip-ip` and `ext-rtp-ip` to the public address and forward the SIP
port and the RTP range (UDP). The switch warns in the log when a call leg
receives no audio, naming the address and the port range to check.

---

## 10. Testing

```bash
make test                         # unit and end-to-end tests
make race                         # the same with the race detector
make test-db RINGQ_TEST_DSN='postgres://user:pass@127.0.0.1:5432/db?sslmode=disable'
RINGQ_TEST_DSN='postgres://...' go test ./internal/web/    # portal API tests against a database
```

- **Unit tests** per package: SIP parsing and digest vectors, transactions
  over UDP and TCP, SDP negotiation, codecs, RTP relay and latching, DTMF,
  dialplan semantics, fax modems, office hours rules, country lookup, SIP
  Guard counting.
- **End-to-end tests** start a real switch on the loopback interface and
  drive it with scripted SIP phones: registration, calls with audio both
  ways, transfers, hold, ring groups, IVR, queues, voicemail, conferences,
  BLF, fax, office hours, country restrictions, PIN dialing, SIP Guard.
- **Database and portal tests** run in a throw-away schema and exercise the
  JSON API as the portal does.
- `test/` holds interoperability and load tests run with an external SIP
  test tool.

---

## 11. Repository layout

```
cmd/
  ringqswitch/        the daemon
  rq_cli/             the console
internal/
  sip/ sdp/ media/    protocol and media layers
  dialplan/ config/   dialplan engine and configuration model
  core/               the switch (with built-in voice prompts in core/prompts/)
  fax/                fax engine
  db/                 RingQ database integration
  esl/                event socket
  web/                web server and API; web/static/ is the portal
  webphone/           SIP client for browser dialers
  transcribe/         call transcription and sentiment
  logbus/ testua/     live log stream; test phone
conf/                 sample configuration
packaging/            installer, systemd unit, database, certificate and transcription helpers
sounds/               sounds folder layout (prompts, music on hold)
docs/                 guides and references
test/                 interoperability and load tests
vendor/               vendored dependencies (offline builds)
Makefile              build, test, install and container targets
```

---

## 12. Documentation

| Document | Contents |
|---|---|
| [INSTALL.md](INSTALL.md) | Step-by-step installation, cloud networking, phones, troubleshooting |
| [docs/STEP-BY-STEP.md](docs/STEP-BY-STEP.md) | Moving a server onto the RingQ database, with a test database |
| [docs/WEB-UI.md](docs/WEB-UI.md) | The web portal, page by page |
| [docs/DATABASE.md](docs/DATABASE.md) | What is read from and written to the RingQ database |
| [docs/API.md](docs/API.md) | Event socket commands and events, and the portal's JSON API |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Call model, threading and module boundaries in more depth |
| [docs/ROADMAP.md](docs/ROADMAP.md) | What comes next |

---

## 13. License

The license is to be decided by Cal4Care; until then all rights are reserved.

RingQswitch includes a few small components from other authors under their
own licenses; they are listed in
[THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md).
