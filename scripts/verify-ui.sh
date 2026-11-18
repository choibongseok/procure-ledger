#!/usr/bin/env bash
# 브라우저 화면 스모크(B14). `make verify-ui` 의 본체이며 `make verify` 에는 들어 있지 않다.
#
# make build 산출물(bin/server)을 dev IdP 를 켠 채 임시 DB 로 띄우고, 헤드리스 Chromium(Playwright)으로 화면을 돈다.
# 스크린샷은 UI_SHOT_DIR(기본 ui-screenshots/, gitignore)에 단계 번호 이름으로 남는다. CI 는 이 폴더를 아티팩트로 올린다.
# 브라우저 바이너리는 `npx playwright install chromium` 으로 받는다(이미 받았으면 건너뜀). 내려받기가 막힌 환경이면
# 이 단계에서 실패하고 이유를 출력한다. 리눅스 CI 는 UI_INSTALL_DEPS=1 로 시스템 라이브러리까지 설치한다.
# 반복 개발용: UI_SKIP_BUILD=1 이면 make build 를 건너뛴다(CI 에서는 쓰지 않는다).
# 종료 코드: 0 전부 통과 / 1 화면 스모크 실패 / 2 준비 단계(빌드·브라우저·서버 기동) 실패.
set -u
cd "$(dirname "$0")/.."
unset GOROOT

PORT="${UI_PORT:-$((30000 + RANDOM % 10000))}"
BASE="http://127.0.0.1:${PORT}"
WORK="$(mktemp -d)"
SERVER_PID=""
export UI_SHOT_DIR="${UI_SHOT_DIR:-$PWD/ui-screenshots}"

# 실패하면(종료 코드가 0 이 아니면) 임시 서버 로그를 build/verify-ui/ 로 보존한다(성공하면 임시 디렉터리와 함께 지운다). 화면 단계 실패를 서버 쪽 오류와 맞춰 볼 수 있게 한다.
cleanup() {
  local code=$?
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null && wait "$SERVER_PID" 2>/dev/null
  if [ "$code" != 0 ]; then
    mkdir -p build/verify-ui
    for f in server.log setup-server.log; do
      [ -f "$WORK/$f" ] && cp "$WORK/$f" "build/verify-ui/$f" 2>/dev/null
    done
    echo "서버 로그 보존: build/verify-ui/ (실패 원인 확인용)"
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

prep_fail() { echo "화면 스모크 준비 실패: $1"; exit 2; }

if [ "${UI_SKIP_BUILD:-}" != 1 ]; then
  echo "=== make build ==="
  make build >"$WORK/build.log" 2>&1 || { tail -30 "$WORK/build.log"; prep_fail "make build"; }
fi
[ -x bin/server ] || prep_fail "bin/server 가 없습니다(make build 필요)"
[ -d web/node_modules/playwright ] || npm --prefix web ci --no-audit --no-fund >"$WORK/npm.log" 2>&1 || { tail -20 "$WORK/npm.log"; prep_fail "npm ci"; }

echo "=== Chromium 준비 ==="
if [ "${UI_INSTALL_DEPS:-}" = 1 ]; then
  (cd web && npx playwright install --with-deps chromium) || prep_fail "Chromium 설치(--with-deps)"
else
  (cd web && npx playwright install chromium) || prep_fail "Chromium 내려받기(네트워크가 막혔거나 캐시가 없음)"
fi

rm -rf "$UI_SHOT_DIR"
mkdir -p "$UI_SHOT_DIR"

echo "=== 서버 기동(bin/server, PL_DEV_IDP=1) ==="
PL_NOTIFY_INSECURE_LOCAL=1 PL_ADDR="127.0.0.1:${PORT}" PL_DB_PATH="$WORK/ui.db" PL_DEV_IDP=1 PL_ACCOUNT_SECRET_KEY="MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" \
  PL_AUDIT_ANCHOR_KEY="ui-anchor-key-0123456789abcdef" PL_AUDIT_ANCHOR_PATH="$WORK/audit-anchor.log" PL_AUDIT_ANCHOR_INTERVAL=1h \
  bin/server >"$WORK/server.log" 2>&1 &
SERVER_PID=$!
up=0
for _ in $(seq 1 50); do
  if curl -fsS "$BASE/healthz" >/dev/null 2>&1; then up=1; break; fi
  kill -0 "$SERVER_PID" 2>/dev/null || { cat "$WORK/server.log"; prep_fail "서버가 바로 종료됨"; }
  sleep 0.2
done
[ "$up" = 1 ] || { cat "$WORK/server.log"; prep_fail "서버 헬스체크"; }

echo "=== 브라우저 스모크 ==="
(cd web && UI_BASE_URL="$BASE" node e2e-ui/smoke.mjs)
rc=$?
if [ "$rc" = 0 ]; then
  # 설치 마법사(B38): 빈 DB·dev IdP 없음으로 띄운 두 번째 서버에서 마법사 흐름을 돈다. 설치 코드는 PL_SETUP_CODE 로 정한다.
  echo "=== 설치 마법사 스모크(빈 DB 서버) ==="
  SETUP_PORT=$((PORT + 1))
  SETUP_CODE_VALUE="ui-setup-code-0123456789abcdef"
  PL_ADDR="127.0.0.1:${SETUP_PORT}" PL_DB_PATH="$WORK/setup.db" PL_SETUP_CODE="$SETUP_CODE_VALUE" PL_BASE_URL="http://127.0.0.1:${SETUP_PORT}" \
    bin/server >"$WORK/setup-server.log" 2>&1 &
  SETUP_PID=$!
  sup=0
  for _ in $(seq 1 50); do
    if curl -fsS "http://127.0.0.1:${SETUP_PORT}/healthz" >/dev/null 2>&1; then sup=1; break; fi
    sleep 0.2
  done
  if [ "$sup" = 1 ]; then
    (cd web && UI_BASE_URL="http://127.0.0.1:${SETUP_PORT}" UI_SETUP_CODE="$SETUP_CODE_VALUE" node e2e-ui/setup.mjs)
    rc=$?
    if grep -q "$SETUP_CODE_VALUE" "$WORK/setup-server.log"; then echo "실패: 서버 로그에 PL_SETUP_CODE 값이 있다"; rc=1; fi
  else
    cat "$WORK/setup-server.log"; rc=1
  fi
  kill "$SETUP_PID" 2>/dev/null; wait "$SETUP_PID" 2>/dev/null
  if [ "$rc" != 0 ]; then echo "--- 마법사 서버 로그(끝 20줄) ---"; tail -20 "$WORK/setup-server.log"; fi
fi
echo "스크린샷: $UI_SHOT_DIR ($(ls "$UI_SHOT_DIR" | wc -l | tr -d ' ')장)"
if [ "$rc" != 0 ]; then echo "--- 서버 로그(끝 20줄) ---"; tail -20 "$WORK/server.log"; fi
exit "$rc"
