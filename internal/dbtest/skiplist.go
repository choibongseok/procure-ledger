package dbtest

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// PGSkip 은 PG 모드에서 건너뛰어도 되는 시험 한 건이다(AC-1741). 사유·담당·기한이 없으면 목록에 못 넣는다.
type PGSkip struct {
	Pkg    string // 모듈 루트 기준 패키지 경로(예: internal/notify)
	Test   string // 최상위 시험 함수 이름
	Reason string
	Owner  string // platform, erp, ai
	Until  string // YYYY-MM-DD. 이 날짜가 지나면 건너뛸 수 없다(PG 에서 통과시키거나 사유를 갱신해야 한다)
}

// AllowedPGSkips 가 SkipPG 로 건너뛸 수 있는 시험의 전부다. 목록 밖의 SkipPG 호출과 기한이 지난 항목은 PG 모드에서 실패한다.
// 항목을 늘리려면 사유·담당·기한을 적고 리뷰를 받는다. 줄이는 쪽이 목표다.
var AllowedPGSkips = []PGSkip{
	{Pkg: "internal/notify", Test: "TestTransitionTableMatchesTrigger",
		Reason: "statusTriggerSQL 이 SQLite 트리거 정의라 PG 에서 재생성할 수 없다. PG 트리거는 migrations/pg 와 스키마 동등성 시험이 다룬다",
		Owner:  "platform", Until: "2027-03-31"},
	{Pkg: "cmd/server", Test: "TestNotifyChannelsEndToEndOnRealAssembly",
		Reason: "org.SeedDev 가 PG 미지원(internal/org 의 INSERT OR IGNORE·LastInsertId). 조직 시드가 PG 에서 돌면 건너뜀을 뺀다",
		Owner:  "erp", Until: "2026-12-31"},
	{Pkg: "internal/service", Test: "TestCardSchemaHasNoFullCardNumber",
		Reason: "SQLite 트리거·PRAGMA 를 직접 쓰는 시험이다. PG 트리거 동등성은 migrations 스키마 동등성 시험과 t2 동시성 시험이 다룬다",
		Owner:  "erp", Until: "2027-03-31"},
	{Pkg: "internal/service", Test: "TestReassignRollsBackWhenNotificationInsertFails",
		Reason: "SQLite 트리거·PRAGMA 를 직접 쓰는 시험이다. PG 트리거 동등성은 migrations 스키마 동등성 시험과 t2 동시성 시험이 다룬다",
		Owner:  "erp", Until: "2027-03-31"},
	{Pkg: "internal/service", Test: "TestLegacyStepDisplaysTitleAndAllCandidates",
		Reason: "SQLite 트리거·PRAGMA 를 직접 쓰는 시험이다. PG 트리거 동등성은 migrations 스키마 동등성 시험과 t2 동시성 시험이 다룬다",
		Owner:  "erp", Until: "2027-03-31"},
	{Pkg: "cmd/server", Test: "TestB50LeaveFlowDoesNotUsePoolInsideTx_ProdSQLite",
		Reason: "운영 SQLite 의 연결 1개 제약(트랜잭션 안 풀 연결 사용이 영구 대기가 되는 사고)을 시험한다. PG 는 이 제약이 없다",
		Owner:  "erp", Until: "2027-03-31"},
	{Pkg: "cmd/server", Test: "TestB50LeaveFlowDoesNotUsePoolInsideTx_Guarded",
		Reason: "SQLite 드라이버를 감싸 트랜잭션 안 풀 연결 사용을 모으는 시험이다. PG 에는 같은 위험이 없다",
		Owner:  "erp", Until: "2027-03-31"},
}

// pkgOf 는 호출한 시험 파일의 패키지 경로(모듈 루트 기준)를 돌려준다.
func pkgOf(skip int) string {
	_, file, _, ok := runtime.Caller(skip + 1)
	if !ok {
		return ""
	}
	dir := filepath.ToSlash(filepath.Dir(file))
	if i := strings.LastIndex(dir, "/internal/"); i >= 0 {
		return dir[i+1:]
	}
	if i := strings.LastIndex(dir, "/cmd/"); i >= 0 {
		return dir[i+1:]
	}
	return dir
}

// checkSkipAllowed 는 (pkg, 최상위 시험 이름)이 목록에 있고 기한 안인지 본다. 아니면 사유를 돌려준다.
func checkSkipAllowed(pkg, test string, now time.Time) error {
	if i := strings.Index(test, "/"); i >= 0 {
		test = test[:i]
	}
	for _, s := range AllowedPGSkips {
		if s.Pkg != pkg || s.Test != test {
			continue
		}
		until, err := time.Parse("2006-01-02", s.Until)
		if err != nil {
			return fmt.Errorf("허용 목록의 기한 %q 형식이 잘못됨", s.Until)
		}
		if now.After(until.AddDate(0, 0, 1)) {
			return fmt.Errorf("건너뛰기 허용 기한(%s)이 지났습니다. PG 에서 통과시키거나 사유와 기한을 갱신하세요", s.Until)
		}
		return nil
	}
	return fmt.Errorf("%s 의 %s 는 PG 건너뛰기 허용 목록(internal/dbtest/skiplist.go)에 없습니다. 사유·담당·기한을 적어 추가하거나 PG 에서 통과시키세요", pkg, test)
}

func enforceSkipAllowed(t testing.TB) {
	t.Helper()
	if err := checkSkipAllowed(pkgOf(2), t.Name(), time.Now()); err != nil {
		t.Fatal(err)
	}
}
