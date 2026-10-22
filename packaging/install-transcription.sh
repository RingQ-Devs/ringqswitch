#!/usr/bin/env bash
# Install the speech recogniser for Call transcription (Settings → Call
# transcription): rq-speech (transcripts after calls), rq-speech-server
# (live transcription during calls) and a speech model. Everything runs on
# this server; no call audio leaves it. The recogniser is an open-source
# program built from source here (see THIRD-PARTY-NOTICES.md).
#
#   sudo bash packaging/install-transcription.sh
#       Downloads and builds the recogniser (about 5 minutes), installs
#       /usr/local/bin/rq-speech and /usr/local/bin/rq-speech-server and
#       downloads a model to
#       /usr/local/share/ringqswitch/speech/: "small" (466 MB, more
#       accurate) on a server with 4 or more CPUs and 4 GB of memory,
#       "base" (142 MB, faster) otherwise.
#
#   sudo bash packaging/install-transcription.sh --model small
#       Chooses the model: base, small or medium (1.5 GB, most accurate,
#       slow without a fast CPU); base.en / small.en for English only.
#       Run it again to change the model: RingQswitch uses the largest one
#       installed (or the one set with transcribe-model in ringqswitch.xml)
#       after calls, and the fastest good one (base) for live transcription
#       (base is downloaded too when the model is larger); both can be
#       chosen per domain in Settings → Call transcription.
#       Running it again also adds rq-speech-server to an earlier install.
#
#   Without internet access from the server:
#     --source DIR      the recogniser's source tree copied to the server
#     --model-file FILE a ggml-*.bin model copied to the server
#
# RingQswitch finds the recogniser within a minute: no restart needed.
set -euo pipefail

# Where the recogniser's source and the speech models are downloaded from.
SPEECH_REPO="${SPEECH_REPO:-https://github.com/ggml-org/whisper.cpp}"
SPEECH_REF="${SPEECH_REF:-v1.8.5}"
MODEL_URL="${MODEL_URL:-https://huggingface.co/ggerganov/whisper.cpp/resolve/main}"
BIN=/usr/local/bin/rq-speech
SERVER_BIN=/usr/local/bin/rq-speech-server
MODELS=/usr/local/share/ringqswitch/speech
OLD_MODELS=/usr/local/share/ringqswitch/whisper   # before 0.10.1
SRCDIR=/opt/ringqswitch-speech
[ -d /opt/whisper.cpp ] && [ ! -e "$SRCDIR" ] && SRCDIR=/opt/whisper.cpp   # an earlier install's source

MODEL=""
SOURCE=""
MODEL_FILE=""
die() { echo "error: $*" >&2; exit 1; }
while [ $# -gt 0 ]; do
  case "$1" in
    --model) MODEL="${2:-}"; shift 2 ;;
    --source) SOURCE="${2:-}"; shift 2 ;;
    --model-file) MODEL_FILE="${2:-}"; shift 2 ;;
    -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
    *) die "unknown option $1 (see --help)" ;;
  esac
done
[ "$(id -u)" -eq 0 ] || die "run as root: sudo bash $0"

if [ -n "$MODEL_FILE" ]; then
  [ -f "$MODEL_FILE" ] || die "$MODEL_FILE not found"
  if [ -z "$MODEL" ]; then
    MODEL="$(basename "$MODEL_FILE" .bin)"
    MODEL="${MODEL#ggml-}"
  fi
fi
if [ -z "$MODEL" ]; then
  CPUS="$(nproc)"
  MEM_KB="$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)"
  if [ "$CPUS" -ge 4 ] && [ "$MEM_KB" -ge 3800000 ]; then MODEL=small; else MODEL=base; fi
  echo "model: $MODEL ($CPUS CPUs, $((MEM_KB / 1024)) MB memory; --model base|small|medium to choose)"
fi
[[ "$MODEL" =~ ^(tiny|base|small|medium|large-v3-turbo)(\.en)?$ ]] || die "--model: base, small or medium (or base.en, small.en); with --model-file, the model's name"

# ---- the recogniser
need=()
for t in cmake make g++ git curl; do command -v "$t" >/dev/null 2>&1 || need+=("$t"); done
if [ ${#need[@]} -gt 0 ]; then
  if command -v apt-get >/dev/null 2>&1; then
    echo "installing the build tools (apt): build-essential cmake git curl..."
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq build-essential cmake git curl ca-certificates >/dev/null
  elif command -v dnf >/dev/null 2>&1; then
    echo "installing the build tools (dnf): gcc-c++ make cmake git curl..."
    dnf install -y -q gcc-c++ make cmake git curl
  else
    die "install ${need[*]} first"
  fi
fi

if [ -n "$SOURCE" ]; then
  [ -f "$SOURCE/CMakeLists.txt" ] && grep -q 'whisper.cpp' "$SOURCE/CMakeLists.txt" || die "$SOURCE is not the speech recogniser's source tree"
  SRC="$(cd "$SOURCE" && pwd)"
else
  SRC="$SRCDIR"
  if [ -f "$SRC/CMakeLists.txt" ] && [ ! -d "$SRC/.git" ]; then
    echo "using the speech recogniser's source in $SRC"
  elif [ -d "$SRC/.git" ]; then
    echo "updating the speech recogniser in $SRC ($SPEECH_REF)..."
    git -C "$SRC" fetch -q --depth 1 origin "refs/tags/$SPEECH_REF:refs/tags/$SPEECH_REF" 2>/dev/null \
      && git -C "$SRC" checkout -q "$SPEECH_REF" \
      || echo "  (kept the version already there)"
  else
    echo "downloading the speech recogniser $SPEECH_REF to $SRC..."
    rm -rf "$SRC.part"
    if ! git clone -q --depth 1 --branch "$SPEECH_REF" "$SPEECH_REPO" "$SRC.part" 2>/dev/null; then
      echo "  ($SPEECH_REF not found: using the latest version)"
      rm -rf "$SRC.part"
      git clone -q --depth 1 "$SPEECH_REPO" "$SRC.part" || die "could not download $SPEECH_REPO (copy the recogniser's source tree to the server and use --source DIR)"
    fi
    mv "$SRC.part" "$SRC"
  fi
fi

echo "building the speech recogniser (a few minutes)..."
BUILD="$SRC/build-ringq"
JOBS="$(nproc)"
cmake -S "$SRC" -B "$BUILD" -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
  -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_EXAMPLES=ON -DWHISPER_BUILD_SERVER=ON -DWHISPER_SDL2=OFF >"$BUILD.log" 2>&1 \
  || { tail -20 "$BUILD.log"; die "cmake failed (log: $BUILD.log)"; }
cmake --build "$BUILD" --config Release -j "$JOBS" --target whisper-cli >>"$BUILD.log" 2>&1 \
  || { tail -20 "$BUILD.log"; die "the build failed (log: $BUILD.log)"; }
install -m 0755 "$BUILD/bin/whisper-cli" "$BIN"
echo "installed $BIN"
# rq-speech-server keeps the model loaded, so live transcription keeps up
# with the conversation. Without it, live transcription still works with
# rq-speech, a few seconds late.
if cmake --build "$BUILD" --config Release -j "$JOBS" --target whisper-server >>"$BUILD.log" 2>&1 && [ -x "$BUILD/bin/whisper-server" ]; then
  install -m 0755 "$BUILD/bin/whisper-server" "$SERVER_BIN"
  echo "installed $SERVER_BIN"
else
  SERVER_BIN=""
  echo "note: rq-speech-server could not be built (log: $BUILD.log): live transcription uses rq-speech instead (slower)"
fi

# ---- the speech model
# Models of an earlier install move to the new folder; the old path stays
# valid (a link) for a transcribe-model setting that names it.
if [ -d "$OLD_MODELS" ] && [ ! -L "$OLD_MODELS" ] && [ ! -e "$MODELS" ]; then
  mv "$OLD_MODELS" "$MODELS" && ln -s speech "$OLD_MODELS"
  echo "moved the speech models to $MODELS"
fi
mkdir -p "$MODELS"
chmod 0755 "$MODELS" "$(dirname "$MODELS")"
DEST="$MODELS/ggml-$MODEL.bin"
if [ -n "$MODEL_FILE" ]; then
  install -m 0644 "$MODEL_FILE" "$DEST.part"
elif [ -s "$DEST" ]; then
  echo "model $DEST is already there"
else
  echo "downloading the $MODEL speech model..."
  curl -fL --retry 3 --progress-bar -o "$DEST.part" "$MODEL_URL/ggml-$MODEL.bin" \
    || { rm -f "$DEST.part"; die "could not download $MODEL_URL/ggml-$MODEL.bin (download it elsewhere and use --model-file FILE)"; }
fi
CHECK="$DEST"
if [ -f "$DEST.part" ]; then
  # A ggml model starts with "lmgg"; an error page does not.
  [ "$(head -c 4 "$DEST.part")" = "lmgg" ] && [ "$(stat -c %s "$DEST.part")" -gt 10000000 ] \
    || { rm -f "$DEST.part"; die "that is not a speech model (ggml-*.bin)"; }
  chmod 0644 "$DEST.part"
  CHECK="$DEST.part"
fi

# ---- check
if [ -f "$SRC/samples/jfk.wav" ]; then
  echo "checking: transcribing the recogniser's sample..."
  ERR="$(mktemp)"
  OUT="$(timeout 300 "$BIN" -m "$CHECK" -f "$SRC/samples/jfk.wav" -l en -np -nt -t "$(( JOBS < 4 ? JOBS : 4 ))" 2>"$ERR" | tr -s ' \n' ' ')" \
    || { tail -5 "$ERR"; rm -f "$ERR" "$DEST.part"; die "rq-speech could not run with this model"; }
  rm -f "$ERR"
  echo "  heard:$OUT"
  case "$OUT" in *[Cc]ountry*) ;; *) rm -f "$DEST.part"; die "rq-speech did not recognise the sample";; esac
fi
if [ -f "$DEST.part" ]; then
  mv -f "$DEST.part" "$DEST"
  echo "installed $DEST"
fi

# ---- live transcription keeps up with a conversation best with Base
# (Small and larger answer more slowly): downloaded too when the model
# above is larger.
LIVE_MODEL="$MODEL"
case "$MODEL" in
  tiny*|base*) ;;
  *)
    LIVE_DEST="$MODELS/ggml-base.bin"
    if [ -s "$LIVE_DEST" ]; then
      LIVE_MODEL=base
    elif [ -z "$MODEL_FILE" ]; then
      echo "downloading the base model for live transcription (142 MB)..."
      if curl -fL --retry 3 --progress-bar -o "$LIVE_DEST.part" "$MODEL_URL/ggml-base.bin" \
        && [ "$(head -c 4 "$LIVE_DEST.part")" = "lmgg" ] && [ "$(stat -c %s "$LIVE_DEST.part")" -gt 10000000 ]; then
        chmod 0644 "$LIVE_DEST.part"
        mv -f "$LIVE_DEST.part" "$LIVE_DEST"
        echo "installed $LIVE_DEST"
        LIVE_MODEL=base
      else
        rm -f "$LIVE_DEST.part"
        echo "note: the base model could not be downloaded: live transcription uses $MODEL (slower); download Base later in Settings → Call transcription"
      fi
    fi
    ;;
esac

CFG=/etc/ringqswitch/ringqswitch.xml
if [ -f "$CFG" ] && grep -Eq '^[[:space:]]*<param name="transcribe-model"' "$CFG"; then
  echo "note: $CFG sets transcribe-model; RingQswitch uses that model, not $DEST"
fi
cat <<EOF

Call transcription is ready to use:
  recogniser  $BIN
  live        ${SERVER_BIN:-rq-speech (rq-speech-server could not be built)}, model $LIVE_MODEL
  model       $DEST
Open the web interface → Settings → Call transcription (it shows "Ready"
within a minute) and turn on "Transcribe calls" and, for text in the
dialer while calls go on, "Live transcription during calls".
EOF
