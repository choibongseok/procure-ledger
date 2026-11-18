#!/usr/bin/env bash
# 실제 ProcureLedger 서버(bin/server, 개발 모드)가 보내는 서명된 알림을 이 수신기가 받는지 확인한다.
#
# 하는 일: 서버 기동 -> 연동 API 켜기·토큰 발급·알림 구독 등록 -> 수신기 기동 -> 거래처 등록 -> 수신기 상태에 반영되는지 확인
#         -> 서명 비밀 교체 뒤에도(새 비밀만 가진 수신기로) 다음 알림이 받아지는지 확인 -> 시험 알림(ping) 확인.
# 준비: 저장소 루트에서 `make build` (bin/server), jq, curl, node 22 이상. 이 폴더에서 `npm ci` 와 `npm run build`.
# 실행: 저장소 루트에서 `make webhook-nest-live`.   종료 코드: 0 전부 통과 / 1 실패 있음.
set -u
cd "$(dirname "$0")/../../.."
ROOT="$(pwd)"
APP="$ROOT/examples/webhook-consumer-nest"
BIN="$ROOT/bin/server"
command -v jq >/dev/null || { echo "jq 가 필요합니다"; exit 1; }
[ -x "$BIN" ] || { echo "bin/server 가 없습니다. 먼저 make build"; exit 1; }
[ -f "$APP/dist/main.js" ] || { echo "수신기 빌드가 없습니다. examples/webhook-consumer-nest 에서 npm run build"; exit 1; }

PL_PORT="${LIVE_PL_PORT:-$((20000 + RANDOM % 10000))}"
RX_PORT="${LIVE_RX_PORT:-$((30000 + RANDOM % 10000))}"
PL="http://127.0.0.1:${PL_PORT}"
RX="http://127.0.0.1:${RX_PORT}"
WORK="$(mktemp -d)"
PL_PID=""; RX_PID=""; FAIL=0

cleanup() {
  [ -n "$RX_PID" ] && kill "$RX_PID" 2>/dev/null && wait "$RX_PID" 2>/dev/null
  [ -n "$PL_PID" ] && kill "$PL_PID" 2>/dev/null && wait "$PL_PID" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

pass() { echo "  통과    $1"; }
fail() { echo "  실패    $1 — $2"; FAIL=$((FAIL + 1)); }

# 개발용 IdP 로 로그인하고 세션 쿠키·CSRF 토큰을 저장한다.
login_as() {
  curl -sS -L -o /dev/null -c "$WORK/$1.jar" -b "$WORK/$1.jar" "$PL/api/auth/login?login_hint=$2" || return 1
  curl -sS -b "$WORK/$1.jar" "$PL/api/me" | jq -r '.csrfToken // empty' >"$WORK/$1.csrf"
  [ -s "$WORK/$1.csrf" ]
}
# call <계정> <METHOD> <경로> [json]: 본문은 $WORK/body, 상태 코드는 $CODE.
call() {
  local args=(-sS -o "$WORK/body" -w '%{http_code}' -X "$2" -b "$WORK/$1.jar" -H "X-CSRF-Token: $(cat "$WORK/$1.csrf")" -H "Origin: $PL")
  [ -n "${4:-}" ] && args+=(-H 'Content-Type: application/json' --data "$4")
  CODE=$(curl "${args[@]}" "$PL$3") || CODE=000
}
jget() { jq -r "$1" "$WORK/body" 2>/dev/null; }

start_receiver() { # <현재 비밀> [옛 비밀]
  [ -n "$RX_PID" ] && { kill "$RX_PID" 2>/dev/null; wait "$RX_PID" 2>/dev/null; }
  PORT="$RX_PORT" PL_API_BASE_URL="$PL" PL_API_TOKEN="$API_TOKEN" PL_WEBHOOK_SECRET="$1" PL_WEBHOOK_SECRET_PREVIOUS="${2:-}" \
    PL_INITIAL_CURSOR=start node "$APP/dist/main.js" >>"$WORK/receiver.log" 2>&1 &
  RX_PID=$!
  for _ in $(seq 1 50); do curl -fsS "$RX/healthz" >/dev/null 2>&1 && return 0; sleep 0.2; done
  cat "$WORK/receiver.log"; return 1
}

# 수신기 상태에 vendor <번호> 가 나타날 때까지 기다린다(발송 주기·합치기 창 때문에 수 초 걸린다).
wait_vendor() {
  for _ in $(seq 1 90); do
    if curl -fsS "$RX/state" | jq -e --argjson id "$1" '.objects[] | select(.resource=="vendor" and .id==$id)' >/dev/null 2>&1; then return 0; fi
    sleep 0.5
  done
  return 1
}

echo "수신기 실서버 확인 (서버 $PL, 수신기 $RX)"
PL_NOTIFY_INSECURE_LOCAL=1 PL_ADDR="127.0.0.1:${PL_PORT}" PL_BASE_URL="$PL" PL_DB_PATH="$WORK/live.db" PL_DEV_IDP=1 \
  PL_ACCOUNT_SECRET_KEY="MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" \
  PL_AUDIT_ANCHOR_KEY="live-anchor-key-0123456789abcdef" PL_AUDIT_ANCHOR_PATH="$WORK/anchor.log" PL_AUDIT_ANCHOR_INTERVAL=1h \
  "$BIN" >"$WORK/server.log" 2>&1 &
PL_PID=$!
for _ in $(seq 1 300); do curl -fsS "$PL/healthz" >/dev/null 2>&1 && break; kill -0 "$PL_PID" 2>/dev/null || { cat "$WORK/server.log"; exit 1; }; sleep 0.2; done
curl -fsS "$PL/healthz" >/dev/null 2>&1 && pass "서버 기동" || { fail "서버 기동" "응답 없음"; exit 1; }

login_as admin admin@pl.test && login_as finance finance@pl.test && pass "개발용 로그인(관리자·재무)" || { fail "로그인" "세션 실패"; exit 1; }

call admin PUT /api/admin/integrations/settings '{"enabled":true}'
[ "$CODE" = 204 ] && pass "연동 API 켜기" || fail "연동 API 켜기" "HTTP $CODE"
call admin POST /api/admin/integrations/tokens '{"name":"수신기 예제","scopes":["changes:read","vendors:read"],"days":1}'
API_TOKEN=$(jget .token)
case "$API_TOKEN" in pli_*) pass "연동 토큰 발급(changes:read)" ;; *) fail "연동 토큰 발급" "HTTP $CODE"; exit 1 ;; esac

call admin POST /api/admin/integrations/webhooks "{\"name\":\"수신기 예제\",\"url\":\"$RX/webhooks/procure-ledger\",\"events\":[\"vendor.created\",\"vendor.updated\"],\"includeApi\":false}"
WH_ID=$(jget .subscription.id); SECRET_1=$(jget .secret)
case "$SECRET_1" in whsec_*) pass "알림 구독 등록(비밀 whsec_…)" ;; *) fail "알림 구독 등록" "HTTP $CODE $(head -c 200 "$WORK/body")"; exit 1 ;; esac

start_receiver "$SECRET_1" && pass "수신기 기동" || { fail "수신기 기동" "응답 없음"; exit 1; }

call admin POST "/api/admin/integrations/webhooks/$WH_ID/test" '{}'
if [ "$CODE" = 200 ] && [ "$(jget .ok)" = true ]; then pass "시험 알림(ping)을 수신기가 2xx 로 받음"; else fail "시험 알림(ping)" "HTTP $CODE $(head -c 200 "$WORK/body")"; fi

call finance POST /api/vendors '{"name":"라이브상사","businessNo":"123-45-67890","accountNo":"110-123-456789","phone":"010-1234-5678","email":"live@example.test"}'
V1=$(jget .id)
if [ "$CODE" = 201 ] && wait_vendor "$V1"; then
  pass "거래처 $V1 등록 -> 서명된 알림 수신 -> 피드 재조회로 상태 반영"
else
  fail "거래처 알림 수신" "HTTP $CODE, 수신기 상태에 없음"; tail -5 "$WORK/receiver.log"
fi

call admin POST "/api/admin/integrations/webhooks/$WH_ID/rotate-secret" '{}'
SECRET_2=$(jget .secret)
if [ "$CODE" = 200 ] && [ "$SECRET_2" != "$SECRET_1" ]; then pass "서명 비밀 교체"; else fail "서명 비밀 교체" "HTTP $CODE"; fi
# 교체 뒤 24시간은 서버가 새·옛 서명을 함께 보낸다. 새 비밀만 가진 수신기로 바꿔도 받아야 한다.
start_receiver "$SECRET_2" && pass "새 비밀만 가진 수신기 재기동" || fail "수신기 재기동" "응답 없음"

call finance POST /api/vendors '{"name":"교체후상사","businessNo":"123-45-67891","accountNo":"110-123-456790","phone":"010-1234-5679","email":"live2@example.test"}'
V2=$(jget .id)
if [ "$CODE" = 201 ] && wait_vendor "$V2"; then
  pass "비밀 교체 뒤 거래처 $V2 알림도 수신(새 서명 검증)"
else
  fail "비밀 교체 뒤 알림 수신" "HTTP $CODE"; tail -5 "$WORK/receiver.log"
fi

call admin GET "/api/admin/integrations/webhook-deliveries?subscription=$WH_ID"
echo "  발송 기록(서버): $(jq -c '[.[]? | {status, code: .last_code}] | group_by(.status) | map({(.[0].status): length}) | add' "$WORK/body" 2>/dev/null)"
echo "  수신기 상태: $(curl -fsS "$RX/state" | jq -c '{cursor, objects: [.objects[] | "\(.resource)#\(.id)@\(.lastSeq)"]}')"

echo
if [ "$FAIL" -eq 0 ]; then echo "결과: 전체 통과"; else echo "결과: 실패 $FAIL 건"; exit 1; fi
