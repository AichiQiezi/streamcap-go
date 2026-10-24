#!/usr/bin/env bash
# End-to-end check of danmaku recording against a real live room (macOS).
#
#   scripts/e2e-danmaku.sh <live room url> [seconds] [--dump frames.bin]
#
# Builds the app, runs it as a separate, isolated instance (own port, data and save folders in a
# temp dir; the installed app is not touched), records the room for <seconds> (default 90) and
# reports the comments received and the .ass written. A small StreamCap window appears while it
# runs: the douyin page watcher needs the app's run loop, as in normal use.
# --dump also keeps every raw douyin frame (STREAMCAP_DANMAKU_DUMP), e.g. to refresh the fixture:
#   go test ./internal/platform/douyin -run TestRefreshFixture -fixture-from=frames.bin
set -euo pipefail
cd "$(dirname "$0")/.."

ROOM="${1:?usage: scripts/e2e-danmaku.sh <live room url> [seconds] [--dump frames.bin]}"
SECS="${2:-90}"
DUMP=""
if [[ "${3:-}" == "--dump" ]]; then DUMP="$(cd "$(dirname "${4:?--dump needs a file}")" && pwd)/$(basename "$4")"; fi
[[ "$(uname)" == Darwin ]] || { echo "macOS only (the douyin page watcher uses WKWebView)"; exit 1; }

PORT=6019
API="http://127.0.0.1:$PORT"
TMP="$(mktemp -d -t streamcap-e2e)"
echo "==> work dir $TMP"
mkdir -p "$TMP/data" "$TMP/videos"

echo "==> build"
(cd web && npm run build >/dev/null)
go build -o "$TMP/streamcap" ./cmd/streamcap 2>&1 | grep -v "duplicate libraries" || true
codesign --force --sign - "$TMP/streamcap" 2>/dev/null # cgo's ad-hoc signature is invalid until re-signed

STREAMCAP_DANMAKU_DUMP="$DUMP" "$TMP/streamcap" -port "$PORT" -data "$TMP/data" >"$TMP/stdout.log" 2>&1 &
APP=$!
cleanup() {
  curl -s -X POST -H 'Content-Type: application/json' -d '{}' "$API/api/app/quit" >/dev/null || true
  for _ in $(seq 1 60); do kill -0 "$APP" 2>/dev/null || break; sleep 0.5; done
  kill "$APP" 2>/dev/null || true
}
trap cleanup EXIT
for _ in $(seq 1 40); do curl -s "$API/api/state" >/dev/null && break; sleep 0.5; done
curl -s "$API/api/state" >/dev/null || { echo "app did not start"; tail -20 "$TMP/stdout.log"; exit 1; }

curl -s -X PUT -H 'Content-Type: application/json' "$API/api/settings" -d "{
  \"live_save_path\": \"$TMP/videos\", \"record_danmaku\": true, \"merge_sessions\": true, \"convert_to_mp4\": true,
  \"delete_original\": true, \"resume_wait_minutes\": \"0\", \"loop_time_seconds\": \"20\", \"system_notify_enabled\": false
}" >/dev/null
ID=$(curl -s -X POST -H 'Content-Type: application/json' "$API/api/recordings" \
  -d "[{\"url\": \"$ROOM\", \"monitor_status\": true, \"quality\": \"SD\", \"record_format\": \"TS\"}]" |
  python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["created"][0]["id"] if d["created"] else ""); [print("skipped:", s, file=sys.stderr) for s in d["skipped"]]')
[[ -n "$ID" ]] || exit 1

state() { curl -s "$API/api/state" | python3 -c 'import json,sys; print(json.load(sys.stdin)["recordings"][0]["state"])'; }
for _ in $(seq 1 60); do [[ "$(state)" == recording ]] && break; sleep 1; done
[[ "$(state)" == recording ]] || { echo "not recording (is the room live?): $(state)"; exit 1; }
echo "==> recording ${SECS}s from $(date +%H:%M:%S)"
sleep "$SECS"
curl -s -X POST -H 'Content-Type: application/json' -d '{"on": false}' "$API/api/recordings/$ID/record" >/dev/null

for _ in $(seq 1 120); do find "$TMP/videos" -name '*.ass' | grep -q . && break; sleep 1; done
sleep 2
MP4=$(find "$TMP/videos" -name '*.mp4' | head -1)
ASS=$(find "$TMP/videos" -name '*.ass' | head -1)
JSONL=$(find "$TMP/videos" -name '*.danmaku.jsonl' | head -1)
echo "==> result"
echo "video:     ${MP4:-none} ($(ffprobe -v error -show_entries format=duration -of default=nw=1:nk=1 "$MP4" 2>/dev/null || echo ?)s)"
echo "received:  $( [[ -n "$JSONL" ]] && wc -l <"$JSONL" | tr -d ' ' || echo 0) comments"
# Per 30 s of receipt: a feed that stops after the first burst shows as leading numbers then zeros.
[[ -s "$JSONL" ]] && python3 -c 'import json,sys
ts=[json.loads(l)["t"] for l in open(sys.argv[1])]; t0=min(ts); n=int(sys.argv[2])//30+1; b=[0]*n
for t in ts: b[min(n-1,(t-t0)//30000)]+=1
print("per 30s:  ", " ".join(map(str,b)))' "$JSONL" "$SECS"
echo "on screen: $( [[ -n "$ASS" ]] && grep -c '^Dialogue:' "$ASS" || echo 0) in ${ASS:-no .ass}"
[[ -n "$ASS" ]] && grep '^PlayRes' "$ASS" | tr '\n' ' ' && echo
[[ -n "$DUMP" ]] && echo "frames:    $DUMP ($(wc -c <"$DUMP" | tr -d ' ') bytes)"
# A binary under the temp dir counts as a dev run, so it logs to ./logs (gitignored) like `go run`.
grep -i "danmaku\|did not compile\|reloading\|cannot keep" logs/streamcap.log 2>/dev/null | grep -v DEBUG | tail -5 || true
[[ -n "$ASS" && -s "$JSONL" ]]
