#!/usr/bin/env bash
# make verify-docker 의 본체(선택 타깃, make verify 에는 들어가지 않는다).
# docker build -> 컨테이너 기동 -> 헬스체크 -> 기본 설정에서 dev IdP 꺼짐 확인 -> 정리.
# 어떤 경로로 끝나도 컨테이너를 지운다(trap). 이미지는 남겨 두어 다음 빌드 캐시로 쓴다.
set -u
cd "$(dirname "$0")/.."

IMAGE="${PL_VERIFY_IMAGE:-procure-ledger:verify}"
NAME="procure-ledger-verify-$$"
FAILS=0

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  echo "FAIL: docker 데몬에 연결할 수 없습니다(설치·실행 확인)."
  exit 1
fi

cleanup() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

check() { # check <설명> <참|거짓 판정 명령...>
  local desc="$1"; shift
  if "$@"; then echo "  통과: $desc"; else echo "  FAIL: $desc"; FAILS=$((FAILS + 1)); fi
}

echo "=== [1/4] docker build ($IMAGE) ==="
if ! docker build -t "$IMAGE" .; then
  echo "FAIL: docker build 실패"
  exit 1
fi

echo "=== [2/4] 컨테이너 기동 ==="
# 환경변수는 이미지 기본값만 쓴다(PL_DEV_IDP 미설정 = 운영 기본). 호스트 포트는 비어 있는 것을 OS 가 고른다.
if ! docker run -d --name "$NAME" -p 127.0.0.1::8080 "$IMAGE" >/dev/null; then
  echo "FAIL: docker run 실패"
  exit 1
fi
PORT=$(docker port "$NAME" 8080/tcp | head -1 | sed 's/.*://')
BASE="http://127.0.0.1:$PORT"
echo "  컨테이너 $NAME, $BASE"

echo "=== [3/4] 헬스체크 ==="
# 이미지의 HEALTHCHECK(/app/server healthcheck) 가 healthy 가 될 때까지 최대 60초 기다린다.
STATUS=""
for _ in $(seq 1 60); do
  STATUS=$(docker inspect -f '{{if .State.Running}}{{.State.Health.Status}}{{else}}exited{{end}}' "$NAME" 2>/dev/null)
  [ "$STATUS" = "healthy" ] && break
  [ "$STATUS" = "exited" ] && break
  sleep 1
done
echo "  컨테이너 헬스 상태: $STATUS"
if [ "$STATUS" != "healthy" ]; then
  echo "--- 컨테이너 로그 ---"
  docker logs "$NAME" 2>&1 | tail -30
fi
check "컨테이너 HEALTHCHECK 가 healthy" test "$STATUS" = "healthy"
check "GET /healthz 가 ok" bash -c "curl -fsS $BASE/healthz | grep -q '\"status\":\"ok\"'"

echo "=== [4/4] 기본 설정에서 dev IdP 꺼짐 ==="
CFG=$(curl -fsS "$BASE/api/auth/config" 2>&1)
echo "  /api/auth/config: $CFG"
check "devIdp 가 false" bash -c "echo '$CFG' | grep -q '\"devIdp\": *false'"
DEV_CT=$(curl -s -o /dev/null -w '%{content_type}' "$BASE/dev-idp/authorize")
echo "  /dev-idp/authorize content-type: $DEV_CT"
check "/dev-idp/authorize 가 IdP 응답이 아님(SPA html)" bash -c "echo '$DEV_CT' | grep -qi 'text/html'"
ME=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/me")
check "미인증 /api/me 가 401 ($ME)" test "$ME" = "401"
check "컨테이너 로그에 dev IdP 경고가 없음" bash -c "! docker logs $NAME 2>&1 | grep -q 'dev IdP'"

if [ "$FAILS" -gt 0 ]; then
  echo "결과: 실패 ($FAILS 건)"
  exit 1
fi
echo "결과: verify-docker 통과 (정리: 컨테이너 $NAME 삭제)"
