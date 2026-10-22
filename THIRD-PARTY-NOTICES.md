# Third-party notices

RingQswitch is original software by Cal4Care. It includes, or works with,
the components below from other authors, used under their licenses. The full
license texts are in the folders named here (`vendor/<module>/LICENSE` for the
Go modules).

## Go modules (vendored in `vendor/`, compiled into the binaries)

| Module | Version | License | Used for |
|---|---|---|---|
| github.com/jackc/pgx/v5 (with pgpassfile, pgservicefile, puddle/v2) | 5.7.4 | MIT | Database driver |
| github.com/pion/webrtc/v4 (with datachannel, dtls, ice, interceptor, logging, mdns, randutil, rtcp, rtp, sctp, sdp, srtp, stun, transport, turn) | 4.2.22 | MIT | Browser dialer and meetings |
| github.com/gorilla/websocket | 1.5.3 | BSD-2-Clause | WebSocket |
| github.com/hajimehoshi/go-mp3 | 0.3.4 | Apache-2.0 | MP3 sound uploads |
| github.com/google/uuid | 1.6.0 | BSD-3-Clause | Identifiers (via the WebRTC library) |
| github.com/wlynxg/anet | 0.0.5 | BSD-3-Clause | Network interfaces (via the WebRTC library) |
| golang.org/x/crypto, net, sync, sys, text, time | see `vendor/modules.txt` | BSD-3-Clause | Standard extensions |

## Fonts and data embedded in the portal

| Component | License | File |
|---|---|---|
| Inter typeface | SIL Open Font License 1.1 | `internal/web/static/fonts/OFL.txt` |
| Twemoji Country Flags font (country-flag-emoji-polyfill; flag art by Twemoji, Twitter, Inc. and contributors) | MIT (font packaging), CC-BY 4.0 (flag art) | `internal/web/static/fonts/TWEMOJI-FLAGS-LICENSE.md` |
| VADER sentiment lexicon (sentiment analysis of transcripts) | MIT | `internal/transcribe/VADER-LICENSE.txt` |

## Installed separately (not part of the RingQswitch binaries)

| Component | License | How |
|---|---|---|
| whisper.cpp speech recogniser and its speech models (call transcription) | MIT | Downloaded and built by `packaging/install-transcription.sh`, installed as `rq-speech` and `rq-speech-server`; runs as its own program |
| PostgreSQL (database server) | PostgreSQL License | Installed by the system's package manager (`packaging/setup-test-db.sh` for test servers) |

The AI agents use a cloud voice AI service chosen by the customer, with the
customer's own API key; no code of that service is included.
