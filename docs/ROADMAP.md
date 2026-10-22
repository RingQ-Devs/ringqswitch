# RingQswitch roadmap

This page shows what RingQswitch 0.10 does today and what is planned next.
Estimates assume a small team (2-3 engineers).

## Where 0.10 stands

| Capability | RingQswitch 0.10 |
|-----------|------------------|
| SIP over UDP/TCP, registrar, digest auth, several SIP profiles (ports) | ✅ |
| B2BUA call control, CANCEL, re-INVITE/UPDATE, hold | ✅ |
| NAT traversal (rport, symmetric RTP latching, external addresses) | ✅ |
| XML dialplan (regex, captures, anti-actions, time rules in the company's time zone, nested conditions, `<regex>` children, `${...}` expansion) | ✅ |
| Multi-domain configuration from the RingQ PostgreSQL database, or from XML files | ✅ |
| Ring groups, IVR menus (WAV and MP3 prompts; no text-to-speech), time conditions | ✅ |
| Recording (`record`, `record_session`, `uuid_record`; WAV, mono or stereo) | ✅ |
| SIP trunks with registration, outbound rules, caller ID rules | ✅ |
| Blind transfer (REFER) and attended transfer (REFER with Replaces) | ✅ |
| Voicemail with message-waiting lamps | ✅ (voicemail to email: planned) |
| Conferences (mixing, mute, kick, PIN, recording) | ✅ |
| Call queues / ACD (agents, tiers, strategies, announcements, wrap-up, AUX codes, reports, wallboard) | ✅ |
| Music on hold, park, pickup, intercom, call monitoring (listen, whisper, barge) | ✅ |
| BLF / SUBSCRIBE-NOTIFY (dialog, message-summary, feature events), desk phone keys | ✅ (PIDF presence: planned) |
| Event socket (commands, `bgapi`, plain and JSON events, filters, live log) | ✅ (per-call outbound socket: planned) |
| CDR to CSV and PostgreSQL, call flow timeline per call | ✅ |
| Web portal: configuration, live dashboard, wallboard, reports, user roles, contacts | ✅ |
| Browser dialer and video meetings (encrypted WebRTC audio and video) | ✅ |
| Chat with files, meetings with screen sharing, whiteboard and captions | ✅ |
| Fax: G.711 (V.21/V.29) and T.38, eFax portal module | ✅ (V.17 and V.27ter: planned) |
| Call and voicemail transcription with sentiment, live transcription during calls | ✅ (on the server, no audio leaves it) |
| AI agents that answer calls with a cloud voice AI service | ✅ |
| Language & office hours (weekly hours, breaks, holidays, exemptions) | ✅ |
| Security centre: SIP Guard, IP rules, allowed countries, PIN dialing, idle sign-out | ✅ |
| Single sign-on (OpenID Connect) | ✅ |
| Hotel front desk (check-in/out, wake-up calls) | ✅ |
| TLS for SIP, SRTP for desk phones | ❌ planned |
| Transcoding beyond G.711 (G.722, Opus, G.729...) | ❌ relayed untouched when both sides share a codec |
| DTMF detection in the audio | ❌ planned (RFC 4733 and SIP INFO work) |
| Text-to-speech prompts, `say` for numbers | ❌ planned |
| Scripting language for dialplan logic | ❌ (control via the event socket and the JSON API) |
| 100rel/PRACK, session timers | ❌ planned |
| Clustering / high availability | ❌ planned |

## Next: hardening (≈ 2-3 months)

1. **Field testing** with more desk phone models and carriers. Fix interop
   issues and add a regression test for each one.
2. **TLS transport** for SIP and **SRTP (SDES)** for desk phones.
3. **DTMF detection in the audio** (Goertzel) for phones without RFC 4733.
4. **Voicemail to email** and listening to voicemail from the phone menu.
5. **Prompts package** and a `say` application for numbers and dates.
6. Metrics endpoint, log rotation, deb/rpm packages.

## Then: contact centre and media (≈ 3-4 months)

1. **Queues**: skills-based routing, callbacks instead of waiting.
2. **Codecs**: G.722 and Opus transcoding with a jitter buffer; G.729 when
   licensing allows.
3. **Presence**: PIDF presence for phones that show colleagues' status.
4. **Conferences**: wideband audio, moderator controls, deaf members.
5. **Fax**: V.17 and V.27ter page modems, error correction mode.
6. **Outbound event socket**: per-call control from external applications.

## Later: platform (≈ 4-6 months)

1. **SIP over WebSocket** for WebRTC phones from other vendors.
2. **Multi-tenant limits**: calls, channels and storage per domain.
3. **Scripting**: an embedded language for dialplan logic.
4. **High availability**: shared registration state, SIP load balancing,
   graceful drain.
5. **Session timers** and **100rel/PRACK**.

## Engineering principles

* Keep the switch core free of dependencies; the few the web layer needs
  are vendored (see [THIRD-PARTY-NOTICES.md](../THIRD-PARTY-NOTICES.md)).
* Every interop bug gets a regression test: a unit test, an end-to-end test
  with `testua`, or an interop scenario in `test/interop/`.
* Keep names stable (dialplan applications, channel variables, hangup
  causes, event names, event socket commands), so the RingQ portal and
  existing integrations keep working across releases.
