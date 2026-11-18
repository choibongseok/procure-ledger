#!/usr/bin/env bash
# make verify 의 본체(spec 5절의 5개 단계 + 예제 시험 1개).
#
# 규칙
#  - 단계가 실패하면 FAIL, 아직 없는 단계는 "미구현"으로 표시한다(실패로 세지는 않지만 요약에서 따로 센다).
#  - 마지막 요약에서 FAIL 이나 미구현이 하나라도 있으면 0 이 아닌 코드(1)로 끝난다.
#  - 한 단계가 실패해도 나머지를 계속 돌려 전체 상황을 한 번에 보여 준다.
set -u
cd "$(dirname "$0")/.."
unset GOROOT # 셸에 남은 다른 버전의 GOROOT 가 툴체인과 어긋나는 사고 방지

NAMES=()
RESULTS=()
NOTES=()

record() { NAMES+=("$1"); RESULTS+=("$2"); NOTES+=("${3:-}"); }

# stage <이름> <명령...>: 명령을 돌려 통과/실패를 기록한다.
stage() {
  local name="$1"; shift
  echo
  echo "=== [$name] ==="
  if "$@"; then record "$name" "통과"; else record "$name" "실패" "종료 코드 $?"; fi
}

# unimplemented <이름> <사유>
unimplemented() {
  echo
  echo "=== [$1] ==="
  echo "미구현: $2"
  record "$1" "미구현" "$2"
}

# gofmt 가 고칠 파일이 있으면 실패하고 어떤 파일인지 출력한다(B11 ③). 고치려면 `gofmt -w <파일>`.
gofmt_check() {
  local bad
  bad=$(find . -name '*.go' -not -path './web/*' -not -path './node_modules/*' -print0 | xargs -0 gofmt -l)
  if [ -n "$bad" ]; then
    echo "gofmt 로 정리해야 하는 파일이 있습니다:"
    printf '  %s\n' $bad
    return 1
  fi
}

stage1() { go vet ./... && go test ./... && gofmt_check; }

# 라우트·MCP 도구 권한 선언 누락 검사. 테스트 이름에 PermissionDeclaration 을 포함하는 규약을 쓴다.
# 매칭되는 테스트가 하나도 없으면 검사가 사라진 것이므로 실패로 본다.
stage2() {
  local out
  out=$(go test ./... -run 'PermissionDeclaration' -count=1 -v 2>&1) || { echo "$out"; return 1; }
  local n
  n=$(printf '%s\n' "$out" | grep -c '^--- PASS: TestPermissionDeclaration' || true)
  printf '%s\n' "$out" | grep -E '^(--- (PASS|FAIL)|ok|FAIL)' || true
  if [ "$n" -lt 2 ]; then echo "권한 선언 검사 테스트가 $n 개뿐입니다(2개 이상 필요)"; return 1; fi
}

stage3() {
  [ -d web/node_modules ] || npm --prefix web ci --no-audit --no-fund || return 1
  npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build
}

stage6() {
  local app=examples/webhook-consumer-nest
  [ -d "$app/node_modules" ] || npm --prefix "$app" ci --no-audit --no-fund || return 1
  npm --prefix "$app" run typecheck && npm --prefix "$app" run test:all
}

echo "ProcureLedger verify ($(date '+%Y-%m-%d %H:%M:%S'))"

stage "1. go vet · go test" stage1
stage "2. 권한 선언 누락 검사" stage2
stage "3. 프론트 tsc · vitest · vite build" stage3

if [ -f cmd/eval/main.go ]; then
  stage "4. 평가 러너(mock 합격률)" go run ./cmd/eval --provider mock
else
  unimplemented "4. 평가 러너(mock 합격률)" "cmd/eval 없음"
fi

# e2e.sh 는 미구현 단계가 남아 있으면 종료 코드 3 으로 끝난다.
echo
echo "=== [5. e2e 스모크] ==="
bash scripts/e2e.sh
rc=$?
case $rc in
  0) record "5. e2e 스모크" "통과" ;;
  3) record "5. e2e 스모크" "미구현" "e2e.sh 에 미구현 단계가 남아 있음" ;;
  *) record "5. e2e 스모크" "실패" "종료 코드 $rc" ;;
esac

stage "6. 예제 NestJS 수신기 tsc · jest(단위·e2e)" stage6

echo
echo "================ 요약 ================"
bad=0
for i in "${!NAMES[@]}"; do
  printf '  %-8s %s %s\n' "${RESULTS[$i]}" "${NAMES[$i]}" "${NOTES[$i]:+(${NOTES[$i]})}"
  [ "${RESULTS[$i]}" = "통과" ] || bad=$((bad + 1))
done
if [ "$bad" -ne 0 ]; then
  echo "결과: 실패 — 통과하지 못한 단계 $bad 개(실패·미구현 포함)"
  exit 1
fi
echo "결과: 전체 통과"
