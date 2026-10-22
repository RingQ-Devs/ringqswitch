#!/usr/bin/env bash
# Prepare a TEST database for RingQswitch on this server and point
# RingQswitch at it. Safe to run on a test machine: it never touches any
# other database.
#
#   sudo bash packaging/setup-test-db.sh
#       Installs PostgreSQL if needed, creates database "ringqswitch" with the
#       tables RingQswitch uses and sample data (extensions 1000-1004, a ring
#       group, an IVR, feature codes, AUX codes), switches on the web
#       interface (https://<server>:8443), configures /etc/ringqswitch and
#       restarts the service. Prints the passwords at the end (also saved in
#       /root/ringqswitch-test.txt).
#
#   sudo bash packaging/setup-test-db.sh --db ringq_copy --load /root/ringq.sql
#       Same, but loads a dump of your real RingQ database instead of the
#       sample data. Gateway registration is switched off in that copy so the
#       test server cannot steal your carriers' calls (--keep-gateways to
#       leave it on).
#
# Options:
#   --db NAME             database name (default ringqswitch)
#   --db-user NAME        database user (default ringqswitch)
#   --load FILE           load this SQL dump (plain format) instead of the built-in tables
#   --sample              also add the sample data when using --load
#   --ext-password PW     password of the sample extensions (default: the one in
#                         /root/ringqswitch-test.txt from an earlier run, else random)
#   --web-password PW     web interface password for user "admin" (default: random)
#   --web-port PORT       web interface port (default 8443; 0 = no web interface)
#   --domain NAME         domain of the sample data (default test.ringq.local)
#   --default-domain NAME domain for phones that use the server IP (needed with several domains)
#   --public-ip IP        this server's public IP (ext-sip-ip/ext-rtp-ip); default: kept from the old config
#   --keep-gateways       with --load: leave gateway registration as it is in the dump
#   --reset               delete and recreate the database if it already exists
#   --no-restart          configure only, do not restart the service
set -euo pipefail

DB=ringqswitch
DBUSER=ringqswitch
WEBPW=""
WEBPORT=8443
LOAD=""
SAMPLE=""
EXTPW=""
DOMAIN=test.ringq.local
DEFAULT_DOMAIN=""
PUBLIC_IP=""
KEEPGW=0
RESET=0
RESTART=1
CONF=/etc/ringqswitch
SRC="$(cd "$(dirname "$0")/.." && pwd)"

die() { echo "error: $*" >&2; exit 1; }
step() { echo; echo "==> $*"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --db) DB="${2:?}"; shift 2 ;;
    --db-user) DBUSER="${2:?}"; shift 2 ;;
    --web-password) WEBPW="${2:?}"; shift 2 ;;
    --web-port) WEBPORT="${2:?}"; shift 2 ;;
    --load) LOAD="${2:?}"; shift 2 ;;
    --sample) SAMPLE=1; shift ;;
    --ext-password) EXTPW="${2:?}"; shift 2 ;;
    --domain) DOMAIN="${2:?}"; shift 2 ;;
    --default-domain) DEFAULT_DOMAIN="${2:?}"; shift 2 ;;
    --public-ip) PUBLIC_IP="${2:?}"; shift 2 ;;
    --keep-gateways) KEEPGW=1; shift ;;
    --reset) RESET=1; shift ;;
    --no-restart) RESTART=0; shift ;;
    -h|--help) sed -n '2,42p' "$0"; exit 0 ;;
    *) die "unknown option $1 (see --help)" ;;
  esac
done

[ "$(id -u)" -eq 0 ] || die "run as root: sudo bash $0"
[[ "$DB" =~ ^[a-z_][a-z0-9_]*$ ]] || die "--db: use lower-case letters, digits and _"
[[ "$DBUSER" =~ ^[a-z_][a-z0-9_]*$ ]] || die "--db-user: use lower-case letters, digits and _"
[[ "$WEBPORT" =~ ^[0-9]+$ ]] && [ "$WEBPORT" -le 65535 ] || die "--web-port: a port number"
[[ "$DOMAIN" =~ ^[A-Za-z0-9.-]+$ ]] || die "--domain: letters, digits, dots and dashes only"
[ -z "$SAMPLE" ] && { [ -n "$LOAD" ] && SAMPLE=0 || SAMPLE=1; }
if [ -n "$LOAD" ]; then
  [ -f "$LOAD" ] || die "--load: file $LOAD not found"
  LOAD="$(cd "$(dirname "$LOAD")" && pwd)/$(basename "$LOAD")"
fi
command -v ringqswitch >/dev/null || die "RingQswitch is not installed yet: run 'make build && sudo make install' first"
EXAMPLE="$SRC/conf/examples/ringq-postgres.xml"
[ -f "$EXAMPLE" ] || die "$EXAMPLE not found (run this script from the RingQswitch folder)"
ringqswitch -version | grep -qE ' 0\.([3-9]|[1-9][0-9])\.' || die "the installed ringqswitch is older than this script: run 'make build && sudo make install' first"

randhex() { local x; x=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'); echo "${x:0:$1}"; }
DBPW="$(randhex 24)"
if [ -z "$EXTPW" ] && [ -f /root/ringqswitch-test.txt ]; then
  # Keep the phones of an earlier test working.
  EXTPW=$(grep -m1 -E '^ +Password +: ' /root/ringqswitch-test.txt | awk '{print $3}' || true)
fi
if [ -z "$EXTPW" ]; then EXTPW="Rq$(randhex 8)"; fi
[[ "$EXTPW" =~ ^[A-Za-z0-9._@-]+$ ]] || die "--ext-password: letters, digits and . _ @ - only"
if [ -z "$WEBPW" ]; then WEBPW="Rq$(randhex 12)"; fi
[[ "$WEBPW" =~ ^[A-Za-z0-9._@-]{8,}$ ]] || die "--web-password: at least 8 letters, digits or . _ @ -"

# Run psql as the postgres superuser, from a directory it can read.
pg() { (cd / && runuser -u postgres -- psql -X -q -v ON_ERROR_STOP=1 "$@"); }
# Run psql as the RingQswitch database user.
pgdb() { PGPASSWORD="$DBPW" psql -X -q -h 127.0.0.1 -U "$DBUSER" -d "$DB" "$@"; }

# ---------------------------------------------------------------------------
step "PostgreSQL"
if ! command -v pg_lsclusters >/dev/null 2>&1 || [ -z "$(pg_lsclusters -h 2>/dev/null)" ]; then
  echo "installing PostgreSQL (apt)..."
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq postgresql >/dev/null
fi
if ! pg_isready -q -h 127.0.0.1 -p 5432; then
  systemctl enable --now postgresql >/dev/null 2>&1 || true
  # Without systemd (containers, WSL) start the clusters directly.
  if ! pg_isready -q -h 127.0.0.1 -p 5432 && [ ! -d /run/systemd/system ]; then
    pg_lsclusters -h | while read -r ver name _ status _; do
      [ "$status" = online ] || pg_ctlcluster "$ver" "$name" start || true
    done
  fi
fi
for i in $(seq 1 30); do
  pg_isready -q -h 127.0.0.1 -p 5432 && break
  [ "$i" -eq 30 ] && die "PostgreSQL is not answering on 127.0.0.1:5432 (check: systemctl status postgresql)"
  sleep 1
done
echo "PostgreSQL is running: $(pg -tAc 'show server_version')"

# ---------------------------------------------------------------------------
step "Database $DB and user $DBUSER"
if [ -n "$(pg -tAc "select 1 from pg_database where datname = '$DB'")" ]; then
  if [ "$RESET" -eq 1 ]; then
    echo "deleting the existing database $DB (--reset)"
    systemctl stop ringqswitch 2>/dev/null || true
    pg -c "select pg_terminate_backend(pid) from pg_stat_activity where datname = '$DB'" >/dev/null
    pg -c "drop database \"$DB\""
  else
    die "database $DB already exists. Add --reset to delete and recreate it, or use --db another_name"
  fi
fi
if [ -n "$(pg -tAc "select 1 from pg_roles where rolname = '$DBUSER'")" ]; then
  pg -c "alter role \"$DBUSER\" with login password '$DBPW'"
else
  pg -c "create role \"$DBUSER\" with login password '$DBPW'"
fi
pg -c "create database \"$DB\" owner \"$DBUSER\""
echo "created database $DB owned by $DBUSER"

# ---------------------------------------------------------------------------
if [ -n "$LOAD" ]; then
  step "Loading $LOAD (this can take a while)"
  log="/root/ringqswitch-load-$DB.log"
  pgdb -f "$LOAD" > "$log" 2>&1 || true
  errors=$(grep -c "ERROR:" "$log" || true)
  echo "done; $errors error line(s), details in $log"
  if [ "$errors" -gt 0 ]; then
    echo "(a few errors are normal: missing roles/owners, MySQL-style DELIMITER lines from"
    echo " Adminer exports, a trigger whose function is not in the dump. RingQswitch only"
    echo " needs the tables.)"
    grep "ERROR:" "$log" | sed 's/^/   /' | sort | uniq -c | sort -rn | head -8
  fi
  for t in v_domains v_extensions v_dialplans; do
    [ "$(pgdb -tAc "select to_regclass('public.$t') is not null")" = "t" ] || die "table $t is missing after loading $LOAD: is it a RingQ database dump?"
  done
fi

# ---------------------------------------------------------------------------
step "Configuring $CONF/ringqswitch.xml"
mkdir -p "$CONF"
cfg="$CONF/ringqswitch.xml"
if [ -z "$PUBLIC_IP" ]; then
  for f in "$cfg" "$cfg.orig"; do
    [ -f "$f" ] || continue
    ip=$(grep -o 'name="ext-sip-ip" value="[0-9.]*"' "$f" | grep -o '[0-9][0-9.]*' | head -1 || true)
    if [ -n "$ip" ]; then PUBLIC_IP="$ip"; break; fi
  done
fi
if [ -f "$cfg" ]; then
  backup="$cfg.before-test-db.$(date +%Y%m%d-%H%M%S)"
  cp -p "$cfg" "$backup"
  echo "previous configuration saved as $backup"
fi
cp "$EXAMPLE" "$cfg"
dsn="postgres://$DBUSER:$DBPW@127.0.0.1:5432/$DB?sslmode=disable"
sed -i -e "s|<param name=\"dsn\" value=\"[^\"]*\"/>|<param name=\"dsn\" value=\"$dsn\"/>|" \
       -e 's|<param name="poll-seconds" value="[0-9]*"/>|<param name="poll-seconds" value="5"/>|' "$cfg"
[ -d /usr/share/freeswitch/sounds ] || sed -i 's|"/usr/share/freeswitch/sounds"|"/var/lib/ringqswitch/sounds"|' "$cfg"
[ -d /var/lib/freeswitch/recordings ] || sed -i 's|"/var/lib/freeswitch/recordings"|"/var/lib/ringqswitch/recordings"|' "$cfg"
if [ -n "$PUBLIC_IP" ]; then
  sed -i -e "s|<param name=\"ext-sip-ip\" value=\"[^\"]*\"/>|<param name=\"ext-sip-ip\" value=\"$PUBLIC_IP\"/>|" \
         -e "s|<param name=\"ext-rtp-ip\" value=\"[^\"]*\"/>|<param name=\"ext-rtp-ip\" value=\"$PUBLIC_IP\"/>|" "$cfg"
fi
if [ -n "$LOAD" ] && [ "$(pgdb -tAc "select to_regclass('public.q_ringq_cdr') is not null")" = "t" ]; then
  sed -i 's|<param name="cdr-tables" value="v_xml_cdr"/>|<param name="cdr-tables" value="v_xml_cdr,q_ringq_cdr"/>|' "$cfg"
fi
grep -q "$DB?sslmode" "$cfg" || die "could not set the database address in $cfg"
if [ "$WEBPORT" -gt 0 ]; then
  sed -i -e "s|<param name=\"listen\" value=\"\"/>|<param name=\"listen\" value=\"0.0.0.0:$WEBPORT\"/>|" \
         -e "s|<param name=\"admin-password\" value=\"[^\"]*\"/>|<param name=\"admin-password\" value=\"$WEBPW\"/>|" "$cfg"
  grep -q "0.0.0.0:$WEBPORT" "$cfg" || die "could not switch on the web interface in $cfg"
fi
mkdir -p /var/lib/ringqswitch/voicemail /var/lib/ringqswitch/sounds/music/default /var/lib/ringqswitch/sounds/prompts /var/lib/ringqswitch/recordings
id ringq >/dev/null 2>&1 && chown -R ringq:ringq /var/lib/ringqswitch

if id ringq >/dev/null 2>&1; then
  chown root:ringq "$cfg"
  chmod 0640 "$cfg"
fi
ESLPW=$(grep -o 'name="event-socket-password" value="[^"]*"' "$cfg" | sed 's/.*value="//; s/"$//')

# ---------------------------------------------------------------------------
step "Creating the tables RingQswitch uses"
ringqswitch -conf "$CONF" -init-db

# ---------------------------------------------------------------------------
if [ "$SAMPLE" -eq 1 ]; then
  step "Adding sample data (domain $DOMAIN, extensions 1000-1004, ring group 2000, IVR 5000)"
  pgdb -v ON_ERROR_STOP=1 -v domain="$DOMAIN" -v extpw="$EXTPW" <<'SQL'
\set duuid 'a0000000-0000-4000-8000-0000000000a1'
\set rguuid 'c0000000-0000-4000-8000-0000000000c1'
\set ivruuid 'd0000000-0000-4000-8000-0000000000d1'
insert into v_domains (domain_uuid, domain_name, domain_enabled, domain_description)
  values (:'duuid', :'domain', true, 'RingQswitch test domain');
insert into v_extensions (extension_uuid, domain_uuid, extension, password, effective_caller_id_name,
    effective_caller_id_number, outbound_caller_id_name, outbound_caller_id_number, user_context,
    call_timeout, enabled, max_registrations, description)
  select gen_random_uuid(), :'duuid', e, :'extpw', 'Ext ' || e, e, 'Test Company', '1000', :'domain', 30, 'true', '3', 'test extension'
  from unnest(array['1000','1001','1002','1003','1004']) as e;
insert into v_vars (var_uuid, var_category, var_name, var_value, var_enabled, var_order) values
  (gen_random_uuid(), 'Tones', 'us-ring', '%(2000,4000,440,480)', 'true', 1),
  (gen_random_uuid(), 'Tones', 'busy', '%(500,500,480,620)', 'true', 2);

insert into v_ring_groups (ring_group_uuid, domain_uuid, ring_group_name, ring_group_extension, ring_group_context,
    ring_group_strategy, ring_group_call_timeout, ring_group_enabled, ring_group_description)
  values (:'rguuid', :'duuid', 'Sales', '2000', :'domain', 'simultaneous', 30, 'true', 'test ring group');
insert into v_ring_group_destinations (ring_group_destination_uuid, domain_uuid, ring_group_uuid,
    destination_number, destination_delay, destination_timeout, destination_enabled)
  values (gen_random_uuid(), :'duuid', :'rguuid', '1001', 0, 30, true),
         (gen_random_uuid(), :'duuid', :'rguuid', '1002', 0, 30, true);

insert into v_ivr_menus (ivr_menu_uuid, domain_uuid, ivr_menu_name, ivr_menu_extension, ivr_menu_timeout,
    ivr_menu_inter_digit_timeout, ivr_menu_max_failures, ivr_menu_max_timeouts, ivr_menu_digit_len,
    ivr_menu_direct_dial, ivr_menu_enabled, ivr_menu_greet_long, ivr_menu_description)
  values (:'ivruuid', :'duuid', 'Main', '5000', 5000, 2000, 3, 3, 4, 'true', 'true',
          'tone_stream://%(200,100,800);loops=2', 'beeps, then press 1 or 2 (or dial an extension)');
insert into v_ivr_menu_options (ivr_menu_option_uuid, ivr_menu_uuid, domain_uuid, ivr_menu_option_digits,
    ivr_menu_option_action, ivr_menu_option_param, ivr_menu_option_order, ivr_menu_option_enabled)
  values (gen_random_uuid(), :'ivruuid', :'duuid', '1', 'menu-exec-app', 'transfer 2000 XML ' || :'domain', 10, true),
         (gen_random_uuid(), :'ivruuid', :'duuid', '2', 'menu-exec-app', 'transfer 1000 XML ' || :'domain', 20, true);
SQL
  echo "sample data added"
fi

if [ -n "$LOAD" ] && [ "$KEEPGW" -eq 0 ] && [ "$(pgdb -tAc "select to_regclass('public.v_gateways') is not null")" = "t" ]; then
  n=$(pgdb -tAc "with u as (update v_gateways set register = 'false' where register = 'true' returning 1) select count(*) from u")
  echo
  echo "switched off registration of $n gateway(s) in this TEST copy, so the carriers keep"
  echo "sending calls to your production server (use --keep-gateways to keep them on)"
fi

# ---------------------------------------------------------------------------
step "Adding default call routing (feature codes, AUX codes)"
ringqswitch -conf "$CONF" -init-db
if [ "$SAMPLE" -eq 1 ] && [ "$WEBPORT" -gt 0 ]; then
  # Extension 1000 manages the domain from the web interface (Owner).
  RQ_WEB_PASSWORD="$WEBPW" ringqswitch -conf "$CONF" -web-access "1000@$DOMAIN" -role owner
fi

ndomains=$(pgdb -tAc "select count(*) from v_domains where coalesce(domain_enabled::text, 'true') in ('true','t')")
nexts=$(pgdb -tAc "select count(*) from v_extensions")

if [ -z "$DEFAULT_DOMAIN" ] && [ "$SAMPLE" -eq 1 ] && [ "$ndomains" -gt 1 ]; then
  DEFAULT_DOMAIN="$DOMAIN"
fi
if [ -n "$DEFAULT_DOMAIN" ]; then
  sed -i "s|<!-- <param name=\"default-domain\" value=\"[^\"]*\"/> -->|<param name=\"default-domain\" value=\"$DEFAULT_DOMAIN\"/>|" "$cfg"
fi

# ---------------------------------------------------------------------------
step "Checking the configuration"
if ! ringqswitch -conf "$CONF" -check; then
  die "the configuration check failed (see the messages above)"
fi
if [ "$ndomains" -gt 1 ] && [ -z "$DEFAULT_DOMAIN" ]; then
  echo
  echo "NOTE: the database has $ndomains domains. Phones that use the server IP address as"
  echo "their domain need: --default-domain <one of them>  (or use the domain name on the phone)"
  pgdb -tAc "select '   ' || domain_name from v_domains order by 1"
fi

if [ "$RESTART" -eq 1 ] && [ -d /run/systemd/system ]; then
  step "Restarting RingQswitch"
  # The previous switch of a RingQ server listens on the same ports.
  OLD_SWITCH=freeswitch
  if systemctl is-active --quiet "$OLD_SWITCH" 2>/dev/null; then
    echo "Another phone switch is running on this server and uses the same ports."
    die "stop it first (sudo systemctl disable --now $OLD_SWITCH), then run this script again"
  fi
  systemctl enable ringqswitch >/dev/null 2>&1 || true
  systemctl restart ringqswitch
  sleep 2
  if systemctl is-active --quiet ringqswitch; then
    echo "RingQswitch is running"
  else
    journalctl -u ringqswitch -n 20 --no-pager || true
    die "RingQswitch did not start (messages above)"
  fi
fi

if [ "$WEBPORT" -gt 0 ] && command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
  ufw allow "$WEBPORT/tcp" >/dev/null && echo "firewall (ufw): opened port $WEBPORT/tcp for the web interface"
  ufw allow 8443/udp >/dev/null && echo "firewall (ufw): opened port 8443/udp for the dialer and meetings"
fi

# ---------------------------------------------------------------------------
server_ip="${PUBLIC_IP:-$(hostname -I 2>/dev/null | awk '{print $1}')}"
summary="/root/ringqswitch-test.txt"
{
  echo "RingQswitch test database ($(date))"
  echo
  echo "Database      : $DB on 127.0.0.1:5432, user $DBUSER, password $DBPW"
  echo "Open it       : sudo -u postgres psql $DB"
  echo "Event socket  : rq_cli -p $ESLPW -x status"
  echo "Domains       : $ndomains    Extensions: $nexts"
  if [ "$WEBPORT" -gt 0 ]; then
    echo
    echo "Web interface : https://$server_ip:$WEBPORT/   (accept the certificate warning once)"
    echo "  Web user    : admin   (administrator)"
    [ "$SAMPLE" -eq 1 ] && echo "              or 1000    (extension 1000, Owner: also has the dialer, chat and meetings)"
    echo "  Web password: $WEBPW"
  fi
  if [ "$SAMPLE" -eq 1 ]; then
    echo
    echo "Softphone settings:"
    echo "  SIP server / domain : $server_ip"
    echo "  Username            : 1000   (or 1001, 1002, 1003, 1004)"
    echo "  Password            : $EXTPW"
    echo "  Transport           : UDP, port 5060"
    echo
    echo "Numbers to dial:"
    echo "  1001 .. 1004  call another extension"
    echo "  9196          echo test (you hear yourself)"
    echo "  9197          1 kHz test tone"
    echo "  2000          ring group: 1001 and 1002 ring together"
    echo "  5000          IVR: two beeps, then press 1 (ring group) or 2 (extension 1000)"
    echo "  *22 / *23     call-centre agent: log in-out / break on-off"
    echo "  *99<ext>      leave a voicemail      *97  check voicemail (beeps)"
    echo "  *5901..*5999  park / pick up a parked call   **<ext>  answer a ringing phone"
  fi
  echo
  echo "Call records  : sudo -u postgres psql $DB -c \"select start_stamp, caller_id_number, destination_number, billsec, hangup_cause, status from v_xml_cdr order by start_stamp desc limit 10\""
  echo "Registrations : rq_cli -p $ESLPW -x \"show registrations\""
  echo "Live log      : sudo journalctl -u ringqswitch -f"
} > "$summary"
chmod 600 "$summary"
echo
echo "=================================================================="
cat "$summary"
echo "=================================================================="
echo "(saved in $summary)"
