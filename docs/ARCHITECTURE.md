# RingQswitch architecture

## Packages

| Package | Responsibility |
|---------|----------------|
| `internal/sip` | Message/URI/header parsing, digest auth, UDP/TCP transport, RFC 3261 transaction layer (`Stack`), DNS resolution |
| `internal/sdp` | SDP parse/marshal and offer/answer helpers (select, answer, passthrough offer, payload type mapping) |
| `internal/media` | RTP endpoints and port allocator, relay/local/echo modes, G.711, WAV read/write, tone generation, RFC 4733 DTMF |
| `internal/dialplan` | XML dialplan loader and hunter (contexts, extensions, conditions, regex captures, anti-actions, `continue`/`break`) |
| `internal/config` | Loads `conf/` with `$${var}` pre-processing into typed settings, multi-domain directory, gateways, ACLs, dialplan, features (ring groups, IVR menus) |
| `internal/core` | The switch: registrar, channels, B2BUA call control, executor, `${...}` expansion, applications (including the ones the RingQ portal's dialplans use, ring groups, IVR, session recording), gateways, events, API, CDR |
| `internal/db` | PostgreSQL (the RingQ portal schema): `Overlay` loads the configuration tables into a `config.Config`, `Watch` reloads on change, `Writer` mirrors events into `registrations`/`channels`/`calls` and inserts CDRs. Core does not import it: `main` wires it through `Core.SetLoader` and the event bus |
| `internal/esl` | Event socket server and client |
| `internal/fax` | Fax engine: V.21 control channel, V.29 page modem, T.30 session, T.4 page coding, T.38 over UDPTL |
| `internal/web` | HTTPS server, JSON API, WebSocket hub, browser audio (WebRTC) for the dialer and meetings, and the embedded portal (`web/static/`) |
| `internal/webphone` | The SIP client inside the switch that each browser dialer registers as |
| `internal/transcribe` | Call and voicemail transcription (speech recogniser driver, voice activity, speakers) and sentiment |
| `internal/logbus` | Live log stream for `rq_cli` and the event socket `log` command |
| `internal/testua` | Scriptable SIP phone used by the end-to-end tests |
| `cmd/ringqswitch` | Daemon (signals, reload, logging) |
| `cmd/rq_cli` | Console client |

Dependencies point one way: `sip`, `sdp`, `media` and `dialplan` know nothing
about the core. `core` composes them, and `esl` sits on top of `core`.

## Call model

Every call leg is a **channel**. A basic call has two:

```
 phone A ──SIP dialog 1──► [channel A: inbound]  ══bridge══  [channel B: outbound] ──SIP dialog 2──► phone B
 phone A ◄──RTP──► endpoint A (port 16384) ◄═relay═► endpoint B (port 16386) ◄──RTP──► phone B
```

* Each channel owns one SIP dialog (Call-ID, tags, CSeq, route set, target)
  and one RTP endpoint (an even RTP port plus RTCP on port+1).
* In-dialog requests go to the address the dialog was set up with (the
  INVITE source for inbound legs, the registration source for outbound legs).
  This is what makes phones behind NAT work without STUN.
* Media is always anchored on the switch. SDP from the caller is re-offered
  to the callee with our address (codec **passthrough**, no transcoding), and
  the callee's answer is translated back into the caller's payload numbering.

### Inbound INVITE

1. The transaction layer creates a server transaction and hands it to
   `Core.OnRequest`, which sends `100 Trying`.
2. The source is classified: gateway address → gateway context; ACL match →
   ACL context; otherwise digest authentication (407) → the user's
   `user_context`.
3. A channel is created with variables (`destination_number`,
   `caller_id_number`, `network_addr`, directory user variables...) and an
   RTP endpoint pointed at the caller's SDP.
4. The goroutine becomes the channel's **executor**. It hunts the dialplan
   for the context and runs each action's application in order.
5. When the actions run out, the channel hangs up (`NORMAL_CLEARING`).

### Bridge

`bridge <dialstring>` calls `originate`, which:

* parses the dial string (`,` parallel, `|` sequential, `{}`/`[]` variables);
* resolves targets (`user/` → registrar bindings, `gateway/` → trunk, `sip:`);
* creates an outbound channel per destination and sends the INVITEs;
* relays 180s as ringing and, for a single leg, bridges early media (183);
* returns the first leg that answers and cancels the others (`LOSE_RACE`).

The A leg is then answered with SDP derived from the winner's answer. Both
endpoints switch to relay mode, and the bridge waits until either side hangs
up or a transfer detaches it.

### Transfers

`transfer` (dialplan), `uuid_transfer` (API) and SIP REFER all end up in
`Core.transfer`:

* a channel running the dialplan gets a pending transfer, and its current
  application's context is cancelled, so the executor re-hunts the dialplan
  with the new destination;
* a bridged outbound leg (no executor) is detached from its bridge and gets
  its own executor.

## Concurrency

* One goroutine reads UDP, one accepts TCP, and one reads each TCP connection.
  Messages are dispatched to the transaction layer synchronously. New
  requests are handed to the core on their own goroutine.
* Each channel with an executor has one goroutine running applications.
  Applications receive a `context.Context` that is cancelled on hangup or
  transfer, so every blocking operation is interruptible.
* Each RTP endpoint has a reader goroutine for RTP and one for RTCP. In local
  mode (the switch plays or records audio) a 20 ms media clock goroutine sends
  queued frames or silence. `WriteFrame` blocks on a small queue, which paces
  playback in real time.
* The event bus never blocks publishers. Slow subscribers drop events, and
  each subscription counts its drops.

## Timers and reliability

The transaction layer implements RFC 3261 timers A-K (T1 = 500 ms, T2 = 4 s).
RFC 6026's "Accepted" state absorbs INVITE retransmissions after a 2xx.
Retransmitted 2xx responses are re-ACKed automatically. A 2xx that is never
ACKed hangs the call up. A CANCELled INVITE that never receives a final
response is terminated after 64·T1.

## Testing strategy

* Unit tests per package: the parser (including the RFC 2617 digest vector),
  transactions over UDP and TCP, SDP negotiation, G.711 code words, RTP
  relay, transcoding and latching, DTMF, dialplan semantics and config
  validation.
* End-to-end tests (`internal/core/integration_test.go`) start a real switch
  on loopback and drive it with `testua` phones, checking SIP and RTP.
* Interoperability tests (`test/interop/run.sh`) run an independent SIP
  traffic generator against the daemon over UDP and TCP, including a load
  test.
