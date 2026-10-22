# RingQswitch: install, test and troubleshooting guide

This guide takes you from a fresh 64-bit Linux server to two phones calling
each other through RingQswitch, and then to RingQswitch running under your
RingQ portal database. It also explains how to diagnose problems. Commands
are given for apt-based systems; dnf-based equivalents are shown where they
differ.

---

## 0. What you received

| File | Contents |
|------|----------|
| `ringqswitch-0.10.1-src.tar.gz` | Full source code, sample configuration, tests, docs, this guide |
| `ringqswitch-0.10.1-linux-bin.tar.gz` | Prebuilt static binaries for `amd64` and `arm64` + config + installer (no Go needed) |
| `SHA256SUMS` | Checksums of both archives |

Source layout:

```
cmd/ringqswitch/     the switch daemon
cmd/rq_cli/          the command line console
internal/            sip, sdp, media, dialplan, config, core, fax, db, esl, web, webphone, transcribe
conf/                sample configuration (ringqswitch.xml, directory/, dialplan/, examples/)
packaging/           installer, systemd unit, database, certificate and transcription helpers
test/interop/        interoperability and load tests
docs/                guides and references
vendor/              every Go dependency (offline builds)
```

---

## 1. Requirements

| Item | Minimum | Notes |
|------|---------|-------|
| OS | Any 64-bit Linux with systemd | x86-64 or ARM64 |
| CPU / RAM | 1 vCPU / 512 MB | 2 vCPU / 2 GB for 100+ concurrent calls; 4 vCPU / 4 GB with call transcription |
| Go | 1.24 or newer | only to build from source |
| Database | PostgreSQL with the RingQ database | for the portal and its configuration pages (section 14); the XML mode of sections 4-9 runs without it |
| Network | A public IP (or port forwarding) | see section 5 |

RingQswitch is a single static binary: it needs no other libraries.

---

## 2. Upload and extract

From your workstation:

```bash
# Verify the downloads (optional)
sha256sum -c SHA256SUMS

# Copy to the server (replace user/IP/key)
scp -i mykey.pem ringqswitch-0.10.1-src.tar.gz admin@SERVER_IP:~
```

On the server, extract into your home directory or `/opt`. **Never extract
into `/etc/ringqswitch`**: that is where the installer writes the live
configuration.

```bash
cd /opt                                   # or: cd ~
tar xzf ~/ringqswitch-0.10.1-src.tar.gz
cd ringqswitch-0.10.1
```

> Upload the `.tar.gz` file itself and extract it **on the server** with `tar`.
> Extracting on a desktop computer and uploading the folder with a file
> transfer program strips the Linux execute permissions. If that happened,
> run scripts as `bash script.sh`, or restore the permissions with
> `chmod +x packaging/*.sh test/interop/run.sh`.

---

## 3. Build

### Option A: build from source (recommended)

Install Go 1.24 or newer from the official Go downloads (the Go package of
many distributions is older than 1.24):

```bash
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
curl -LO https://go.dev/dl/go1.24.7.linux-$ARCH.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.24.7.linux-$ARCH.tar.gz
echo 'export PATH=/usr/local/go/bin:$PATH' >> ~/.bashrc && source ~/.bashrc
sudo apt install -y make                  # dnf: sudo dnf install -y make
go version        # must print go1.24 or newer
```

Build:

```bash
make build                 # -> bin/ringqswitch and bin/rq_cli
# without make:
go build -o bin/ringqswitch ./cmd/ringqswitch
go build -o bin/rq_cli ./cmd/rq_cli
```

No internet access is needed: every dependency is in `vendor/`.

Run the test suite (a few minutes). Every package must print `ok`:

```bash
make test                  # or: go test ./...
make race                  # optional: same tests with the race detector
```

### Option B: prebuilt binaries

```bash
tar xzf ringqswitch-0.10.1-linux-bin.tar.gz
cd ringqswitch-0.10.1-linux
ls bin/linux-amd64 bin/linux-arm64      # pick yours; install.sh does this automatically
```

The binaries are statically linked and need no libraries.

---

## 4. First run (foreground, from the source tree)

Run in the foreground first. You will see errors directly in the terminal.

### 4.1 Edit the configuration

Open `conf/ringqswitch.xml`:

```xml
<var name="domain" value="$${local_ip_v4}"/>          <!-- optional: your public IP or DNS name -->
<var name="default_password" value="ChangeMe-1234"/>  <!-- CHANGE: password of extensions 1000-1003 -->
...
<param name="ext-sip-ip" value=""/>                   <!-- SET to your PUBLIC IP behind NAT -->
<param name="ext-rtp-ip" value=""/>                   <!-- SET to your PUBLIC IP behind NAT -->
...
<param name="event-socket-password" value="ChangeMe-ESL"/>   <!-- CHANGE -->
```

Find your public IP:

```bash
curl -s ifconfig.me
ip -4 addr show                            # the addresses on the network card
```

> **Why ext-sip-ip / ext-rtp-ip matter:** most cloud servers only see a
> **private** address on their network card and reach the internet through
> NAT. Without these settings RingQswitch tells phones to send audio to the
> private address, so calls connect with **no audio**. If `ip -4 addr show`
> already lists the public address, leave both empty.

### 4.2 Validate and start

```bash
bin/ringqswitch -conf conf -check
# configuration OK: 1 domain(s), 4 users, 2 dialplan contexts, 0 gateways, 0 ring groups, 0 IVR menus
bin/ringqswitch -conf conf
# level=INFO msg="RingQswitch started" version=0.10.1 sip=10.0.1.23:5060 tcp=true ...
# level=INFO msg="event socket listening" addr=127.0.0.1:8021
```

In a second terminal:

```bash
bin/rq_cli -conf conf -x status         # reads event-socket-password from conf/ringqswitch.xml
```

Stop the server with `Ctrl+C`. To get more detail, add `-log-level debug`.

---

## 5. Open the network

### 5.1 Cloud firewall (security group / network firewall)

In your cloud provider's console, allow **inbound**:

| Protocol | Port(s) | Purpose |
|----------|---------|---------|
| UDP, TCP | 5060 | SIP (phones and carriers) |
| UDP | 16384-32768 | RTP audio |
| UDP, TCP | 5080 | SIP for carriers, when the database has an enabled `external` SIP profile (Settings → Global → SIP Profile) |
| TCP | 8443 | Web portal (HTTPS) and browser audio over TCP, when the web portal is on; ideally from your office IPs only |
| UDP | 8443 | Browser dialer and meetings audio (`webrtc-port`), when the web portal is on |
| TCP | 22 | SSH (you have it already) |

**Never** open TCP 8021 (event socket) to the internet. Every socket
RingQswitch opens is listed in [docs/API.md](docs/API.md), section 11.

If you can, restrict the source to your office/home IP while testing. SIP
scanners find a new server on port 5060 within minutes; the Security
settings (SIP Guard) block them once the portal is running.

### 5.2 Server firewall (if enabled)

```bash
# ufw
sudo ufw allow 5060/udp && sudo ufw allow 5060/tcp && sudo ufw allow 16384:32768/udp
sudo ufw allow 8443/tcp && sudo ufw allow 8443/udp          # web portal, dialer and meetings

# firewall-cmd
sudo firewall-cmd --permanent --add-port=5060/udp --add-port=5060/tcp --add-port=16384-32768/udp \
  --add-port=8443/tcp --add-port=8443/udp
sudo firewall-cmd --reload

# plain iptables rules (some cloud images ship them)
sudo iptables -I INPUT -p udp --dport 5060 -j ACCEPT
sudo iptables -I INPUT -p tcp --dport 5060 -j ACCEPT
sudo iptables -I INPUT -p udp --dport 16384:32768 -j ACCEPT
```

### 5.3 Check that it is listening

```bash
sudo ss -lunp | grep ringqswitch     # UDP: SIP, RTP of calls, browser audio
sudo ss -ltnp | grep ringqswitch     # TCP: SIP, event socket, web portal
```

---

## 6. Connect phones

Use any SIP softphone or desk phone.

| Setting | Phone 1 | Phone 2 |
|---------|---------|---------|
| Username / Auth ID / User | `1000` | `1001` |
| Password | your `default_password` | same |
| Domain / Server / Registrar | `SERVER_PUBLIC_IP` | same |
| Port | 5060 | 5060 |
| Transport | **UDP** (or TCP) | |
| Encryption / SRTP / TLS | **off** | |
| Codecs | enable **G.711 µ-law (PCMU)** and **A-law (PCMA)** | |
| DTMF mode | **RFC 2833 / RFC 4733** (or SIP INFO) | |
| STUN / ICE | not needed (leave off) | |

Check the registration on the server:

```bash
sudo rq_cli -x "show registrations"
```

---

## 7. Test checklist

| # | Dial (from 1000) | Expected |
|---|------------------|----------|
| 1 | `9196` | short beep, then you hear yourself (echo). Audio works both ways |
| 2 | `9197` | continuous 1004 Hz tone |
| 3 | `1001` | phone 1001 rings. Answer it and talk in both directions |
| 4 | `1001`, decline on 1001 | busy tone on 1000 |
| 5 | `1002` (not registered) | busy tone, then the call ends |
| 6 | `5000` | two short tones (IVR). Press `1` → echo, `2` → tone, `3` → rings 1000 (try that one from 1001) |
| 7 | `9198` | beep, record up to 20 s (press `#` to stop), then it plays back |
| 8 | `2000` | 1000 and 1001 ring together (ring group) |
| 9 | during a 1000↔1001 call, 1001 presses **Transfer** to `1002` | 1000 is connected to 1002 (blind or attended) |

While calls are up:

```bash
sudo rq_cli                 # the live log, then commands:
ringqswitch@host> show channels           # two channels per bridged call, codec, state
ringqswitch@host> show calls
ringqswitch@host> /log info               # less detail; /log debug for the dialplan trace; /nolog
ringqswitch@host> /events plain ALL       # live event stream; /noevents to stop
ringqswitch@host> uuid_kill <uuid>        # hang up a call
ringqswitch@host> originate user/1000 9197   # server calls 1000 and connects it to the tone
```

Call records are in `log/cdr.csv` (source-tree run) or
`/var/lib/ringqswitch/log/cdr.csv` (service install).

---

## 8. Install as a service (systemd)

From the source tree (it builds first):

```bash
sudo make install                 # same as: make build && sudo bash packaging/install.sh
```

From the extracted binary package:

```bash
sudo bash packaging/install.sh
```

It installs:

| Path | What |
|------|------|
| `/usr/local/bin/ringqswitch`, `/usr/local/bin/rq_cli` | binaries |
| `/usr/local/share/ringqswitch/install-transcription.sh` | the call transcription installer |
| `/etc/ringqswitch/` | configuration (an existing one is kept) |
| `/var/lib/ringqswitch/log/cdr.csv` | call records |
| `/var/lib/ringqswitch/recordings/`, `sounds/` | audio files |
| `/etc/systemd/system/ringqswitch.service` | service unit (runs as user `ringq`) |

Then:

```bash
sudo nano /etc/ringqswitch/ringqswitch.xml       # passwords + ext-sip-ip/ext-rtp-ip (section 4.1)
sudo ringqswitch -conf /etc/ringqswitch -check
sudo systemctl enable --now ringqswitch
sudo systemctl status ringqswitch
sudo journalctl -u ringqswitch -f                 # live logs
```

| Task | Command |
|------|---------|
| Reload users / dialplan without dropping calls | `sudo systemctl reload ringqswitch` or `rq_cli -x reloadxml` |
| Restart (drops calls; needed for port/IP/log-level changes) | `sudo systemctl restart ringqswitch` |
| Stop | `sudo systemctl stop ringqswitch` |

---

## 9. Everyday administration (XML mode)

With the database (section 14) all of this is done in the web portal
instead.

### Add an extension

Add to `/etc/ringqswitch/directory/default.xml` (or a new `*.xml` file in that folder):

```xml
<user id="1004">
  <params><param name="password" value="A-Strong-Password"/></params>
  <variables>
    <variable name="user_context" value="default"/>
    <variable name="effective_caller_id_name" value="Reception"/>
    <variable name="effective_caller_id_number" value="1004"/>
  </variables>
</user>
```

Then `rq_cli -x reloadxml`. The sample dialplan already routes
`1000-1099`.

### Change the dialplan

Edit `/etc/ringqswitch/dialplan/default.xml`, then `reloadxml`. The sample
file is commented and includes ring groups, hunt groups, an IVR and an
office-hours example; the dialplan model is described in
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

### Connect a SIP trunk (PSTN)

In `ringqswitch.xml` uncomment and fill a `<gateway>`, uncomment the
`outbound_pstn` extension in `dialplan/default.xml`, and **restart** (gateways
are read at startup). Check its state with `rq_cli -x "show gateways"`
(`REGED` = registered). Inbound calls from the trunk's IP land in the
`public` context. Route your DIDs in `dialplan/public.xml`. If the carrier
sends calls from IPs other than its registrar, add them as
`<acl><node cidr="..." context="public"/></acl>`.

### Call transcription (optional)

Transcripts of calls (Speaker A / Speaker B, with sentiment) need the
database and the web portal. Install the speech recogniser once, then
turn it on in the portal (Settings → Call transcription):

```bash
sudo bash packaging/install-transcription.sh        # the speech recogniser + a speech model, about 5 minutes
```

It is optional: without it RingQswitch works as before. Everything runs on
the server; no call audio leaves it. Details:
[docs/WEB-UI.md](docs/WEB-UI.md), section 12D.

### Useful rq_cli commands

`sudo rq_cli` opens the console with the live log (no password to type: it
is read from `/etc/ringqswitch/ringqswitch.xml`). `rq_cli -x "status"` runs
one command; `rq_cli -l info` shows less detail; `rq_cli -q` shows no log.
In the console: `/log debug|info|notice|warning`, `/nolog`, `/events`,
`/help`, `/exit`.

```
status                         uptime, sessions, registrations, ports in use
show registrations | channels | calls | gateways | applications   (add "as json")
uuid_kill <uuid> [CAUSE]       hang up
uuid_transfer <uuid> <ext> [context]
uuid_dump <uuid>               all channel variables
originate <dialstring> <ext>|&app(args)
hupall                         hang up everything
reloadxml                      reload directory, dialplan, ACLs
blocked / unblock <ip>         brute-force block list
help
```

The full command and event reference is [docs/API.md](docs/API.md).

---

## 10. Automated interoperability test (optional)

This drives the real daemon with an independent SIP traffic generator
(the `sip-tester` package) over UDP and TCP:

```bash
sudo apt install -y sip-tester
make interop                            # or: test/interop/run.sh; expect: 8 passed, 0 failed
CALLS=500 test/interop/run.sh           # adds a 500-call load test through the bridge
```

It uses ports 5070, 8031 and 36000-38000 on `127.0.0.1`, so it does not
disturb a running instance on 5060.

---

## 11. Troubleshooting

### 11.1 Turn on diagnostics first

```bash
# Detailed logs (foreground)
bin/ringqswitch -conf conf -log-level debug

# Full SIP message trace: set in ringqswitch.xml, then restart
<param name="log-level" value="debug"/>
<param name="sip-trace" value="true"/>

# Watch SIP on the wire
sudo tcpdump -ni any -s0 port 5060 -A

# Is RingQswitch receiving SIP at all? (counters: received / sent / not SIP / invalid / blocked / send errors)
rq_cli -x status | grep "sip messages"

# Is audio arriving?
sudo tcpdump -ni any udp portrange 16384-32768 -c 50

# Service logs
sudo journalctl -u ringqswitch --since "10 min ago"
```

The web portal shows the same information without a terminal: Logs, the
Call Status page and the Security centre.

### 11.2 Symptoms and fixes

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `configuration error: ...` at start | XML typo or invalid value. The message names the file | Fix it, then run `ringqswitch -conf DIR -check` until OK |
| `sip: udp listen: address already in use` | Another SIP server on 5060 (the previous switch, or a second RingQswitch) | `sudo ss -lunp \| grep 5060`. Stop it, or set `sip-port` to e.g. 5080 |
| `event socket: ... 8021: address already in use` | The previous switch is still running (it uses 8021 too) | Stop it, or change `event-socket-listen` to `127.0.0.1:8022` |
| `media: no free rtp ports` / 503 "No Media Ports" | RTP range too small or used by another app | Widen `rtp-start-port`/`rtp-end-port` (each call leg uses 2 ports) |
| Phone says **Request Timeout** / never registers | Cloud or server firewall blocks 5060, wrong IP, or your IP is **blocked** | Check section 5. `sudo tcpdump -ni any port 5060` to see if packets arrive. `rq_cli -x blocked`, then `rq_cli -x "unblock <ip>"`; with the portal also check Security → SIP Guard |
| `tcpdump` shows REGISTERs arriving but RingQswitch logs nothing and never replies | A **firewall on the server** drops them after tcpdump has seen them | Check `ufw status`, `nft list ruleset`, `iptables -L INPUT -n -v`. Allow UDP/TCP 5060 and UDP 16384-32768 (section 5.2). Confirm with `rq_cli -x status`: the "sip messages: N received" counter stays at 0 while the firewall blocks |
| **403 Forbidden** on register | Wrong username/password, or registering an extension with another's credentials | Log shows `authentication failed`. 10 failures in 60 s block the IP for 10 min (`auth-fail-threshold`) |
| Register OK but calls get **404** | No dialplan match | Log shows `no route context=... destination=...`. Check `user_context` and the regex |
| Calls get **480 Temporarily Unavailable** | Callee not registered (`USER_NOT_REGISTERED`) | `show registrations`. Re-register the callee |
| **No audio** / **one-way audio** | `ext-rtp-ip` not set behind NAT, RTP ports blocked, or **SIP ALG** on the office router | Set `ext-sip-ip`/`ext-rtp-ip` to the public IP and restart. Open UDP 16384-32768. Disable "SIP ALG" on the router. Check RTP with tcpdump |
| Call drops after **~32 seconds** | Our 200 OK's ACK never arrives (NAT/firewall/ALG mangling) | Log shows `no ACK for 200 OK`. Same fixes as above. Try TCP transport on the phone |
| Call drops after **2 minutes** of silence | `rtp-timeout` (no RTP received; the phone may stop sending on mute) | Raise `rtp-timeout` or set it to `0` |
| **488 Not Acceptable Here** on echo/IVR/tones | Phone offers no G.711, or forces SRTP | Enable PCMU/PCMA. Disable SRTP/encryption |
| IVR does not react to keys | Phone sends DTMF in the audio | Set DTMF to RFC 2833/4733 or SIP INFO |
| Trunk `show gateways` → `FAIL_WAIT` | Wrong credentials/realm, or the trunk is unreachable | `last_error` column. Check `username`, `password`, `realm`, `from-domain` with the carrier |
| Outbound trunk calls rejected 403 | Carrier requires a specific From user / caller ID | Set `from-user`, `from-domain` or `caller-id-in-from` on the gateway |
| Outbound calls refused with a short announcement | The country is blocked or a PIN is required (Settings → Security) | Allow the country, or dial the PIN when asked |
| Outside callers hear "closed" during the day | Office hours or a holiday in the company's time zone | Settings → Language & office hours: check the time zone and the schedule |
| `rq_cli: authentication failed` or `no event socket password` | Not run as root (it reads the password from `/etc/ringqswitch/ringqswitch.xml`), or a wrong `-p` | `sudo rq_cli`, or `rq_cli -p <event-socket-password>` |
| `sudo: packaging/install.sh: command not found` | You are not in the source/package directory, **or** the execute bit was lost (files uploaded from a desktop computer) | `cd` into the extracted directory and run `sudo bash packaging/install.sh` (or `sudo make install`) |
| `make: *** No rule to make target 'build'` | You are not in the source directory | `cd ringqswitch-0.10.1` (the directory containing `Makefile`) |
| installer: `the source tree is inside /etc/ringqswitch` | The source was extracted where the config belongs | `mv /etc/ringqswitch /opt/ringqswitch-src && cd /opt/ringqswitch-src`, then install again |
| `rq_cli: dial tcp 127.0.0.1:8021: connect: connection refused` | Server not running, or event socket disabled/moved | `systemctl status ringqswitch`. Check `event-socket-listen` |
| Service will not start under systemd | Config unreadable by `ringq`, or a relative path points to an unwritable dir | `journalctl -u ringqswitch`. Re-run `sudo bash packaging/install.sh`. Use absolute paths |
| Warning `default passwords are in use` | Sample passwords unchanged | Change `default_password` and `event-socket-password` |

Web portal problems (sign-in, certificate, dialer audio) are covered in
[docs/WEB-UI.md](docs/WEB-UI.md), section 19.

### 11.3 Build and test problems

| Problem | Fix |
|---------|-----|
| `go: command not found` | Add Go to PATH: `export PATH=$PATH:/usr/local/go/bin` |
| `go.mod requires go >= 1.24` | Your Go is older; install a newer one (section 3) |
| A test fails with `address already in use` | Tests use loopback ports 30000-30300 and 41000-61000. Stop whatever uses them, or run on an idle machine |
| Investigate one failing test | `go test ./internal/core -run TestBasicCallWithMediaRelay -v`. Add `RQ_TEST_DEBUG=1` for switch debug logs |

### 11.4 Reporting a bug

Collect:

1. `ringqswitch -version`
2. The configuration (remove passwords)
3. A debug log with `sip-trace` enabled covering one failing call
4. A capture: `sudo tcpdump -ni any -s0 -w call.pcap port 5060 or udp portrange 16384-32768`
5. The phone/carrier model and firmware

---

## 12. Uninstall

```bash
sudo systemctl disable --now ringqswitch
sudo rm -f /etc/systemd/system/ringqswitch.service /usr/local/bin/ringqswitch /usr/local/bin/rq_cli
sudo systemctl daemon-reload
sudo rm -rf /etc/ringqswitch /var/lib/ringqswitch      # deletes config, CDRs and recordings
sudo userdel ringq
```

---

## 13. Keeping the source in your own repository

```bash
cd ringqswitch-0.10.1
git init -b main
git add -A
git commit -m "RingQswitch 0.10.1"
git remote add origin <your repository URL>
git push -u origin main
```

`.gitignore` already excludes `bin/`, `log/` and `recordings/`.

---

## 14. Run RingQswitch under your RingQ portal (PostgreSQL)

Everything above runs RingQswitch with its own XML files. To run it under
your RingQ portal instead, with the extensions, trunks, DIDs, ring groups,
queues and IVRs from your PostgreSQL database, start with the plain-English
guide **[docs/STEP-BY-STEP.md](docs/STEP-BY-STEP.md)**: one command
(`sudo bash packaging/setup-test-db.sh`) creates a test database with sample
extensions on a test server. The full reference is
[docs/DATABASE.md](docs/DATABASE.md), and the web portal is described in
[docs/WEB-UI.md](docs/WEB-UI.md). On the real server, in short:

```bash
sudo cp conf/examples/ringq-postgres.xml /etc/ringqswitch/ringqswitch.xml
sudo nano /etc/ringqswitch/ringqswitch.xml     # dsn, event-socket-password, recordings-dir, <web>
ringqswitch -conf /etc/ringqswitch -check      # loads the database, prints counts
ringqswitch -conf /etc/ringqswitch -compat     # what in your dialplans is not supported yet
systemctl list-units --type=service --state=running   # find the previous switch's service
sudo systemctl disable --now <previous switch service>
sudo systemctl restart ringqswitch && sudo journalctl -u ringqswitch -f
```

Rollback: `sudo systemctl stop ringqswitch && sudo systemctl enable --now <previous switch service>`.

The database tests can be run on that server too (they use a throw-away
schema and copy your table definitions; nothing in your tables changes):

```bash
make test-db RINGQ_TEST_DSN='postgres://USER:PASS@127.0.0.1/DB' RINGQ_TEST_LIKE_SCHEMA=public
```

## 15. Current limitations

- Signalling is SIP over UDP and TCP; SIP over TLS and SRTP for desk phones
  are not supported yet (the browser dialer and meetings are always
  encrypted).
- G.711 (PCMU/PCMA) is transcoded; other codecs (G.722, Opus...) are relayed
  untouched when both sides share one, but not transcoded.
- DTMF must be sent as RFC 2833/4733 or SIP INFO (not in the audio).
- `ringqswitch -compat` lists dialplan applications and functions in your
  configuration that RingQswitch does not run; see
  [docs/ROADMAP.md](docs/ROADMAP.md) for what is planned.
