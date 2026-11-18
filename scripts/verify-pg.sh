#!/usr/bin/env bash
# make verify-pg 의 본체(B37 10절). 선택 타깃이며 make verify 에 포함하지 않는다.
#
# 로컬에 설치된 PostgreSQL 로 임시 클러스터(initdb + 유닉스 소켓)를 만들어 PG 용 Go 시험을 돌린다.
# Docker 를 쓰지 않고, 이미 떠 있는 PostgreSQL(예: brew 서비스 5432)은 읽지도 쓰지도 않는다
# (새 클러스터는 임시 디렉터리의 유닉스 소켓만 열고 TCP 는 열지 않는다).
#
# PG 실행 파일을 못 찾으면 "건너뜀"을 표시하고 0 으로 끝난다. PL_VERIFY_PG=required 이면 실패(1)한다.
# 찾는 순서: PL_PG_BIN(디렉터리) → PATH 의 pg_ctl → brew --prefix postgresql@* → /usr/lib/postgresql/*/bin
# 시험 범위: 기본은 전체(./...)를 PL_TEST_DB=pg 로 한 번 더 돈다(internal/dbtest 가 PG 데이터베이스를 준다). 목록은 PL_VERIFY_PG_PKGS 로 바꾼다.
# 출력: 패키지별 통과·실패·건너뜀 표. 실패·건너뜀 시험 목록은 PL_VERIFY_PG_REPORT(기본 임시 디렉터리 밖 build/verify-pg/) 아래 failures.tsv·skips.tsv 로 남긴다.
# 설정: PL_VERIFY_PG_RUN 은 go test -run 정규식(시험 하나만 PG 로 돌려 볼 때). PL_VERIFY_PG_P 로 패키지 병렬도를 바꾼다(기본 4, 임시 클러스터 max_connections 200 안).
set -u
cd "$(dirname "$0")/.."
unset GOROOT

PKGS="${PL_VERIFY_PG_PKGS:-./...}"
PAR="${PL_VERIFY_PG_P:-4}"
REPORT="${PL_VERIFY_PG_REPORT:-build/verify-pg}"
mkdir -p "$REPORT"

find_pg_bin() {
  if [ -n "${PL_PG_BIN:-}" ] && [ -x "$PL_PG_BIN/pg_ctl" ]; then echo "$PL_PG_BIN"; return 0; fi
  if command -v pg_ctl >/dev/null 2>&1; then dirname "$(command -v pg_ctl)"; return 0; fi
  if command -v brew >/dev/null 2>&1; then
    local f p
    for f in $(brew list --formula 2>/dev/null | grep '^postgresql@' | sort -r); do
      p="$(brew --prefix "$f" 2>/dev/null)/bin"
      if [ -x "$p/pg_ctl" ]; then echo "$p"; return 0; fi
    done
  fi
  local d
  for d in $(ls -d /usr/lib/postgresql/*/bin 2>/dev/null | sort -r); do
    if [ -x "$d/pg_ctl" ]; then echo "$d"; return 0; fi
  done
  return 1
}

summary() { # $1 결과(통과|실패|건너뜀) $2 비고
  echo
  echo "=== verify-pg 요약 ==="
  echo "  결과: $1"
  [ -n "${2:-}" ] && echo "  비고: $2"
}

BIN="$(find_pg_bin)" || BIN=""
if [ -z "$BIN" ]; then
  echo "건너뜀: PostgreSQL 이 설치되어 있지 않습니다(PL_PG_BIN 으로 bin 디렉터리를 지정할 수 있습니다)."
  if [ "${PL_VERIFY_PG:-}" = "required" ]; then
    summary 실패 "PL_VERIFY_PG=required 인데 PostgreSQL 을 찾지 못했습니다"
    exit 1
  fi
  summary 건너뜀 "PostgreSQL 없음"
  exit 0
fi

VER="$("$BIN/postgres" --version 2>/dev/null | head -1)"
# 유닉스 소켓 경로는 길이 제한(약 100자)이 있어 짧은 임시 경로를 쓴다.
T="$(mktemp -d "/tmp/plpg-XXXXXX")"
PORT=$((55000 + RANDOM % 5000))
cleanup() {
  "$BIN/pg_ctl" -D "$T/data" -m immediate stop >/dev/null 2>&1 || true
  rm -rf "$T"
}
trap cleanup EXIT INT TERM

echo "PostgreSQL: $VER ($BIN)"
echo "임시 클러스터: $T (유닉스 소켓만, 포트 번호 $PORT)"
if ! "$BIN/initdb" -D "$T/data" -A trust -U pl --locale=C -E UTF8 --no-sync >"$T/initdb.log" 2>&1; then
  cat "$T/initdb.log"
  summary 실패 "initdb 실패"
  exit 1
fi
OPTS="-c listen_addresses= -c unix_socket_directories=$T -c port=$PORT -c fsync=off -c synchronous_commit=off -c full_page_writes=off -c max_connections=200 -c logging_collector=off"
if ! "$BIN/pg_ctl" -D "$T/data" -o "$OPTS" -l "$T/pg.log" -w -t 60 start >"$T/start.log" 2>&1; then
  cat "$T/start.log" "$T/pg.log" 2>/dev/null
  summary 실패 "임시 클러스터를 시작하지 못했습니다"
  exit 1
fi

export PL_TEST_PG_URL="postgres://pl@/postgres?host=$T&port=$PORT&sslmode=disable"
export PL_TEST_PG_VERSION="$VER"
export PL_TEST_DB=pg
echo
echo "=== go test $PKGS (PostgreSQL, 패키지 병렬 $PAR) ==="
START=$(date +%s)
# shellcheck disable=SC2086
go test -json -count=1 -p "$PAR" ${PL_VERIFY_PG_RUN:+-run "$PL_VERIFY_PG_RUN"} $PKGS >"$T/test.json" 2>"$T/test.err"
RC=$?
cp "$T/test.json" "$REPORT/test.json" 2>/dev/null
python3 scripts/verify_pg_summary.py ${PL_VERIFY_PG_DETAIL:+--detail} --failures "$REPORT/failures.tsv" --skips "$REPORT/skips.tsv" <"$T/test.json"
SRC=$?
[ -s "$T/test.err" ] && { echo; echo "--- go test 표준 오류 ---"; head -40 "$T/test.err"; }
END=$(date +%s)
echo "  소요: $((END - START))초, 실패 시험 목록: $REPORT/failures.tsv, 건너뜀 목록: $REPORT/skips.tsv"
if [ "$RC" -eq 0 ] && [ "$SRC" -eq 0 ]; then
  summary 통과 "$VER"
  exit 0
fi
summary 실패 "go test 실패(위 표 참고)"
exit 1
