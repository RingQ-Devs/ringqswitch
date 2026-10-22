# RingQswitch with a database: step-by-step guide (plain English)

> **Already running 0.3.0 (or 0.2.1) with the database?** Go straight to
> [WEB-UI.md](WEB-UI.md), section 1: it upgrades you to 0.10.1 (extension
> logins, browser dialer, chat, meetings, wallboard, security page, SSL).

This guide takes you from "RingQswitch reads its extensions from files" to
"RingQswitch reads them from the PostgreSQL database of your RingQ portal". Copy and paste the commands exactly. Lines starting with
`#` are explanations; you do not type them.

## What you are doing

Up to now RingQswitch on your test server read its extensions (1000, 1001...)
from XML files. Your real RingQ system keeps extensions, trunks, IVRs and call
records in a **PostgreSQL database**, and the switch behind the portal reads
that database. The goal is for RingQswitch to read **the same database** and
become that switch.

There are three stages. Do them in order and don't skip ahead:

| Stage | Where | What | Risk |
|---|---|---|---|
| **1. Test with sample data** | your test server | a small test database with 5 extensions; you make calls with two softphones | none |
| **2. Test with your real data** | your test server | a **copy** of the real RingQ database; you check your real extensions, ring groups and IVRs | none (it's a copy) |
| **3. Switch over** | the real RingQ server | stop the previous switch, start RingQswitch, with a way back | live system |

### Why you got "connection refused"

```
ringqswitch: database: failed to connect to ... 127.0.0.1:5432 ... connection refused
```

The example configuration you copied says "the database is on this server,
port 5432", but **no database is installed on your test server yet**.
Nothing is broken. Step 2 below installs one and connects RingQswitch to it.

A `chmod: cannot access '.../recordings'` message is expected too: that
folder only exists on the real RingQ server. The setup script uses
RingQswitch's own folder on the test server instead.

> **Your phones stop working until you finish Stage 1.** Once RingQswitch
> uses the database, it only knows the extensions in the database (with new
> passwords). The old XML extensions no longer work. To go back at any time,
> see "Undo" at the end of Stage 1.

---

## Stage 1: test with sample data (about 15 minutes)

### Step 1: Put version 0.10.1 on the server

1. Download `ringqswitch-0.10.1-src.tar.gz` from this chat.
2. Upload it to the server's `/root` folder with your file transfer
   program (SFTP), the same way you uploaded the earlier version.
3. On the server (logged in as root):

```bash
cd /opt
tar xzf /root/ringqswitch-0.10.1-src.tar.gz
cd /opt/ringqswitch-0.10.1
make build
sudo make install
```

The last lines may say the configuration check failed because there's no
database. **That's expected.** Carry on.

### Step 2: Create the test database (one command)

Still in `/opt/ringqswitch-0.10.1`:

```bash
sudo bash packaging/setup-test-db.sh
```

This script does everything for you:

* installs PostgreSQL (the database program) if it is missing;
* creates a database called `ringqswitch` with the same tables your RingQ
  database has (plus RingQswitch's own `rq_` tables);
* adds sample data: domain `test.ringq.local`, extensions 1000 to 1004, an
  echo test, a tone test, a ring group (2000), an IVR (5000), the feature
  codes (`*22`, `*97`...) and AUX codes;
* switches on the **web interface** (https://your-server:8443) with a random
  password;
* writes a new `/etc/ringqswitch/ringqswitch.xml` that points at the database
  (your old file is kept as a backup);
* checks everything and restarts RingQswitch.

It takes 1 to 3 minutes. At the end it prints a box like this. **Note the
password.**

```
==================================================================
Softphone settings:
  SIP server / domain : 174.138.24.53
  Username            : 1000   (or 1001, 1002, 1003, 1004)
  Password            : Rq3f9a1c2e
  Transport           : UDP, port 5060

Numbers to dial:
  1001 .. 1004  call another extension
  9196          echo test (you hear yourself)
  9197          1 kHz test tone
  2000          ring group: 1001 and 1002 ring together
  5000          IVR: two beeps, then press 1 (ring group) or 2 (extension 1000)
  ...
Web interface : https://174.138.24.53:8443/
  Web user    : admin
  Web password: Rq3f9c0a1b2c4d
==================================================================
```

You can show it again at any time:

```bash
sudo cat /root/ringqswitch-test.txt
```

To choose the extension password yourself, run the script with
`--ext-password MyPassword123` instead.

### Step 3: Check that RingQswitch is running

```bash
sudo systemctl status ringqswitch --no-pager
```

Look for **`active (running)`** in green. Then:

```bash
sudo rq_cli -x domains
```

You should see `test.ringq.local (default)` with `5` users.

### Step 4: Set up two softphones

Use a softphone on your PC for one extension, and a second softphone (on
another PC, or a softphone app on a mobile) for the other.

In the softphone, add an account (or edit your existing one):

| Field | Phone 1 | Phone 2 |
|---|---|---|
| Account name | Test 1000 | Test 1001 |
| SIP server | `174.138.24.53` | `174.138.24.53` |
| SIP proxy | (empty) | (empty) |
| Username | `1000` | `1001` |
| Domain | `174.138.24.53` | `174.138.24.53` |
| Login | `1000` | `1001` |
| Password | the password from Step 2 | the same password |
| Transport | UDP | UDP |

Save it. The status should change to **Online** (or *Registered*).

Check on the server:

```bash
sudo rq_cli -x "show registrations"
```

You should see `1000@test.ringq.local` and `1001@test.ringq.local`.

### Step 5: Make test calls

From phone 1000, dial each number and tick it off:

| Dial | What should happen |
|---|---|
| `1001` | Phone 1001 rings. Answer it; you can talk both ways. |
| `9196` | Answers straight away; you hear your own voice back (echo). |
| `9197` | Answers; you hear a continuous beep. |
| `2000` | Phone 1001 rings (1002 would ring too if it were online). |
| `5000` | You hear two beeps. Press **1**: phone 1001 rings. Call again and press **2**: it goes to 1000. |
| `1003` | The softphone shows *Temporarily Unavailable*: extension 1003 exists, but no phone is logged in as 1003. |

### Step 6: See the results in the database

Each finished call is stored in the database, in the tables the portal
reads. Look at the last calls:

```bash
sudo -u postgres psql ringqswitch -c "select start_stamp, caller_id_number, destination_number, billsec, hangup_cause, status from v_xml_cdr order by start_stamp desc limit 10"
```

Registered phones, as the portal would see them:

```bash
sudo -u postgres psql ringqswitch -c "select reg_user, realm, network_ip, network_port from registrations"
```

During a call, the call in progress:

```bash
sudo -u postgres psql ringqswitch -c "select cid_num, dest, callstate from channels"
```

### Step 7: Prove that database changes apply by themselves

In the real system your RingQ portal changes the database and the switch must
follow. Try it by adding extension 1005 directly in the database:

```bash
sudo -u postgres psql ringqswitch -c "insert into v_extensions (extension_uuid, domain_uuid, extension, password, effective_caller_id_name, effective_caller_id_number, user_context, enabled) values (gen_random_uuid(), 'a0000000-0000-4000-8000-0000000000a1', '1005', 'MyPass555', 'Ext 1005', '1005', 'test.ringq.local', 'true')"
```

Within 5 seconds the log shows it was picked up:

```bash
sudo journalctl -u ringqswitch -n 5 --no-pager
# ... msg="database configuration changed; reloading"
```

Now register a phone as `1005` with password `MyPass555`: it works without
restarting anything. Changing a password works the same way:

```bash
sudo -u postgres psql ringqswitch -c "update v_extensions set password = 'NewPass1001' where extension = '1001'"
```

**Stage 1 is finished** when all of Steps 3 to 7 work.

### Step 8 (optional): look at the database from your PC

Use a PostgreSQL desktop tool with an **SSH tunnel**. The tool logs in to
the server with SSH (as your SSH program does) and reaches the database from
inside the server. You don't open any extra port and you don't change any
PostgreSQL setting.

In the tool, add a server connection:

| Tab | Field | Value |
|---|---|---|
| General | Name | `RingQswitch test` |
| Connection | Host name/address | `127.0.0.1` (yes, 127.0.0.1: "the database as seen from the server") |
| Connection | Port | `5432` |
| Connection | Maintenance database | `ringqswitch` |
| Connection | Username | `ringqswitch` |
| Connection | Password | the **Database** password from `sudo cat /root/ringqswitch-test.txt` (not the extension password) |
| SSH Tunnel | Use SSH tunneling | on |
| SSH Tunnel | Tunnel host | `174.138.24.53` |
| SSH Tunnel | Tunnel port | `22` |
| SSH Tunnel | Username | `root` |
| SSH Tunnel | Authentication | Password (your root password) or Identity file (your SSH key) |

Save it. The tables are in the `ringqswitch` database, schema `public`.
The interesting ones are `v_extensions`, `v_dialplans`, `registrations`,
`channels` and `v_xml_cdr`.

Connecting straight to `174.138.24.53:5432` without the tunnel times out,
because the server's firewall blocks port 5432, as it blocked 5060
before. Opening PostgreSQL to the whole internet invites password-guessing
attacks. If you really need a direct connection, allow only your office IP
(see "Database tool: connection timeout" below).

### If something goes wrong

| What you see | What to do |
|---|---|
| `connection refused` (port 5432) | The database is not running: `sudo systemctl start postgresql`, then `sudo systemctl restart ringqswitch` |
| `database ringqswitch already exists` | You already ran Step 2. To start again from scratch: `sudo bash packaging/setup-test-db.sh --reset` (makes **new** passwords) |
| Phone shows **403 Forbidden** | Wrong password. Check `sudo cat /root/ringqswitch-test.txt`. After 10 wrong tries the PC's IP is blocked for 10 minutes: `sudo rq_cli -x blocked`, then `sudo rq_cli -x "unblock YOUR.PC.IP"` |
| Phone shows **Request Timeout** | Firewall. Same fix as before: `sudo ufw allow 5060/udp && sudo ufw allow 5060/tcp && sudo ufw allow 16384:32768/udp` |
| Call connects but **no sound** | RTP ports blocked in the firewall (line above), or the wrong public IP. The script copies `ext-sip-ip` from your old file; check with `grep ext- /etc/ringqswitch/ringqswitch.xml` (it should show 174.138.24.53) |
| Database tool: **connection timeout expired** | Use the SSH Tunnel settings in Step 8. For a direct connection instead: in `/etc/postgresql/17/main/postgresql.conf` set `listen_addresses = '*'` (with the **s**); in `/etc/postgresql/17/main/pg_hba.conf` add `host ringqswitch ringqswitch YOUR.OFFICE.IP/32 scram-sha-256`; then `sudo systemctl restart postgresql@17-main` and `sudo ufw allow from YOUR.OFFICE.IP to any port 5432 proto tcp`. Check with `ss -ltn \| grep 5432`: it should show `0.0.0.0:5432` |
| Database tool: **database "ringq" does not exist** | The database is called `ringqswitch` (user name `ringqswitch` too); `ringq_test` was the name before version 0.3.0 |
| `systemctl status` shows **failed** | `sudo journalctl -u ringqswitch -n 50 --no-pager` shows why. Send us those lines |

### Undo: go back to the old XML setup

```bash
sudo cp /etc/ringqswitch/ringqswitch.xml.orig /etc/ringqswitch/ringqswitch.xml
sudo systemctl restart ringqswitch
```

Your old extensions and passwords work again. The test database stays
installed for later.

---

## Stage 2: test with a copy of your real RingQ data

The `ringqswitch.sql` file you sent earlier contains the **table structure**
of your database, but no extensions, trunks or dialplans (only a few call
records). For a real test you need a full copy from the server where RingQ
runs today.

### Step 1: Make a copy on the production server

This is read-only: it does not stop or change anything. First find the
database name and user:

```bash
sudo grep -sE '^database\.0\.(name|username)' /etc/*/config.conf
```

If that prints nothing, ask whoever installed RingQ for the database
name. Then make the copy. Replace `DBNAME` with your database name; call
records are left out to keep the file small:

```bash
sudo -u postgres pg_dump --no-owner --no-privileges \
  --exclude-table-data='v_xml_cdr*' --exclude-table-data='q_ringq_cdr*' \
  DBNAME > /root/ringq-copy.sql
ls -lh /root/ringq-copy.sql
```

Download `/root/ringq-copy.sql` with your file transfer program and upload it to `/root` on the
**test** server. Keep it private: it contains your extension passwords.

### Step 2: Load the copy on the test server

```bash
cd /opt/ringqswitch-0.10.1
sudo bash packaging/setup-test-db.sh --db ringq_copy --load /root/ringq-copy.sql
```

Same script, with these differences:

* It loads your real data into a separate database, `ringq_copy`. The sample
  database is not touched.
* It **switches off trunk registration in the copy**, so your carriers keep
  sending calls to the production server and not to the test server.
* A few `ERROR` lines while loading are normal: missing database users, for
  example. The script prints a summary.

If the error summary mentions `invalid command \restrict`, you can ignore
it: your production PostgreSQL is newer than the test server's and adds that
line, but the data loads anyway.

If your database has **several domains (tenants)**, the script prints them.
Either type the domain name in the phone's **Domain** field, or rerun the
command with `--reset --default-domain your.domain.com` at the end, so phones
that use the server IP land in that domain.

### Step 3: Check what is supported

```bash
sudo ringqswitch -conf /etc/ringqswitch -check
sudo ringqswitch -conf /etc/ringqswitch -compat
```

The first command shows how many domains, extensions, gateways, ring groups
and IVRs were loaded: compare them with your portal. The second lists
anything in your dialplans that RingQswitch can't do yet. **Send us both
outputs.**

### Step 4: Test with real extensions

Set up a softphone with a real extension number and its password from the
portal: SIP server `174.138.24.53`, Domain = the tenant's domain name. Test
calls between extensions, ring groups, IVRs and call forwarding, and check
the CDRs:

```bash
sudo -u postgres psql ringq_copy -c "select start_stamp, caller_id_number, destination_number, billsec, hangup_cause, status from v_xml_cdr order by start_stamp desc limit 10"
```

Don't point real DID numbers or carriers at the test server. Outbound calls
through a trunk may reach the real carrier and cost money if the carrier
accepts them by IP address.

To switch the test server back to the sample database later:

```bash
sudo bash packaging/setup-test-db.sh --reset
```

---

## Stage 3: switch over on the real server

Only start Stage 3 when Stage 2 works and we have reviewed your `-compat`
report, so you know that everything you rely on is supported.

In short, on the production server in a quiet hour:

1. Back up the database: `sudo -u postgres pg_dump -Fc DBNAME > /root/before-ringqswitch.dump`
2. Install RingQswitch (Step 1 of Stage 1). **Don't** run `setup-test-db.sh` there.
3. `sudo cp conf/examples/ringq-postgres.xml /etc/ringqswitch/ringqswitch.xml`,
   then edit the `dsn` line with the real database name, user and password,
   and set `event-socket-password` to the one your RingQ backend uses.
4. `sudo ringqswitch -conf /etc/ringqswitch -check`
5. Stop the previous switch: `sudo systemctl disable --now <previous switch service>`
   (`systemctl list-units --type=service --state=running` shows its name)
6. `sudo systemctl enable --now ringqswitch`

To go back to the previous switch at any moment:

```bash
sudo systemctl disable --now ringqswitch
sudo systemctl enable --now <previous switch service>
```

The detailed version, with every setting explained, is in
[DATABASE.md](DATABASE.md).
