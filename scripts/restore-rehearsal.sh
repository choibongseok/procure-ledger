#!/usr/bin/env bash
# 복구 리허설(B38, 명세 7.5).(초안), platform 확인.
#
# 최신 백업 세트(없으면 새로 만든다)를 임시 위치에 복구해 검증하고, 복구한 DB 로 서버를 임의 포트에 띄워 /healthz 를 확인한 뒤 모두 지운다.
# 운영 DB·앵커 파일은 읽기만 한다(리허설 결과 기록만 라이브 DB 의 backup_runs·감사에 남는다: 끄려면 REHEARSAL_NO_RECORD=1).
# 리허설 서버는 PL_REHEARSAL=1 로만 띄운다(알림 발송·주기 작업·앵커 기록 차단; 서버 쪽 모드 구현은 platform). 앵커 키·SMTP 는 넘기지 않는다.
#
# 사용: scripts/restore-rehearsal.sh [--dir 백업디렉터리]
# 환경: 서버와 같은 PL_DB_PATH 또는 PL_DB_URL, PL_AUDIT_ANCHOR_*, PL_BACKUP_DIR·PL_BACKUP_KEY. 바이너리: ERP_ADMIN(기본 bin/erp-admin), ERP_SERVER(기본 bin/server).
# 종료 코드: 0 성공 / 1 리허설 실패 / 2 준비 실패(바이너리 없음 등).
set -u
cd "$(dirname "$0")/.."
unset GOROOT

DIR_ARG=()
while [ $# -gt 0 ]; do
  case "$1" in
    --dir) [ $# -ge 2 ] || { echo "사용법: $0 [--dir 백업디렉터리]"; exit 2; }; DIR_ARG=(--dir "$2"); shift 2 ;;
    -h|--help) echo "사용법: $0 [--dir 백업디렉터리]"; exit 0 ;;
    *) echo "알 수 없는 인자: $1"; exit 2 ;;
  esac
done

ADMIN="${ERP_ADMIN:-bin/erp-admin}"
SERVER="${ERP_SERVER:-bin/server}"
[ -x "$ADMIN" ] || { echo "리허설 준비 실패: $ADMIN 이 없습니다(make build 필요)"; exit 2; }
[ -x "$SERVER" ] || { echo "리허설 준비 실패: $SERVER 가 없습니다(make build 필요)"; exit 2; }
[ -z "${PL_DB_URL:-}" ] || { echo "리허설 준비 실패: PostgreSQL 은 이 스크립트가 아니라 erp-admin restore --rehearse 로 직접 확인하세요(서버 기동 확인은 SQLite 만 지원)"; exit 2; }

WORK="$(mktemp -d)"
SERVER_PID=""
cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null && wait "$SERVER_PID" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

fail() { echo "복구 리허설 실패: $1"; exit 1; }

# 1) 최신 세트 선택(없으면 새 백업)
BACKUP_DIR="${PL_BACKUP_DIR:-}"
if [ "${#DIR_ARG[@]}" -gt 0 ]; then BACKUP_DIR="${DIR_ARG[1]}"; fi
if [ -z "$BACKUP_DIR" ]; then BACKUP_DIR="$(dirname "${PL_DB_PATH:-data/erp.db}")/backups"; fi
SET="$(ls -1d "$BACKUP_DIR"/pl-* 2>/dev/null | sort | tail -1)"
if [ -z "$SET" ]; then
  echo "=== 백업 세트가 없어 새로 만듭니다 ==="
  "$ADMIN" backup ${DIR_ARG[@]+"${DIR_ARG[@]}"} || fail "백업 생성"
  SET="$(ls -1d "$BACKUP_DIR"/pl-* 2>/dev/null | sort | tail -1)"
  [ -n "$SET" ] || fail "백업 세트를 찾을 수 없습니다"
fi
echo "=== 세트: $(basename "$SET") ==="

# 2) 세트 검증
"$ADMIN" verify-backup ${DIR_ARG[@]+"${DIR_ARG[@]}"} "$SET" || fail "verify-backup"

# 3) 임시 위치에 복구 리허설(표식 파일 .rehearsal 이 임시 디렉터리에 만들어진다)
REC=(--record)
[ -z "${REHEARSAL_NO_RECORD:-}" ] || REC=()
OUT="$WORK/restore.out"
"$ADMIN" restore "$SET" --rehearse --work "$WORK/rehearsal" ${REC[@]+"${REC[@]}"} ${DIR_ARG[@]+"${DIR_ARG[@]}"} >"$OUT" 2>&1; RC=$?
cat "$OUT"
[ $RC -eq 0 ] || fail "restore --rehearse (종료 코드 $RC)"
RDB="$(sed -n 's/^리허설 DB: //p' "$OUT" | head -1)"
[ -n "$RDB" ] && [ -f "$RDB" ] || fail "리허설 DB 를 찾을 수 없습니다"
[ -f "$WORK/rehearsal/.rehearsal" ] || fail "리허설 표식(.rehearsal)이 없습니다"

# 4) 복구한 DB 로 서버를 임의 포트에 띄워 확인한다(외부 발송·주기 작업·앵커 기록 없음)
PORT=$((30000 + RANDOM % 10000))
env -i PATH="$PATH" HOME="$WORK" PL_DB_PATH="$RDB" PL_ADDR="127.0.0.1:${PORT}" PL_BASE_URL="http://127.0.0.1:${PORT}" PL_REHEARSAL=1 \
  "$SERVER" >"$WORK/server.log" 2>&1 &
SERVER_PID=$!
OK=0
for _ in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then OK=1; break; fi
  kill -0 "$SERVER_PID" 2>/dev/null || break
  sleep 0.2
done
if [ $OK -ne 1 ]; then tail -20 "$WORK/server.log"; fail "리허설 서버가 /healthz 에 응답하지 않습니다"; fi
curl -fsS "http://127.0.0.1:${PORT}/api/auth/config" >/dev/null || fail "/api/auth/config 확인"
echo "서버 확인: /healthz·/api/auth/config 응답 정상"
kill "$SERVER_PID" 2>/dev/null; wait "$SERVER_PID" 2>/dev/null; SERVER_PID=""

# 5) 요약
echo "=== 복구 리허설 통과: $(basename "$SET") ==="
exit 0
