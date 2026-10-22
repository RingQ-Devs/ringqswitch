# RingQswitch control API: event socket reference

RingQswitch is controlled through its **event socket**: a plain-text TCP
protocol. One connection can run commands, receive asynchronous results and
stream real-time events. This is the interface for your API servers,
wallboards and CTI integrations, and it is what `rq_cli` uses.

> **Stable for integrations.** The protocol, command names, JSON shapes and
> event format are stable: the RingQ portal and existing event socket client
> libraries work unchanged (connect, `api`, `bgapi`, `show ... as json`, event
> subscriptions, filters). Section 9 lists what is supported and what is not
> yet implemented. Section 11 lists every port RingQswitch listens on.

All examples below were captured from a running RingQswitch 0.1.0.

---

## 1. Connecting

| Setting (`ringqswitch.xml`) | Default | Notes |
|---|---|---|
| `event-socket-listen` | `127.0.0.1:8021` | empty value disables the socket |
| `event-socket-password` | (required) | sent with `auth` |

Keep it bound to `127.0.0.1`. To reach it from another machine, use an SSH
tunnel (`ssh -L 8021:127.0.0.1:8021 root@pbx`) or run your API server on the
PBX host. Never expose 8021 to the internet: anyone with the password can
place calls.

### Framing

Messages from the server are a header block, a blank line, then an optional
body of `Content-Length` bytes:

```
Content-Type: api/response
Content-Length: 22

RingQswitch 0.1.0
```

Commands from the client are a command line, optional `Name: value` header
lines, and a blank line (`\n\n`).

### Session

```
<- Content-Type: auth/request

-> auth <password>

<- Content-Type: command/reply
<- Reply-Text: +OK accepted          (or "-ERR invalid", then disconnect)
```

The client has 15 seconds to authenticate. Failed attempts are logged as
`event socket authentication failed`.

---

## 2. Socket commands

| Command | Reply | Description |
|---|---|---|
| `api <command> [args]` | `api/response` with the output as body | Run an API command (section 3) and wait for the result |
| `bgapi <command> [args]` (header `Job-UUID: <id>` optional) | `command/reply` `+OK Job-UUID: <id>` | Run in the background. The result arrives as a `BACKGROUND_JOB` event with that `Job-UUID` and the output as body. If you send a `Job-UUID` header it is used (client libraries rely on this) |
| `event plain\|json <EVENTS...>` | `+OK event listener enabled plain` | Subscribe. `ALL` = everything. `CUSTOM ringq::register ...` = selected custom subclasses. `CUSTOM` alone = every custom event. Subscriptions **accumulate** across calls |
| `nixevent <EVENTS...>` | `+OK events nixed` | Remove events from the subscription |
| `noevents` | `+OK no longer listening for events` | Stop all events |
| `filter <Header> <value>` | `+OK filter added. [Header]=[value]` | Only deliver events whose header equals the value. Several filters are OR-ed |
| `filter delete <Header> [<value>]` | `+OK filter deleted. ...` | Remove filters |
| `myevents <uuid> [plain\|json]` | `+OK Events Enabled` | All events of one channel (shortcut for `event ALL` + `filter Unique-ID <uuid>`) |
| `sendmsg <uuid>` + header `call-command: hangup` (+ `hangup-cause: USER_BUSY`) | `+OK` | Hang up a channel. Other call-commands reply `-ERR ... not supported yet` |
| `log [level]` | `+OK log level DEBUG [7]` | Follow the log, as `rq_cli` does: `log/data` messages (headers `Log-Level` 0-7, `Log-File`, `Log-Line`, `Log-Func`, `User-Data` = the call's uuid) whose body is the console line: `2026-10-09 10:18:22.557704 [NOTICE] inbound.go:170 New Channel ...`, or the dialplan trace as is (`Dialplan: ... Regex (PASS) [ext] destination_number(1021) =~ /^1021$/ break=on-false`, `EXECUTE [depth=0] ... bridge(...)`). Level: `7`/`debug` (everything, with the dialplan trace), `6`/`info`, `5`/`notice`, `4`/`warning`, `3`/`err`; independent of the service's own log level |
| `nolog` | `+OK no longer logging` | Stop following the log |
| `linger`, `nolinger`, `divert_events` | `+OK` | Accepted for compatibility (no effect) |
| `exit` | `+OK bye` + `text/disconnect-notice` | Close the connection |

Unknown commands reply `-ERR command not found`.

---

## 3. API commands

Use them through `api` / `bgapi`, `rq_cli -x "<command>"`, or the `rq_cli`
prompt. On failure the output starts with `-ERR`.

### `status`

```
UP 2s
RingQswitch 0.10.1 is ready (core e02d33d9-6291-4cf9-85e2-292ec4cad8a1)
1 session(s) since startup
1 session(s) active - max 1000
1 registration(s)
0 address(es) blocked for failed authentication
1 rtp port pair(s) in use (range 16384-32768)
4 sip transaction(s) (4 server, 0 client)
sip messages: 6 received, 6 sent, 0 not SIP, 0 invalid, 0 from blocked addresses, 0 send errors
SIP 192.0.2.2:5072 (tcp=true) domain 192.0.2.2
memory 0 MiB, 14 goroutines
```

### `version`

`RingQswitch 0.10.1`

### `show registrations [as json]`

```json
{
  "row_count": 1,
  "rows": [
    {
      "reg_user": "1001",
      "realm": "192.0.2.2",
      "token": "1-2605@127.0.0.1",
      "url": "sip:1001@127.0.0.1:5150;transport=UDP",
      "expires": "1791274176",
      "network_ip": "127.0.0.1",
      "network_port": "5150",
      "network_proto": "udp",
      "hostname": "vm",
      "metadata": "Softphone/1.0"
    }
  ]
}
```

The fields are the columns of the `registrations` table (also in your
database). `expires` is a Unix epoch. `metadata` is the phone's
User-Agent.

### `show channels [as json]` / `show calls [as json]`

`show calls` lists each bridged call once (by its A leg).

```json
{
  "row_count": 1,
  "rows": [
    {
      "uuid": "65da23f5-1f1c-4696-8867-0cee8131b062",
      "direction": "inbound",
      "created": "2026-10-06 08:04:36",
      "created_epoch": "1791273876",
      "name": "sip/1000@127.0.0.1",
      "state": "CS_EXECUTE",
      "callstate": "ACTIVE",
      "cid_name": "Reception",
      "cid_num": "1000",
      "ip_addr": "127.0.0.1",
      "dest": "9197",
      "application": "playback",
      "context": "default",
      "read_codec": "PCMU",
      "call_uuid": "65da23f5-1f1c-4696-8867-0cee8131b062",
      "bridged_to": ""
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `state` | `CS_NEW`, `CS_ROUTING`, `CS_EXECUTE`, `CS_EXCHANGE_MEDIA` (bridged), `CS_HANGUP` |
| `callstate` | `DOWN`, `RINGING`, `EARLY`, `ACTIVE`, `HANGUP` |
| `call_uuid` | UUID of the A leg (same on both legs of a call) |
| `bridged_to` | UUID of the other leg while bridged |

### `show gateways [as json]`

Rows: `name`, `proxy`, `register`, `state` (`NOREG`, `TRYING`, `REGED`,
`FAIL_WAIT`, `UNREGED`), `last_error`.

### `show applications`

Lists the dialplan applications.

### `uuid_dump <uuid>`

All channel variables:

```
Channel-State: CS_EXECUTE
Channel-Call-State: ACTIVE
Channel-Name: sip/1000@127.0.0.1
variable_caller_id_name: Reception
variable_caller_id_number: 1000
variable_context: default
variable_destination_number: 9197
variable_direction: inbound
variable_effective_caller_id_name: Extension 1000
variable_network_addr: 127.0.0.1
variable_sip_call_id: 1-2607@127.0.0.1
variable_user_name: 1000
variable_uuid: 65da23f5-1f1c-4696-8867-0cee8131b062
...
```

### Call control

| Command | Example | Result |
|---|---|---|
| `uuid_kill <uuid> [CAUSE]` | `uuid_kill 65da23f5-... USER_BUSY` | `+OK` (default cause `MANAGER_REQUEST`) |
| `uuid_transfer <uuid> <dest> [context]` | `uuid_transfer 65da23f5-... 5000 default` | `+OK`. The channel leaves its current app (e.g. a bridge) and runs the dialplan for `dest` |
| `uuid_send_dtmf <uuid> <digits>` | `uuid_send_dtmf 65da23f5-... 123#` | `+OK` (sent as SIP INFO) |
| `uuid_setvar <uuid> <var> [value]` | `uuid_setvar 65da... agent_id 42` | `+OK` (no value unsets) |
| `uuid_setvar_multi <uuid> a=1;b=2` | | `+OK` |
| `uuid_getvar <uuid> <var>` | `uuid_getvar 65da... billsec` | the value, or `_undef_` |
| `uuid_exists <uuid>` | | `true` / `false` |
| `uuid_answer <uuid>` | | `+OK` |
| `uuid_park <uuid>` | | `+OK`: the channel leaves what it was doing and waits |
| `uuid_bridge <uuid> <other_uuid>` | | `+OK <other_uuid>`: connects two existing channels |
| `uuid_record <uuid> start\|stop <path>` | `uuid_record 65da... start /var/lib/ringqswitch/recordings/x.wav` | `+OK Success`: records both parties (WAV) |
| `uuid_broadcast <uuid> <file> [aleg\|bleg\|both]` | | `+OK Message sent`: plays a file to the channel(s) |
| `hupall [CAUSE]` | `hupall` | `+OK hung up 3 channel(s)` |

### `originate`

```
originate <dialstring> <extension>|&<app>(<data>) [XML] [context] [cid_name] [cid_num] [timeout_sec]
```

Arguments are separated by spaces. Quote an argument that contains spaces
(`"Front Desk"`). `{...}` and `[...]` blocks stay together.

Rings the dial string. When it answers, runs `extension` through the
dialplan (default context `default`), or runs the single application. Blocks
until answered or failed. Use `bgapi originate ...` to avoid blocking.

| Example | What it does |
|---|---|
| `originate user/1000 9197` | Ring extension 1000, then connect it to the tone |
| `originate user/1000 1001 XML default "Front Desk" 200` | Click-to-call: ring 1000, then dial 1001 with caller ID "Front Desk" <200> |
| `originate {origination_caller_id_name='Front Desk',call_timeout=20}user/1000 9197` | Variables in `{...}`; spaces inside quotes are fine |
| `originate {origination_caller_id_number=6567157042}gateway/main/91234567 &playback(/var/lib/ringqswitch/sounds/promo.wav)` | Call out via a trunk and play a file |
| `originate user/1000,user/1001 2000` | Ring both, first to answer wins |

Result: `+OK <uuid>`, or `-ERR <CAUSE>` (e.g. `-ERR USER_NOT_REGISTERED`,
`-ERR NO_ANSWER`).

Dial strings: `user/<ext>[@domain]`, `gateway/<name>/<number>`,
`ringq/gateway/<name or gateway_uuid>/<number>`, `ringq/internal/<user@domain>`
(the older spellings `sofia/gateway/...` and `sofia/internal/...` are accepted too, for existing integrations),
`sip:user@host[:port]`, `loopback/<number>[/<context>]` (whatever the
dialplan bridges that number to), `error/<CAUSE>`. Use `,` for parallel, `|` for sequential, `{var=val}` for
global variables and `[var=val]` for per-leg variables. Useful variables:
`origination_caller_id_name`, `origination_caller_id_number`, `call_timeout`,
`leg_timeout`, and `sip_h_X-Header=value` (adds a SIP header to the INVITE).

### Administration

| Command | Result |
|---|---|
| `reloadxml` | Reload the configuration (XML and database): directory, dialplan, gateways, ACLs, variables, ring groups, IVR menus. `+OK [Success]`. Ports and the RTP range need a restart |
| `memcache delete <key>` / `memcache flush` / `xml_flush_cache` / `cache flush` | `+OK`; the configuration is reloaded within a second (the RingQ portal sends these after every change) |
| `sip status` | Profile and gateways (`sofia ...` is an alias of `sip ...`, kept for existing portal integrations) |
| `sip status gateway <name\|uuid>` | Gateway details |
| `sofia xmlstatus profile internal reg [user]` | Registrations as XML |
| `sofia xmlstatus gateway [name\|uuid]` | Gateways as XML (`<state>REGED</state>`...) |
| `sofia profile <p> rescan\|restart` | Reload |
| `sofia profile <p> killgw <gw>` | Stop (and unregister) a gateway until the next reload |
| `sofia profile <p> register\|unregister <gw>` | Register / unregister a gateway now |
| `sofia profile <p> flush_inbound_reg [user@domain]` | Drop registrations |
| `domains` | Domains with user and registration counts |
| `compat` | Dialplan applications and functions in the loaded configuration that are not supported |
| `show channels\|calls\|registrations count` | `N total.` |
| `global_getvar [name]`, `global_setvar name=value` | Global variables |
| `user_exists id <ext> <domain>`, `user_data <ext>@<domain> var\|param <name>`, `domain_exists <domain>`, `sofia_contact <ext>@<domain>`, `sofia_count_reg <ext>@<domain>`, `uuid_exists_func <uuid>`, `create_uuid`, `strftime <fmt>`, `strepoch <date>`, `epoch`, `expr`, `eval`, `regex`, `cond`, `md5`, `url_encode`, `url_decode`, `hash`, `db` | The same functions as in `${...}`, run as commands |
| `uptime [s\|ms\|m\|h\|d]`, `hostname`, `switchname`, `module_exists <mod>`, `fsctl send_sighup`, `load\|reload\|unload <module>` | `reload` reloads the configuration; `load` and `unload` are accepted for existing integrations (no effect) |
| `luarun app.lua event_notify <profile> reboot\|check_sync <user@domain> [vendor]` | Desk phones: reboot or re-provision the phones registered as that extension (`+OK sent to N phone(s)`); the portal's phone reboot button sends this. Other scripts are not run |
| `show_subscriptions` | The phones' BLF, message-waiting and do-not-disturb subscriptions (JSON) |
| `blocked` | Addresses blocked after failed authentications |
| `unblock <ip>` | `+OK` |
| `help` | Command list |

### Call centre (`callcenter_config`)

| Command | Result |
|---|---|
| `callcenter_config agent set status <agent> 'Available'` | `+OK`. `<agent>` is the agent uuid, name, agent ID or extension. Statuses: `Available`, `Available (On Demand)`, `On Break`, `Logged Out` |
| `callcenter_config agent set status <agent> 'On Break' Lunch` | On Break with an AUX code (default `Break`) |
| `callcenter_config agent set aux <agent> Lunch` | Same as above |
| `callcenter_config agent get status\|state\|aux <agent>` | The value |
| `callcenter_config agent list [agent]` | The standard agent columns, `\|` separated (`name\|instance_id\|uuid\|type\|contact\|status\|state\|...`) |
| `callcenter_config queue list` | Queues and their settings |
| `callcenter_config queue list agents\|members\|tiers <queue>` | `<queue>` is `<ext>@<domain>`, the queue name or uuid |
| `callcenter_config queue count agents\|members\|tiers <queue> [status]` | A number |
| `callcenter_config tier list [queue]` | queue, agent, level, position |
| `callcenter_config agent\|tier add/del/set ...`, `queue load\|reload` | `+OK`; agents, tiers and queues are configured in the database (`v_call_center_*`), so these reload it |
| `conference list` | Running rooms and members |
| `conference <room> mute\|unmute\|kick <member uuid>` | `+OK` |

Every agent change publishes `CUSTOM callcenter::info` with `CC-Action:
agent-status-change` (headers `CC-Agent`, `CC-Agent-Name`,
`CC-Agent-Extension`, `CC-Agent-Status`, `CC-Agent-Aux`,
`CC-Agent-Previous-Status`, `CC-Agent-Previous-Aux`,
`CC-Agent-Previous-Seconds`, `CC-Source`) and `agent-state-change`. Queue
callers produce `member-queue-start`, `bridge-agent-start`,
`bridge-agent-end`, `bridge-agent-fail` and `member-queue-end` (with
`RQ-Result`, `RQ-Wait-Seconds`, `RQ-Talk-Seconds`). Voicemail produces
`CUSTOM vm::maintenance` (`VM-Action: leave-message`).

---

## 4. Events

Subscribe with `event plain ...` or `event json ...`. Plain events have
URL-encoded values. JSON events are flat objects (body in `_body`). Every
event has `Event-Name`, `Event-Date-Local`, `Event-Date-Timestamp`
(microseconds) and `Core-UUID`.

### Channel events

| Event | When |
|---|---|
| `CHANNEL_CREATE` | A call leg is created (includes all `variable_*`) |
| `CHANNEL_PROGRESS` | Ringing (180) |
| `CHANNEL_PROGRESS_MEDIA` | Early media (183) |
| `CHANNEL_ANSWER` | Answered |
| `CHANNEL_BRIDGE` / `CHANNEL_UNBRIDGE` | Two legs connected / disconnected (`Bridge-A-Unique-ID`, `Bridge-B-Unique-ID`) |
| `CHANNEL_EXECUTE` / `CHANNEL_EXECUTE_COMPLETE` | A dialplan application starts / ends (`Application`, `Application-Data`) |
| `CHANNEL_HANGUP` | Hangup (`Hangup-Cause`) |
| `CHANNEL_CALLSTATE` | Call state changed (`Channel-Call-State`: `RINGING`, `EARLY`, `ACTIVE`, `HANGUP`) |
| `CHANNEL_HANGUP_COMPLETE` | Final event with every variable, including the CDR fields `start_stamp`/`_epoch`, `answer_stamp`/`_epoch`, `end_stamp`/`_epoch`, `duration`, `billsec`, `billmsec`, `hangup_cause`, `hangup_cause_q850`, `sip_hangup_disposition`, `last_app`, `record_path`, `record_name` |
| `CHANNEL_DESTROY` | After `CHANNEL_HANGUP_COMPLETE` |
| `DTMF` | Digit received (`DTMF-Digit`) |

Channel events (all but `DTMF`) carry every channel
variable as `variable_<name>` headers, plus: `Unique-ID`, `Channel-Name`,
`Channel-State`, `Channel-Call-State`, `Answer-State`
(`ringing`/`early`/`answered`/`hangup`), `Call-Direction`,
`Presence-Call-Direction`, `Channel-Presence-ID`, `Channel-Read-Codec-Name`,
`Channel-Write-Codec-Name`, `Caller-Direction`, `Caller-Username`,
`Caller-Dialplan`, `Caller-Caller-ID-Name`, `Caller-Caller-ID-Number`,
`Caller-Orig-Caller-ID-*`, `Caller-Callee-ID-Name`/`-Number`, `Caller-ANI`,
`Caller-Destination-Number`, `Caller-Unique-ID`, `Caller-Source`,
`Caller-Context`, `Caller-Channel-Name`, `Caller-Channel-Created-Time`,
`-Answered-Time`, `-Progress-Time`, `-Hangup-Time` (microseconds), and while
bridged `Other-Type`, `Other-Leg-Unique-ID`, `Other-Leg-Caller-ID-Name`,
`Other-Leg-Caller-ID-Number`, `Other-Leg-Destination-Number`. Every event
has `RingQswitch-Hostname`, `RingQswitch-Switchname`, `RingQswitch-IPv4`,
`Core-UUID` and `Event-Sequence` (the three host headers are also sent under
the older prefix that existing integrations filter on).

Example (plain):

```
Event-Name: CHANNEL_ANSWER
Event-Date-Local: 2026-10-06%2008%3A04%3A36
Event-Date-Timestamp: 1791273876626477
Unique-ID: 65da23f5-1f1c-4696-8867-0cee8131b062
Channel-Name: sip%2F1000%40127.0.0.1
Channel-State: CS_EXECUTE
Channel-Call-State: ACTIVE
Answer-State: answered
Call-Direction: inbound
Caller-Caller-ID-Name: Reception
Caller-Caller-ID-Number: 1000
Caller-Destination-Number: 9197
Caller-Context: default
Caller-Network-Addr: 127.0.0.1
Core-UUID: e02d33d9-6291-4cf9-85e2-292ec4cad8a1
```

### Custom events (`Event-Name: CUSTOM`)

| `Event-Subclass` | When | Headers |
|---|---|---|
| `sofia::register`, `sofia::unregister`, `sofia::expire` | Same moments as the `ringq::` events below, in the format existing portal integrations read | `profile-name`, `from-user`, `from-host`, `contact`, `call-id`, `status`, `expires`, `to-user`, `to-host`, `network-ip`, `network-port`, `username`, `realm`, `user-agent` |
| `ringq::register` | REGISTER accepted (also on refresh) | `User`, `Realm`, `Contact`, `Call-ID`, `Network-Addr`, `Network-IP`, `Network-Port`, `Network-Proto`, `User-Agent`, `Expires` |
| `ringq::unregister` | Phone unregistered or its TCP connection closed | same + `Reason` |
| `ringq::expire` | Registration expired | same |
| `ringq::auth_block` | Address blocked after failed logins | `Network-IP` |
| `ringq::guard` | SIP Guard banned an address, a ban was lifted or ended | `Action` (`ban`, `unban`, `expire`), `Network-IP`, `Jail` (`sip-request`, `sip-registration`, `sip-password`, `sip-invite`, `web-request`, `web-traffic`, `web-login`), `Reason`, `Failures`, `Ban-Since`, `Ban-Until` (epoch), `Source` (`auto`, `manual`), `Actor` |
| `ringq::gateway_state` | Trunk registration state changed | `Gateway`, `State` |
| `ringq::web_login_failed` | A portal sign-in failed (feeds SIP Guard's web-login protection) | `User`, `Network-IP` |
| `ringq::blf` | Lamps of phone keys changed (a call to or from one of these numbers started or ended, a parking slot filled or emptied) | `Domain`, `Resources` (the numbers, comma separated) |
| `ringq::user_vars` | An extension's settings changed from a phone or a dial code (do-not-disturb, call forward, agent status) | `User`, `Domain`, `Source`, and `Var-<name>` for each changed setting |
| `callcenter::info` | Queue and agent activity (section 3, "Call centre") | `CC-Action`, `CC-Queue`, `CC-Queue-Name`, `CC-Agent`, `CC-Agent-Status`, `CC-Member-UUID`, `CC-Member-CID-Name`, `CC-Member-CID-Number`... |
| `conference::maintenance` | Someone joined or left a conference room | `Action` (`add-member`, `del-member`), `Conference-Name`, `Conference-Domain`, `Unique-ID`, `Caller-Caller-ID-Number`, `Caller-Caller-ID-Name`, `Conference-Size` |
| `vm::maintenance` | A voicemail message was left | `VM-Action` (`leave-message`), `VM-User`, `VM-Domain`, `VM-UUID`, `VM-Message-UUID`, `VM-Caller-ID-Name`, `VM-Caller-ID-Number`, `VM-Message-Len` (seconds), `VM-File-Path`, `VM-Created` (epoch) |

### System events

| Event | Headers |
|---|---|
| `HEARTBEAT` (every 20 s) | `Up-Time`, `Session-Count`, `Session-Since-Startup`, `Registrations` |
| `BACKGROUND_JOB` | `Job-UUID`, `Job-Command`, body = output |
| `RELOADXML` | after a reload |
| `SHUTDOWN` | the switch is stopping |

---

## 5. Client examples

### Node.js (no packages needed), tested

```js
// Node.js 18 or later, no packages needed.
const net = require('net');

const sock = net.connect(Number(process.env.RQ_ESL_PORT || 8021), '127.0.0.1');
const send = (cmd) => sock.write(cmd + '\n\n');
let buf = Buffer.alloc(0);

// Each message: header lines, a blank line, then Content-Length bytes of body.
sock.on('data', (chunk) => {
  buf = Buffer.concat([buf, chunk]);
  for (;;) {
    const end = buf.indexOf('\n\n');
    if (end < 0) return;
    const headers = {};
    for (const line of buf.subarray(0, end).toString().split('\n')) {
      const i = line.indexOf(': ');
      if (i > 0) headers[line.slice(0, i)] = line.slice(i + 2);
    }
    const len = Number(headers['Content-Length'] || 0);
    if (buf.length < end + 2 + len) return;
    const body = buf.subarray(end + 2, end + 2 + len).toString();
    buf = buf.subarray(end + 2 + len);
    onMessage(headers, body);
  }
});

function onMessage(h, body) {
  switch (h['Content-Type']) {
    case 'auth/request':
      send('auth ' + process.env.RQ_ESL_PASSWORD);
      break;
    case 'command/reply':
      if (h['Reply-Text'] === '+OK accepted') {
        send('api show registrations as json');
        send('event json CHANNEL_ANSWER CHANNEL_HANGUP_COMPLETE CUSTOM ringq::register');
      } else if (h['Reply-Text'].startsWith('-ERR')) {
        console.error(h['Reply-Text']);
      }
      break;
    case 'api/response':
      for (const r of JSON.parse(body).rows || []) console.log('registered', r.reg_user, r.network_ip, r.metadata);
      break;
    case 'text/event-json': {
      const e = JSON.parse(body);
      if (e['Event-Name'] === 'CHANNEL_ANSWER') console.log('answered', e['Unique-ID'], e['Caller-Destination-Number']);
      if (e['Event-Name'] === 'CHANNEL_HANGUP_COMPLETE') console.log('ended', e['Hangup-Cause'], e.variable_billsec + 's');
      if (e['Event-Subclass'] === 'ringq::register') console.log('registered', e.User, e['Network-IP']);
      break;
    }
  }
}
```

Run it with `RQ_ESL_PASSWORD=... node client.js`.

### Python (standard library only)

```python
import socket, urllib.parse

def read(f):
    headers = {}
    while True:
        line = f.readline().decode().rstrip("\r\n")
        if not line:
            if headers: break
            continue
        k, _, v = line.partition(": ")
        headers[k] = v
    n = int(headers.get("Content-Length", 0))
    return headers, f.read(n).decode() if n else ""

s = socket.create_connection(("127.0.0.1", 8021)); f = s.makefile("rb")
read(f)                                            # auth/request
s.sendall(b"auth MY-PASSWORD\n\n"); print(read(f)[0]["Reply-Text"])
s.sendall(b"api show channels as json\n\n"); print(read(f)[1])
s.sendall(b"event plain CHANNEL_ANSWER CHANNEL_HANGUP_COMPLETE\n\n"); read(f)
while True:
    _, body = read(f)
    ev = {k: urllib.parse.unquote(v) for k, _, v in (l.partition(": ") for l in body.splitlines())}
    print(ev.get("Event-Name"), ev.get("Unique-ID"), ev.get("Hangup-Cause", ""))
```

### Command line

```bash
sudo rq_cli -x "show registrations as json"   # the password is read from /etc/ringqswitch/ringqswitch.xml
sudo rq_cli                   # interactive, with the live log; /log info, /nolog, /events plain ALL
rq_cli -H 10.0.0.5 -P 8021 -p PASSWORD        # another server
```

---

## 6. Building a real-time wallboard

1. Your backend opens one event socket connection and subscribes to
   `CHANNEL_CREATE CHANNEL_ANSWER CHANNEL_BRIDGE CHANNEL_HANGUP_COMPLETE CUSTOM ringq::register ringq::unregister ringq::expire HEARTBEAT`.
2. At startup (and on reconnect) it loads the current state with
   `show channels as json` and `show registrations as json`.
3. It applies events to that state and pushes changes to browsers over its
   own WebSocket. Browsers never connect to the event socket
   directly.
4. `CHANNEL_HANGUP_COMPLETE` carries every CDR field. Write it to your CDR
   table from there, or read the CSV CDR file.

Queue and agent metrics come from `CUSTOM callcenter::info` events (section
3, "Call centre"), or poll `GET /api/live` of the web interface (section 10),
which returns the agents and queues with today's counters.

---

## 7. Reference

### Hangup causes

`NORMAL_CLEARING`, `USER_BUSY`, `NO_ANSWER`, `NO_USER_RESPONSE`,
`CALL_REJECTED`, `UNALLOCATED_NUMBER`, `NO_ROUTE_DESTINATION`,
`USER_NOT_REGISTERED`, `ORIGINATOR_CANCEL`, `LOSE_RACE`,
`RECOVERY_ON_TIMER_EXPIRE`, `INCOMPATIBLE_DESTINATION`,
`NORMAL_TEMPORARY_FAILURE`, `DESTINATION_OUT_OF_ORDER`,
`INVALID_NUMBER_FORMAT`, `MEDIA_TIMEOUT`, `MANAGER_REQUEST`,
`BLIND_TRANSFER`, `GATEWAY_DOWN`, `INVALID_GATEWAY`, `SYSTEM_SHUTDOWN`, ...
Numeric Q.850 values are in `hangup_cause_q850`.

### Main channel variables

| Variable | Meaning |
|---|---|
| `uuid`, `direction`, `context`, `destination_number` | identity and routing |
| `caller_id_name`, `caller_id_number` | caller as presented |
| `effective_caller_id_name/number` | caller ID used on outgoing legs (from the directory) |
| `user_name`, `user_context` | authenticated extension and its context |
| `sip_call_id`, `sip_from_user`, `sip_to_user`, `sip_req_uri`, `sip_user_agent`, `sip_transport` | SIP details |
| `network_addr`, `network_port` | caller's address |
| `originator` | A-leg UUID on outbound legs |
| `bridge_uuid`, `last_bridge_hangup_cause`, `originate_disposition` | bridge results |
| `start_stamp`, `answer_stamp`, `end_stamp`, `duration`, `billsec`, `hangup_cause` | CDR (set at hangup) |

### CDR CSV columns (`cdr-csv`)

`uuid, direction, caller_id_name, caller_id_number, destination_number,
context, start_stamp, answer_stamp, end_stamp, duration, billsec,
hangup_cause, bridge_uuid, sip_call_id, network_addr, user_name, gateway_name`

---

## 8. Security checklist

* Bind the socket to `127.0.0.1` and use a long random password.
* Run your API server on the PBX host or reach the socket through an SSH tunnel.
* Validate every value you pass into `originate`/`uuid_*`. A dial string
  can reach any trunk.

---

## 9. Support status

| Area | Status |
|---|---|
| `auth`, `api`, `bgapi` (with `Job-UUID`), `event`, `nixevent`, `noevents`, `filter`, `myevents`, `exit` | ✅ |
| `show channels/calls/registrations as json` (stable row shape) | ✅ |
| `originate`, `uuid_kill`, `uuid_transfer`, `uuid_dump`, `uuid_send_dtmf`, `hupall`, `reloadxml`, `status`, `version` | ✅ |
| `uuid_setvar`, `uuid_setvar_multi`, `uuid_getvar`, `uuid_exists`, `uuid_answer`, `uuid_park`, `uuid_bridge`, `uuid_record`, `uuid_broadcast` | ✅ |
| `sip status` (alias `sofia status`), `sofia xmlstatus`, `sofia profile ... rescan/killgw/register/flush_inbound_reg`, `memcache`, `global_getvar`, `user_exists`, `user_data`, `sofia_contact`, `luarun app.lua event_notify`, `show_subscriptions` | ✅ |
| Channel events with all headers and variables, `CHANNEL_CALLSTATE`, `CHANNEL_DESTROY`, `sofia::register/unregister/expire`, `BACKGROUND_JOB`, `HEARTBEAT`, plain and JSON formats | ✅ |
| `sendmsg` | hangup only |
| `uuid_hold` | ❌ planned |
| `callcenter_config`, `conference list/mute/unmute/kick`, `callcenter::info` events | ✅ |
| `valet_info` | ❌ (parked calls are on the web dashboard) |
| Outbound event socket (`socket` application) | ❌ planned |
| XML event format | ❌ (use plain or json) |

---

## 10. Web interface JSON API

The web interface (`<web>` in `ringqswitch.xml`, see [WEB-UI.md](WEB-UI.md))
is a single-page application on top of a JSON API that your own software can
use too. All paths start with `/api/`.

**Authentication.** `POST /api/login` with `{"user": "admin", "password":
"..."}` (the server administrator from `ringqswitch.xml`) or `{"user":
"1001", "password": "<web password>"}` (an extension with web access;
`1001@example.com` when several domains have that number) sets an HttpOnly
session cookie (12 hours, renewed on use). Every other request needs that
cookie. Every request that changes something (POST, PUT, DELETE) also needs
the header `X-RQ: 1`. After 10 failed logins from one address, logins from it
are refused for 10 minutes. Errors are `{"error": "message"}` with a 4xx/5xx
status; `403` means the role does not allow it.

**Roles.** `admin` can do everything. An `owner` extension can do everything
in its own domain, plus the server endpoints (`logs`, `system`, `security`,
domain changes) unless `owner-server-access` is `false`. A `manager`
extension is a supervisor: it sees every call, report and agent of its domain,
can set any agent's status, monitor, hang up and transfer calls, and import
and export contacts, but cannot change settings, `roles` or other people's
voicemail. A `user` extension can read `live`, `meta`, `directory`,
`contacts`, its own `cdr`, reports and voicemail, set its own agent status,
`originate` from its own extension, and use the dialer, `chat` and
`meetings`. Web passwords are stored as PBKDF2-SHA256 hashes in
`rq_web_users`.

| Method and path | What it does |
|---|---|
| `POST /api/login`, `POST /api/logout`, `GET /api/me` | Session; `me` returns `user`, `role` (`admin`, `owner`, `manager`, `user`), `kind`, `domain`, `extension`, `server_access`, `webrtc`, `idle_logout` (minutes, 0 = off), the brand, version and whether a database is used. An extension that is a queue agent is logged in (`Available`, or the status its last sign-out took) when it signs in while `Logged Out`, and logged out 5 seconds after `logout` unless it still has another session or a registered phone |
| `POST /api/password` | Extensions: change your own web password, `{"current": "...", "new": "..."}` (at least 8 characters) |
| `GET/POST /api/domains`, `PUT/DELETE /api/domains/{uuid}` | Domains (deleting needs the domain's extensions removed first) |
| `GET /api/d/{domain}/meta` | Choices for forms: `destinations` (`{group, label, value}`; `value` is what DIDs, IVR options and timeouts store, e.g. `transfer:3000 XML example.com`; group `eFax`, for DIDs only: `efax:1000 XML example.com`, the number answers as a fax machine for extension 1000), `gateways`, `agents`, `auxcodes`, `strategies`, `prompts`, `moh`, `extensions` |
| `GET /api/d/{domain}/{resource}` | List. Resources: `extensions`, `gateways`, `dids`, `routes`, `ringgroups`, `ivrs`, `queues`, `agents`, `auxcodes`, `timeconditions`, `conferences`, `faxes` (fax extensions), `voicemails`, `aiagents` |
| `GET /api/d/{domain}/dialplans` | Read only: the domain's dialplan entries and the global ones, `[{dialplan_uuid, dialplan_name, dialplan_number, dialplan_context, dialplan_order, dialplan_enabled, dialplan_description, dialplan_xml, global}]` |
| `GET /api/d/{domain}/{resource}/{uuid}` | One item (ring groups include `destinations`, IVRs `options`, queues `agents`) |
| `POST /api/d/{domain}/{resource}` | Create (JSON body with the table's column names; see below) |
| `PUT /api/d/{domain}/{resource}/{uuid}` | Change the fields given |
| `DELETE /api/d/{domain}/{resource}/{uuid}` | Delete (with its dialplan) |
| `GET /api/live[?domain=]` | `stats`, `channels` (a leg to or from a provider carries `trunk`: its gateway or carrier ACL), `registrations`, `gateways`, `agents`, `queues`, `conferences`, `parked`, `extensions` (`{total, registered}`), `health` (server CPU, memory and disk: `cpu_pct`, `cpus`, `load1`, `mem_total_mb`, `mem_free_mb`, `mem_pct`, `disk_total_gb`, `disk_free_gb`, `disk_pct`, `cpu_history`, `mem_history`, `available`) |
| `POST /api/agents/{uuid}/status` | `{"status": "On Break", "aux": "Lunch"}` |
| `POST /api/calls/{uuid}/hangup` | Hang up a call (owner or manager) |
| `POST /api/calls/{uuid}/transfer` (or `/divert`) | `{"destination": "1002"}` (owner or manager) |
| `POST /api/calls/{uuid}/listen`, `/whisper`, `/barge` | Monitor an answered call (owner or manager): rings `{"extension": "1000"}` (default: your own) and, when answered, listens, talks to the agent only, or joins. DTMF 1/2/3 on the supervisor phone switches mode. |
| `GET /api/d/{domain}/directory` | Extensions with presence: `phone` (a phone is registered), `web` (signed in to the portal), `on_call`, `dnd`, `agent_status`, `aux` |
| `GET /api/d/{domain}/roles` | Owners: every extension with `role` (`owner`, `manager`, `user`, or `""` for no portal access), `password_set`, `last_login`, `registered` and `you` (the User roles board) |
| `PUT /api/d/{domain}/roles/{extension_uuid}` | Owners: `{"role": "manager", "password": "..."}`; the password is needed only when the extension has none yet. Your own role cannot be changed, and the person's open sessions are signed out |
| `GET /api/d/{domain}/contacts?q=&limit=&offset=` | The domain's shared address book `{rows, total, can_import}`; `q` matches name, company, email, or the digits of a number. Each row has `contact_uuid`, `name`, `company`, `phone`, `mobile`, `email`, `notes`, `can_edit` |
| `GET /api/d/{domain}/contacts/lookup?number=` | `{name, company}` of the contact with that number (matched on the last 9 digits, so `+65 6123 4567` and `61234567` are the same) |
| `POST /api/d/{domain}/contacts`, `PUT/DELETE /api/d/{domain}/contacts/{uuid}` | Add (everyone), change or delete (owners, managers and whoever added it) `{"name", "company", "phone", "mobile", "email", "notes"}` |
| `POST /api/d/{domain}/contacts/import` (body: a CSV file, at most 5 MB), `GET /api/d/{domain}/contacts/export` | Owners and managers: import returns `{added, skipped}` (numbers already in the book are skipped; columns are recognised by their header, e.g. `name` or `first name`/`last name`, `company`, `phone`, `mobile`, `email`, `notes`); export downloads every contact as CSV |
| `POST /api/dnd` | Extensions: `{"on": true}` sets do-not-disturb on your extension |
| `POST /api/originate` | Click-to-call: `{"extension": "1001", "destination": "6591234567", "domain": "example.com"}` rings the extension, then calls the number |
| `GET /api/d/{domain}/cdr?from=YYYY-MM-DD&to=YYYY-MM-DD&q=&direction=&status=&limit=&offset=` | Call history `{rows, total}` (`all=1` instead of `from`/`to`: the whole history; `limit` default 100, at most 10000). `waitsec` is how long the caller waited for a person (a queue call: from joining the queue until an agent answered or the caller gave up; other calls: until bridged or answered; a missed call: as long as it rang); `talksec` is the talk time (a queue call: from the agent's answer; else `billsec`); `answered_by` and `ended_by` (who hung up, or `the system`) when the call kept its path |
| `GET /api/d/{domain}/cdr/{uuid}/flow` | The call's path `{steps: [{t, k, x}], kept}`: `t` Unix ms, `k` the kind (`start`, `route`, `ivr`, `digits`, `queue`, `ring_group`, `ring`, `no_answer`, `answer`, `voicemail`, `conference`, `hangup`, `end`), `x` the text ("Tan Wei Ming (6591234567) called 6531590077 (trunk Main)", "Answered by Boboy Testing (1001)"...). `kept`: false when rebuilt from the call record (calls before 0.6.1, or CDRs written by the previous switch). RingQswitch keeps the path in the CDR's variables as `rq_call_flow` (JSON) |
| `GET /api/d/{domain}/cdr/{uuid}/recording` | The call's recording (WAV) |
| `GET /api/d/{domain}/transcription`, `PUT ...` | Owners: call transcription. GET answers `{settings: {enabled, scope, language, model, voicemail, live, live_model}, engine: {ok, command, model, error}, models: [{name, title, size, note, installed, path, removable, download: {done, total, error}}], default_model, model_in_use, live_model_in_use, live_default_model, live_server, live_running, live_error, can_manage_models, model_dir, counts: {queued, processing, done, empty, failed}, threads, workers, min_seconds}` (counts: the last 24 hours, plus all waiting); PUT `{"enabled": true, "scope": "external" \| "all", "language": "auto" \| "en" \| "ms"..., "model": "" \| "small"..., "voicemail": true, "live": true, "live_model": "" \| "base"...}` (`model` "": the server's default; otherwise an installed model; `voicemail`: transcribe voicemail messages; `live`: live transcription during calls, `live_model` "": the fastest installed; `live_server`: the live speech recogniser is installed, or `transcribe-live-url` is set; `live_running`: the domain's live model is loaded in the running live recogniser, started as soon as `live` is on; `live_error`: why it could not start) |
| `GET /api/d/{domain}/phonekeys/{extension}`, `PUT ...` | Owners: an extension's phone keys, `{keys: [{type, value}], company, company_in_dialer, aux}`; types `blf`, `speed_dial`, `park` (value 1-5), `agent_status` (an aux code or Available), `change_status` (`IN`, `OUT`), `blank`. Stored in RingQ's `q_blfs` (one row set per extension, types and values as JSON lists; RingQ's type names are read too). `aux` is the list numbered for `*26N` (1 is Available) |
| `POST /api/d/{domain}/phonekeys/{extension}/reboot` | Owners: check-sync with reboot to the extension's registered phones, `{phones: n}` |
| `GET /api/d/{domain}/officehours`, `PUT ...` | Owners: Language & office hours. `{language, languages, timezone, server_timezone, now, work: [7 × {mode: open \| hours \| closed, from, to}], work_action, break: [7 × {mode: none \| hours, from, to}], break_action, holiday_sound, holidays, exempt: [numbers], exempt_choices, sounds}`; index 0 is Sunday; actions are destinations (`transfer:1001 XML <domain>`, `hangup:USER_BUSY`, `""` = calls go through). PUT the same fields. Kept in `v_domain_settings`, `q_time_schedules` (RingQ's `work_schedule` day-close-open and `break_schedule` day-start-end in minutes, day 1 = Sunday), `q_holiday_exemptions` |
| `POST /api/d/{domain}/officehours/holidays`, `PUT/DELETE .../holidays/{id}` | Owners: `{name, start: "2026-12-25", end, recurring, from: "13:00", to: "18:00" (empty: all day), greeting: "prompts/x.wav"}` (`q_holidays`) |
| `GET /api/d/{domain}/dialsecurity` | Owners: `{countries: [{iso, name, continent, code}], continents, blocked: [iso], pin: {mode: "" \| prefix \| all, prefixes, trusted}, pins: [{uuid, pin, accountcode, enabled, description}], idle_logout, idle_choices}` |
| `PUT /api/d/{domain}/dialsecurity/countries` | Owners: `{"blocked": ["CU", "INMARSAT"]}` (`q_security_restrictions.blocked_countries`, ISO codes separated by commas) |
| `PUT /api/d/{domain}/dialsecurity/pin`, `POST .../pins`, `PUT/DELETE .../pins/{uuid}` | Owners: `{"mode": "prefix", "prefixes": ["00"], "trusted": ["1001"]}` (`q_security_rules`); PINs `{"pin": "4821", "accountcode": "SALES", "description": "...", "enabled": true}` (`v_pin_numbers`) |
| `PUT /api/d/{domain}/dialsecurity/autologout` | Owners: `{"minutes": 30}` (0 = off; 5, 10, 15, 30, 60, 120, 240, 480); `GET /api/me` returns it as `idle_logout` |
| `GET /api/d/{domain}/phonekeys`, `PUT ...` | Owners: the company's keys shown in every dialer (`q_company_blfs`), `{keys, show_dialer, aux}`; PUT `{"keys": [...], "show_dialer": true}` |
| `GET /api/keys` | The signed-in extension's keys for the dialer, with their lamps: `{keys: [{type, value, label, state, number, name, since, dial, group}], parks: [5 slots], aux, agent_status, agent_aux, dnd}`; `state` is `idle`, `ringing` or `busy` for extensions and parking slots, `on` for the current status; `group` is `mine` or `company`. The WebSocket sends `blf` `{resources}` when lamps change and `keys` when keys are saved |
| `GET /api/d/{domain}/aiagents`, `POST`, `GET/PUT/DELETE .../{id}` | Owners: AI agents (a resource like `ivrs`): `agent_name`, `agent_extension`, `agent_enabled`, `agent_voice` (`marin`, `cedar`, `alloy`, `ash`, `ballad`, `coral`, `echo`, `sage`, `shimmer`, `verse`), `agent_noise` (`near`, `far`), `agent_languages` (`en,ta`...), `agent_greetings` (a JSON list of strings, or one per line), `agent_prompt`, `agent_context`, `knowledge_urls` (one per line, up to 10), `knowledge_text`, `max_minutes` (1-120), `allow_end_call`, `fallback_action` (a destination), `agent_description`, and `keywords: [{keyword, destination}]`. Saving writes the number's dialplan (`ai_agent <uuid>`) and, when the web addresses changed, reads the pages in the background. `meta` lists each enabled agent as a destination (group `AI agent`) |
| `GET /api/d/{domain}/ai/settings`, `PUT ...`, `POST .../test` | Owners: the voice AI service. GET answers `{has_key, key_hint, env_key, key_lost, model, base_url, default_model, default_base_url}` (the key itself is never returned); PUT `{"api_key": "sk-...", "clear_key": false, "model": "", "base_url": ""}` (an empty `api_key` keeps the saved one); `test` opens a session (with the saved settings, or `api_key`/`model`/`base_url` given to try) and answers `{ok, model, ms}` or `{ok: false, error}` |
| `GET /api/d/{domain}/ai/knowledge/{agent}`, `POST ...` | Owners: the pages an agent read, `{pages: [{source, title, chunks, chars, fetched_at}], status: {running, pages, chunks, error, finished}}`; POST reads them again |
| `POST /api/d/{domain}/ai/preview` | Owners: `{"voice": "marin", "text": "..."}` answers `audio/wav` of that voice saying the text (24 kHz) |
| `GET /api/d/{domain}/ai/calls[?agent=&limit=&offset=]`, `GET .../{id}` | Owners and managers: the calls AI agents took, `{rows, total}` newest first (without the transcript); one call adds `transcript: [{role, text, t}]` |
| `GET /api/d/{domain}/ai/status` | Owners and managers: `{configured, model, agents: [{ai_agent_uuid, active, today, calls, transferred, failed, avg_duration, last_call}]}` (the last 30 days) |
| `GET /api/d/{domain}/ai/options` | Owners: the voices and languages offered, the default prompt and greeting |
| `POST /api/d/{domain}/transcription/models/{name}`, `DELETE ...` | Server access (the administrator, or owners with `owner-server-access`): download a speech model of the list (`tiny`, `base`, `small`, `medium`, their `.en` versions, `large-v3-turbo`, `large-v3-turbo-q5_0`, `large-v3`) into `<data-dir>/speech/` (in the background: follow `models[].download` in the GET), or delete a downloaded one |
| `GET /api/d/{domain}/transcripts/{call uuid}` | A call's transcript, for whoever may see the call (users: their own calls): `{call: {direction, caller_id_name, caller_id_number, destination_number, start_stamp, billsec, record_name, cc_queue, cc_agent...}, transcript: {call_uuid, status, direction, speaker_a, speaker_b, customer, language, duration, stereo, segments: [{speaker: "A" \| "B", start, end, text, sentiment}], speakers: [{speaker, talk_seconds, words, sentiment, label, ending, positive, neutral, negative}], sentiment, sentiment_label, customer_sentiment, customer_label, engine, error, created_at, updated_at}}`. Times in seconds from the start of the recording; sentiment from -1 to +1, label `positive` (≥ 0.05), `negative` (≤ -0.05) or `neutral`. 404 when there is none |
| `POST /api/d/{domain}/transcripts/{call uuid}` | Managers and owners: transcribe a recorded call (again): `{status: "queued"}`. A WebSocket event `{t: "transcript", status}` follows when it is done. `cdr` and `reports/queue-calls` rows carry `transcript` (the status), `sentiment` (the customer's label, else the call's) and `sentiment_score` |
| `GET /api/d/{domain}/reports/agent-status?from=&to=` | Per agent: `seconds` per status (`On Break: Lunch`...), `breaks` count per AUX code, `logged_in_seconds`, `available_seconds`, `break_seconds`, `changes`, `current` |
| `GET /api/d/{domain}/reports/agent-log?from=&to=&agent=` | Status changes |
| `GET /api/d/{domain}/reports/queues?from=&to=&sla=20` | Per queue: offered, answered, abandoned, timeout, exited, avg/max wait, avg talk, in_sla |
| `GET /api/d/{domain}/reports/agents?from=&to=` | Queue calls answered per agent, talk time |
| `GET /api/d/{domain}/reports/queue-calls?from=&to=&queue=` | Queue calls |
| `GET /api/d/{domain}/reports/hourly?from=&to=` | Queue calls per hour of the day and queue: `[{hour, queue_uuid, offered, answered, abandoned}]` |
| `...reports/{report}?all=1` | Every report: `all=1` instead of `from`/`to` for the whole history. `agent-log` and `queue-calls` with `limit` (at most 50000) and `offset` answer `{rows, total}` (without `limit`: the plain list, at most 50000 rows) |
| `GET /api/d/{domain}/voicemail-messages[?box=]`, `GET .../{uuid}/audio`, `POST .../{uuid}/read`, `DELETE .../{uuid}` | Voicemail messages: `uuid`, `box`, `created`, `caller_id_name`, `caller_id_number`, `length`, `status`, and with a transcript `transcript` (`queued`, `processing`, `done`, `empty`, `failed`), `transcript_text`, `sentiment`, `sentiment_score`. Deleting a message deletes its transcript |
| `GET /api/d/{domain}/voicemail-messages/{uuid}/transcript`, `POST ...` | A message's transcript `{transcript: {status, kind: "voicemail", speaker_a, language, duration, segments, sentiment, sentiment_label...}}`; POST transcribes it (again): `{status: "queued"}` |
| `GET /api/sounds`, `POST /api/sounds?path=prompts/name.wav` (body: a WAV or MP3 file), `DELETE /api/sounds?path=...`, `GET /api/sounds/file?path=...` | Prompts and music on hold (`music/<folder>/name.wav`); uploads are converted to 8 kHz mono |
| `GET /api/logs?kind=&q=&limit=` | The event log (kinds: `agent`, `call`, `queue`, `registration`, `gateway`, `security`, `system`) |
| `GET /api/system`, `POST /api/system/reload`, `POST /api/system/unblock` (`{"ip": "..."}`), `POST /api/system/repair` | System information (with `network`: SIP, RTP, web and browser-audio addresses and ports) and actions |
| `GET /api/sipprofiles`, `POST /api/sipprofiles`, `PUT/DELETE /api/sipprofiles/{uuid}` | SIP profiles (`v_sip_profiles`): `{"name", "description", "enabled", "sip_ip", "port", "protocol": "udp,tcp" \| "udp", "ext_sip_ip"}`. The list adds `main`, `listening`, `bind_ip`, `advertise` and `error` (why a port could not open); the main port of `ringqswitch.xml` is listed with `from_config`. Saving opens or closes the port at once |
| `POST /api/sipprofiles/defaults` | Adds RingQ's standard profiles that are missing: `internal` on the main SIP port and `external` on 5080. Answers `{"added": ["internal", "external"]}`. The list (`GET`) also says `database` and `in_database` (how many profiles the database has) |
| `POST /api/cache/clear` | Owners: the top bar's reload button. Forgets the carriers' resolved addresses and compiled number patterns and reloads the configuration from the database at once (calls continue). Answers `{"domains", "extensions", "gateways", "dialplans", "took_ms"}` |
| `GET /api/devices` | The phones and apps signed in (registered) as my extension: `[{"user", "domain", "contact", "source", "user_agent", "expires", "since"}]` (empty for the administrator account) |
| `GET /api/security` | `rules`, `types`, `blocked` (banned addresses), `acl` (trusted carriers), recent security `events`, `your_ip`, `restrict_sip` (the global IP restriction), and `guard`: `{enabled, banned_ips, jails: [{name, title, description, area (sip, web), enabled, max, window, ban (seconds), banned: [{ip, since, until, reason, source, failures}]}], ssh: {installed, enabled, max, window, ban}}` |
| `PUT /api/security/guard` | `{"enabled": true, "jails": {"web-login": {"enabled": true, "max": 10, "window": 600, "ban": 3600}}}`; protections not sent keep their settings; `enabled: false` lifts every ban. Returns `guard` |
| `POST /api/security/ban`, `POST /api/security/unban` | `{"ip": "203.0.113.7", "jail": "sip-request", "reason": "..."}` bans for that protection's ban time; unban `{"ip", "jail"}` (`jail` empty: all) |
| `GET /api/security/history?days=7` | `bans`, `bans_24h`, `unique_ips`, `by_jail`, `by_day`, `top_ips: [{ip, bans, last, jails}]`, `events: [{time, action, ip, jail, reason, actor}]` (from `security_audit_log`) |
| `PUT /api/security/restrict` | `{"on": true}`: the global IP restriction |
| `POST /api/security/rules`, `PUT/DELETE /api/security/rules/{uuid}` | `{"type": "ip-deny" \| "ip-allow" \| "web-allow" \| "web-deny" \| "ua-deny", "value": "203.0.113.0/24", "description": "..."}`. Rules that would lock out your own address are refused. |
| `POST /api/security/unblock` | `{"ip": "..."}` |
| `POST /api/security/acl`, `DELETE /api/security/acl/{uuid}` | Trusted carrier addresses (`v_access_control_nodes` of the `providers` list): `{"cidr": "...", "description": "..."}` |
| `GET /api/chat/rooms` | Your chats with unread counts and the last message |
| `POST /api/chat/rooms` | New group `{"name": "...", "members": ["1001", "1002"]}` |
| `POST /api/chat/direct` | `{"extension": "1002"}`: the one-to-one chat with that extension (made when missing) |
| `GET /api/chat/rooms/{id}/messages?before=&limit=`, `POST .../messages` (`{"body": "..."}`) | History and sending |
| `POST /api/chat/rooms/{id}/files?name=report.pdf` (body: the file, at most 20 MB), `GET /api/chat/files/{id}[?download=1]` | Files |
| `POST /api/chat/rooms/{id}/read`, `PUT /api/chat/rooms/{id}` (`{"name", "members"}`), `DELETE /api/chat/rooms/{id}` (leave) | |
| `GET /api/meetings`, `POST /api/meetings`, `PUT/DELETE /api/meetings/{code}` | Meetings: `{"title", "passcode", "scheduled_at", "duration_minutes", "lobby", "allow_guests", "description", "instant"}`. `lobby` (the waiting room) is on unless it is `false`: then only the meeting's organizer (who made it) is let in at once; everybody else waits until the organizer admits them. Codes look like `46ak-c6kg-hvcz`; the link is `/meet/m/{code}` (returned as `link`). The list holds planned meetings (not `instant` ones started from the Meet window) and who is in each meeting now |
| `GET /api/meetings/history`, `GET /api/meetings/history/{uuid}` | Finished meetings you took part in (owners and managers: the whole domain), newest first: `title`, `code`, `started`, `ended` (ms), `participants` (`name`, `guest`, `email`, `joined`), `chat_count`, `transcript_count`; one meeting adds `chat`, `transcript` (`{name, text, time}`) and `files` (names of presented files) |
| `GET /api/guest/meeting/{code}` | Without a session: the meeting `title`, `host`, whether a `passcode` is needed, `lobby` (a waiting room or a locked meeting now), `guests`, `signed_in` (a colleague's session), `organizer` (the signed-in colleague made the meeting), `link`, `in_meeting`, `brand` |
| `POST /api/guest/file?token=&name=slide.pdf` (body: the file, at most 25 MB; header `X-RQ: 1`), `GET /api/guest/file/{id}?token=` | Present file: a PDF, PNG, JPEG, GIF or WebP for the meeting. `token` is the participant's meeting token (from `meet.joined`), so guests without a session can use it; the file lives in memory while the meeting runs |
| `GET /api/d/{domain}/fax` | Owners and managers: eFax. `{counts: {sent, received, failed, active, pages_sent, pages_received, today_<status>}, numbers: [{number, label, trunk, fax}], settings: {fax_ident, fax_header, company_name, ident, header}, ghostscript}` (`ghostscript`: the server's PDF converter is installed, so PDFs can be sent): `numbers` are the DIDs a fax can be sent from (`fax`: the fax extension the DID rings), `ident`/`header` what faxes carry now |
| `GET /api/d/{domain}/fax/jobs?direction=in\|out&status=&q=&limit=` | The faxes, newest first: `[{id, direction, from_number, to_number, pages, status, error_reason, call_uuid, created_at, updated_at, has_file}]` (times in epoch seconds; `status=active` = queued, dialing, sending or receiving) |
| `POST /api/d/{domain}/fax/send?from=&to=&name=` | Body: the document (PDF, TIFF, PNG, JPEG or GIF, 20 MB). `from` must be one of `numbers`; `to` may list up to 20 numbers (commas). Answers `{jobs: [id...], pages}`; 400 when the document cannot be faxed (a PDF when the server has no PDF converter...) |
| `GET /api/d/{domain}/fax/jobs/{id}/file[?download=1]`, `POST .../jobs/{id}/retry`, `DELETE .../jobs/{id}` | The fax received (PDF) or the document sent; send a failed fax again; delete a fax and its file (not while in progress) |
| `GET /api/d/{domain}/fax/jobs/{id}/trace?side=heard\|sent` | The audio of a fax call (WAV, `fax-<call_uuid>-<side>.wav`), to find out why a fax failed: `heard` (default) is what came from the other machine, `sent` what RingQswitch sent |
| `PUT /api/d/{domain}/fax/settings` | `{"fax_ident": "+65 6123 4567", "fax_header": "ACME PTE LTD"}` (empty = the company name) |
| `GET /api/d/{domain}/hotel` | Owners and managers: the hotel front desk. `{rooms, occupied, vacant}`; each room is an enabled extension: `{extension, name, registered, status: "vacant" \| "occupied", guest: {stay_uuid, first_name, last_name, checked_in_at}, wakeup: {wakeup_uuid, call_at, status, attempts, last_result, sound}}` (`wakeup` is the planned call, else the latest of the last 18 hours) |
| `POST /api/d/{domain}/hotel/checkin` | `{"extension": "1004", "first_name": "Sarah", "last_name": "Bennett"}` answers `{stay_uuid}`; 400 when the room is occupied |
| `PUT /api/d/{domain}/hotel/stays/{stay_uuid}`, `POST .../stays/{stay_uuid}/checkout` | Change the guest's name; check out (cancels the room's planned wake-up call) |
| `POST /api/d/{domain}/hotel/wakeups`, `DELETE .../wakeups/{wakeup_uuid}` | Schedule `{"extension": "1004", "at": "2026-10-10T07:00:00+08:00", "sound": "prompts/wakeup.wav"}` (`at`: RFC 3339, or `YYYY-MM-DDTHH:MM` in the server's time zone; `sound` empty = beeps) for a room with a guest; it replaces the room's planned call. At the time the room rings (caller ID "Wake-up call") for 45 s; unanswered, it is tried again 5 minutes later, 3 tries. `status`: `scheduled`, `calling`, `answered`, `no_answer`, `cancelled` |
| `GET /api/sso/providers` | Without a session: the single sign-on buttons of the sign-in page `[{id, name, need_domain}]` (`need_domain`: several domains use the provider and none is the browser's host) |
| `GET /api/sso/start/{microsoft\|google\|linkedin}[?domain=]`, `GET /api/sso/callback/{provider}` | The browser's sign-in with a provider (OpenID Connect code flow with state, nonce and, where the provider supports it, PKCE; the ID token's RS256 signature, issuer, audience, expiry and nonce are checked). Success sets the session and opens the portal; a failure goes back to `/#sso-error=<reason>` |
| `GET /api/d/{domain}/sso` | Owners: `{providers: [{id, name, configured, enabled, client_id, tenant_id, secret_set, needs_tenant, redirect_uri}], web_password}` (the secret is never sent back) |
| `PUT /api/d/{domain}/sso/{provider}`, `DELETE ...` | Owners: `{"client_id", "client_secret" (empty = keep), "tenant_id" (for providers with tenants), "enabled"}`; turning on checks the provider's OpenID configuration (a wrong tenant is refused). Deleting the last provider turns web passwords back on |
| `PUT /api/d/{domain}/sso/options` | Owners: `{"web_password": false}` makes the domain's extensions sign in with a provider only (needs one turned on); `login` then answers 400 for them. The `admin` account is not affected |
| `GET /api/ws` | WebSocket (same origin, session cookie); see below |

**Field names** are the database column names (`extension`, `password`,
`gateway`, `proxy`, `queue_strategy`, `agent_call_timeout`...), plus a few
friendly fields the interface translates:

* DIDs: `action` (where calls go, a `meta` destination value; empty = not linked yet: the number is kept
  but calls to it are not answered, and `PUT` with only `{"action": ...}` links it, keeping the trunk) and
  `gateway_uuid` (the gateway the number arrives on, empty = any); `destination_prefix` is the country's calling code
  (`65`): the number then also matches without it (`6531591234` and `31591234`)
* Agents: `extension` (stored as `agent_contact = user/<ext>@<domain>`)
* Queues: `record` (true/false) and `agents` (`[{extension, tier_level, tier_position}]`; an
  extension that is not an agent yet becomes one, logged out, with default settings.
  `call_center_agent_uuid` is still accepted instead of `extension`)
* Ring groups: `timeout_action` and `destinations` (`[{destination_number, destination_delay, destination_timeout}]`)
* IVR menus: `exit_action` and `options` (`[{ivr_menu_option_digits, action, ivr_menu_option_description}]`)
* Outbound rules (`routes`): `route_name` (at least 4 characters), `route_from_extensions` (`1001,1002`, empty = all),
  `route_from_ranges` (`100-199,2000-2099`), `route_prefix`, `route_length` (`8`, `8,10`, `7-15`; with the prefix they
  build `route_pattern`, which may also be given alone), `route_block` (true = refuse), `gateway_uuids` (the route and the
  backup), `route_strip`, `route_prepend`, `caller_id_number`, `sip_from_user` (`cid`, `extension`, or empty = the
  gateway account), `sip_from_display` (`cid_name`, `cid_number`, `extension`, or empty), `sip_from_host` (`domain`, or
  empty = the gateway), `cid_type` (`pid`, `rpid`, `none`, or empty = the gateway's default)
* Extensions: `voicemail_enabled`, `voicemail_password`, `voicemail_mail_to`;
  `web_role` (`""` = no web access, `user`, `manager`, `owner`), `web_password` (empty = keep) and `sso_email` (the
  single sign-on account of the extension, unique in the domain). Reading an
  extension gives its `web_password` too (owners only): RingQswitch keeps an encrypted copy, with a key that stays on
  the server (`data-dir/web-passwords.key`); passwords set before 0.5.4 appear after the user's next sign-in;
  `agent_enabled` and `agent_call_timeout`, `agent_wrap_up_time`, `agent_max_no_answer`,
  `agent_no_answer_delay_time` (the extension's call-centre agent)
* Conferences: `record`

Example with curl:

```bash
B=https://pbx.example.com:8443/api
curl -sk -c jar -H 'Content-Type: application/json' -d '{"user":"admin","password":"..."}' $B/login
curl -sk -b jar -H 'X-RQ: 1' -H 'Content-Type: application/json' \
  -d '{"destination_number":"6531591234","gateway_uuid":"<gateway uuid>","action":"transfer:3000 XML example.com"}' \
  $B/d/example.com/dids
curl -sk -b jar -H 'X-RQ: 1' -H 'Content-Type: application/json' -d '{"status":"On Break","aux":"Lunch"}' \
  $B/agents/<agent uuid>/status
```

### WebSocket `/api/ws`

One connection per browser tab carries live updates, chat, the dialer and
meetings. Every message is a JSON object with a type `t`; a request may
carry an `id`, which the reply repeats.

| Client sends | Server sends |
|---|---|
| `{"t": "live", "on": true, "domain": "..."}` | `{"t": "live", "data": {...}}` (the `GET /api/live` object) at once on every call, registration or agent change (at most 4 per second), and every 2 seconds |
| `{"t": "ping"}` | `{"t": "pong", "time": ...}` |
| (any request that fails) | `{"t": "error", "error": "..."}`, with the request's `id` |
| (nothing) | `{"t": "blf", "resources": [...]}`: lamps of phone keys changed (reload `GET /api/keys`); `{"t": "keys"}`: the extension's keys were edited |
| (nothing) | `{"t": "transcript", "status": "queued" \| "processing" \| "done" \| "empty" \| "failed"}` to the signed-in users of the call's domain whenever a call transcript changes status (reload the report to see it) |
| (nothing) | Live transcription (domains with `live` on), to the pages of the extensions on an answered call: `live.start` `{id, me: "a" \| "b", sides: {a: {name}, b: {name}}, started}` (side `a`: the caller's leg, `b`: the leg that answered; `me`: which one is this extension; `started`: Unix ms), then `live.speak` `{id, side, seq, on, at}` when a side starts speaking (`on: false` when nothing was recognised), `live.partial` `{id, side, seq, at, text}` (what that utterance said so far, about every second while the side speaks, when the recogniser is free; replaced by its `live.text`), `live.text` `{id, side, seq, at, text}` (`at`: ms from the start; `seq` numbers each side's utterances, matching its `live.speak`), and `live.end` `{id}`. A page that connects during the call gets `live.start` and the `live.text` so far |
| `chat.send` `{room, body}`, `chat.typing` `{room}`, `chat.read` `{room}` | `chat.sent` `{message}` (the reply to `chat.send`), `chat.msg`, `chat.typing`, `chat.rooms`, and `presence` when someone's status changes |
| `phone.start` `{sdp, gen}` and `phone.ice`, then `phone.dial` `{number}`, `phone.answer`, `phone.hangup`, `phone.hold` `{call, on}`, `phone.mute`, `phone.dtmf` `{call, digits}`, `phone.transfer` `{call, target}`, `phone.attxfer` `{call, target}`, `phone.stats` (the audio check of the call being heard), `phone.stop` | `phone.dialing` `{call}` (the reply to `phone.dial`), `phone.sdp`, `phone.ice` and `phone.media` (carrying the `gen` of the `phone.start` they answer, so a browser that restarted ignores old ones), `phone.reg` (registered or not), `phone.call` (each call's state), `phone.mwi`, and the `phone.stats` reply: `via`, `connection`, `call`, `audio` (`from_browser`, `to_call`, `from_call`, `to_browser`, each `{packets, level, recent, loudest}` in dBFS since the answer; `seconds`; `problems`, plain-English), `rtp` (`local`, `remote`, `codec` of the call towards the switch) |
| `meet.join` `{room}`, `meet.answer`, `meet.ice`, `meet.state` (mic, camera, screen, hand), `meet.chat` `{body}`, `meet.reaction` `{body}`, `meet.transcribe` `{on}`, `meet.caption` `{body, on}` (a line of your own speech; `on` = final), `meet.present` `{data: {file, page} \| {page} \| {stop}}`, `meet.board` `{on}`, `meet.draw` `{data: {c, w, p: [x, y, ...]}}` (0-10000), `meet.clear`, `meet.leave`; hosts: `meet.admit`, `meet.deny`, `meet.mute`, `meet.muteall`, `meet.remove` `{peer}`, `meet.lock` `{on}`, `meet.lobby` `{on}` (the waiting room), `meet.end` | `meet.joined` (`me`, `host`, `title`, `code`, `link`, `started`, `now`, `token`, `waiting`), `meet.waiting`, `meet.admitted` (with the chat, transcript, presentation and whiteboard so far), `meet.lobby`, `meet.offer`, `meet.ice`, `meet.peers` (`peers`, `locked`, `lobby`), `meet.media`, `meet.chat`, `meet.reaction`, `meet.transcribe`, `meet.caption` `{line, final}`, `meet.present`, `meet.board`, `meet.draw`, `meet.clear`, `meet.mute`, `meet.ended` |

The dialer is a real SIP client inside RingQswitch: `phone.start` registers
the signed-in extension (User-Agent "RingQswitch Web Dialer"), and the browser
exchanges audio with it over WebRTC on UDP `webrtc-port` (default 8443), or,
where UDP is blocked, over TCP on the web port itself (ICE-TCP, RFC 6544:
the web port tells the two apart by the first byte). `phone.reg` and
`phone.call` carry the server's clock (`now`, Unix milliseconds) so call
timers do not depend on the browser's clock.
Guests join meetings through `/api/guest/ws?code=...&name=...&passcode=...`,
without a session.

---

## 11. Network: listening sockets and outbound connections

Every socket RingQswitch opens, from the code. Open in the firewall only the
rows marked **public**; the others stay on the server.

### Listening

| Socket | Protocol | Address and port (setting) | Default | Who connects |
|---|---|---|---|---|
| SIP | UDP and TCP | `sip-ip`:`sip-port` (`enable-tcp` for TCP) | `auto`:5060 | **Public**: phones, softphones, carriers |
| SIP profiles | UDP and TCP (`protocol`) | each enabled profile's `sip_ip`:`port` (`v_sip_profiles`, e.g. `external` on 5080) | none | **Public**: carriers on their own port |
| RTP and RTCP | UDP | `rtp-ip`, an even port and the next one for each call leg, between `rtp-start-port` and `rtp-end-port` (T.38 fax uses the call's RTP port) | 16384-32768 | **Public**: the voice of phones and carriers |
| Web portal and JSON API | TCP (HTTPS; HTTP with `tls` = `off`) | `<web>` `listen` | off until set (installer: `0.0.0.0:8443`) | **Public** or behind a reverse proxy: browsers, your software |
| Browser audio over TCP | TCP, on the web port itself | the web port tells HTTPS and ICE-TCP apart by the first byte; only when `listen` is not a loopback address | with the web port | **Public**: dialer and meetings where UDP is blocked |
| Browser audio (dialer and meetings) | UDP (IPv4) | all addresses, `<web>` `webrtc-port` (0 = off) | 8443 | **Public**: browsers |
| Event socket | TCP | `event-socket-listen` (empty = off) | `127.0.0.1:8021` | Local: `rq_cli`, the RingQ portal, your API server |
| Live speech recogniser | TCP (HTTP) | `127.0.0.1`, a free port chosen at start; started only while a domain has live transcription on (or use `transcribe-live-url` for one elsewhere) | on demand | Local: RingQswitch itself |
| Browser dialer's SIP client | UDP | `sip-ip` (or 127.0.0.1), free ports: one for SIP and one per call for RTP, towards the switch's own SIP port | per signed-in dialer | Local: RingQswitch itself |

`GET /api/system` returns `network` with the SIP, RTP, web and browser-audio
addresses and ports in use, and `GET /api/sipprofiles` says per profile
whether it is `listening` and, if not, why (`error`).

### Outbound

| To | Protocol | When |
|---|---|---|
| Carriers (SIP trunks) | SIP over UDP or TCP, RTP over UDP | Calls, and trunk registrations |
| DNS resolvers of the server | UDP/TCP 53 | Carrier and provider names (SRV and A records) |
| PostgreSQL | TCP (`dsn`, usually `127.0.0.1:5432`) | Always, when `<database>` is set |
| The voice AI service (the domain's `base_url`) | HTTPS and secure WebSocket (443) | While an AI agent is on a call, settings tests and voice previews |
| The pages given to an AI agent | HTTPS | When the agent's knowledge pages are (re)read |
| Single sign-on providers | HTTPS (443) | Provider discovery, and each sign-in's token exchange |
| The speech model library (`transcribe-model-url`) | HTTPS | Only when a model is downloaded from Settings → Call transcription |

Nothing else leaves the server: call audio for transcription is recognised on
the server itself.
