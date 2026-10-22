#!/usr/bin/env bash
# Interoperability test: drives a real ringqswitch process with an
# independent SIP traffic generator (the sipp command of the sip-tester
# package: apt install sip-tester).
#
#   test/interop/run.sh            run every scenario
#   CALLS=200 test/interop/run.sh  also run a 200 call load test
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
HERE="$ROOT/test/interop"
WORK="$(mktemp -d)"
PORT=5070
PASS="interop-pass"
ESL_PASS="interop-esl"
CALLS="${CALLS:-0}"
trap 'kill $RQ_PID 2>/dev/null || true; wait $RQ_PID 2>/dev/null || true; rm -rf "$WORK"' EXIT

command -v sipp >/dev/null || { echo "the SIP traffic generator is not installed (apt install sip-tester)"; exit 2; }

mkdir -p "$WORK/conf"
cp -r "$ROOT/conf/directory" "$ROOT/conf/dialplan" "$WORK/conf/"
cat > "$WORK/conf/ringqswitch.xml" <<EOF
<configuration>
  <vars>
    <var name="domain" value="127.0.0.1"/>
    <var name="default_password" value="$PASS"/>
    <var name="beep" value="%(100,0,1000);loops=1"/>
    <var name="busy" value="%(500,500,480,620)"/>
  </vars>
  <settings>
    <param name="log-level" value="${RQ_LOG_LEVEL:-warn}"/>
    <param name="sip-ip" value="127.0.0.1"/>
    <param name="sip-port" value="$PORT"/>
    <param name="rtp-ip" value="127.0.0.1"/>
    <param name="rtp-start-port" value="36000"/>
    <param name="rtp-end-port" value="38000"/>
    <param name="event-socket-listen" value="127.0.0.1:8031"/>
    <param name="event-socket-password" value="$ESL_PASS"/>
    <param name="cdr-csv" value="$WORK/cdr.csv"/>
    <param name="max-sessions" value="2000"/>
  </settings>
</configuration>
EOF

(cd "$ROOT" && go build -o "$WORK/ringqswitch" ./cmd/ringqswitch && go build -o "$WORK/rq_cli" ./cmd/rq_cli)
# Ports this test needs must be free (another PBX on the box would collide).
if command -v ss >/dev/null && ss -lun | grep -q ":$PORT "; then
  echo "port $PORT/udp is already in use; stop the other service or edit PORT in $0"; exit 2
fi
"$WORK/ringqswitch" -conf "$WORK/conf" 2> "$WORK/ringqswitch.log" &
RQ_PID=$!
sleep 1

cli() { "$WORK/rq_cli" -P 8031 -p "$ESL_PASS" -x "$1"; }
pass=0; fail=0
check() { # name, command...
  local name="$1"; shift
  if "$@" > "$WORK/$name.out" 2>&1; then
    echo "PASS  $name"; pass=$((pass+1))
  else
    echo "FAIL  $name"; fail=$((fail+1)); tail -20 "$WORK/$name.out"
  fi
}
GEN=(sipp 127.0.0.1:$PORT -i 127.0.0.1 -m 1 -timeout 20s -timeout_error -nostdin -trace_err -error_file /dev/stderr)

# 1. Registration over UDP and TCP (digest auth).
check register_udp "${GEN[@]}" -sf "$HERE/register.xml" -s 1003 -au 1003 -ap "$PASS" -p 5091
check register_tcp "${GEN[@]}" -t t1 -sf "$HERE/register.xml" -s 1002 -au 1002 -ap "$PASS" -p 5092

# 2. Authenticated calls to switch applications over UDP and TCP.
check echo_udp "${GEN[@]}" -sf "$HERE/uac_auth.xml" -s 9196 -au 1000 -ap "$PASS" -set caller 1000 -p 5093
check milliwatt_tcp "${GEN[@]}" -t t1 -sf "$HERE/uac_auth.xml" -s 9197 -au 1000 -ap "$PASS" -set caller 1000 -p 5094

# 3. Extension to extension: the generator registers 1001 from port 5090,
#    then answers there while a second generator calls 1001 through the
#    switch.
check register_1001 "${GEN[@]}" -sf "$HERE/register.xml" -s 1001 -au 1001 -ap "$PASS" -p 5090
sipp 127.0.0.1:$PORT -i 127.0.0.1 -p 5090 -sn uas -m 1 -timeout 25s -timeout_error -nostdin > "$WORK/uas.out" 2>&1 &
UAS_PID=$!
sleep 0.5
check bridge_call "${GEN[@]}" -sf "$HERE/uac_auth.xml" -s 1001 -au 1000 -ap "$PASS" -set caller 1000 -p 5095
check bridge_callee wait $UAS_PID
check channels_cleared bash -c "[ \"\$($WORK/rq_cli -P 8031 -p $ESL_PASS -x 'show channels' | tail -1)\" = '0 total.' ]"

# 4. Optional load test through the bridge path.
if [ "$CALLS" -gt 0 ]; then
  check register_1001_load "${GEN[@]}" -sf "$HERE/register.xml" -s 1001 -au 1001 -ap "$PASS" -p 5090
  sipp 127.0.0.1:$PORT -i 127.0.0.1 -p 5090 -sn uas -m "$CALLS" -timeout 180s -timeout_error -nostdin > "$WORK/uas_load.out" 2>&1 &
  UAS_PID=$!
  sleep 0.5
  check "load_${CALLS}_calls" sipp 127.0.0.1:$PORT -i 127.0.0.1 -p 5096 -sf "$HERE/uac_auth.xml" -s 1001 -au 1000 -ap "$PASS" \
    -set caller 1000 -m "$CALLS" -r 20 -l 100 -timeout 180s -timeout_error -nostdin
  check "load_${CALLS}_callee" wait $UAS_PID
  sleep 0.5
  check load_channels_cleared bash -c "[ \"\$($WORK/rq_cli -P 8031 -p $ESL_PASS -x 'show channels' | tail -1)\" = '0 total.' ]"
fi

echo
cli "show registrations" || true
echo "CDR lines: $(($(wc -l < "$WORK/cdr.csv") - 1))"
echo "CDR hangup causes: $(tail -n +2 "$WORK/cdr.csv" | awk -F, '{print $12}' | sort | uniq -c | tr '\n' ' ')"
echo "$pass passed, $fail failed"
if [ "$fail" -gt 0 ]; then
  echo "--- ringqswitch log (tail) ---"; tail -40 "$WORK/ringqswitch.log"
  exit 1
fi
