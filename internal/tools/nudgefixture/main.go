// 명령: nudgefixture — e2e 전용 픽스처(B29 AC-851).
//
//	go run ./internal/tools/nudgefixture -db <DB 파일> -doc <문서 번호> [-hours 25]
//
// 서버를 멈춘 **시험 전용 DB** 에서 한 문서의 상신 시각을 hours 시간 앞당긴다. 실제 시계로는 "대기 24시간 지연"을 만들 수 없어 독촉(결재 독촉) 흐름을 실제 서버로 돌리려는 도구다.
// 결재 이력의 수정 금지 트리거를 이 DB 에서 지우고 갱신하므로 운영 DB 에 쓰면 안 된다. 이 도구는 서버 바이너리에 들어가지 않고 운영 코드에는 시계 조작 경로가 없다.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

func main() {
	dbPath := flag.String("db", "", "서버가 시드를 만든 시험 전용 DB 파일")
	doc := flag.Int64("doc", 0, "문서(구매요청) 번호")
	hours := flag.Int("hours", 25, "앞당길 시간")
	flag.Parse()
	if *dbPath == "" || *doc == 0 {
		fmt.Fprintln(os.Stderr, "-db 와 -doc 이 필요합니다")
		os.Exit(2)
	}
	if err := run(*dbPath, *doc, *hours); err != nil {
		fmt.Fprintln(os.Stderr, "픽스처 실패:", err)
		os.Exit(1)
	}
}

func run(path string, doc int64, hours int) error {
	db, err := dbutil.Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DROP TRIGGER IF EXISTS trg_approval_history_no_update`); err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, fmt.Sprintf(`UPDATE approval_history SET at = strftime('%%Y-%%m-%%dT%%H:%%M:%%SZ', at, '-%d hours') WHERE pr_id = ? AND action IN ('submit','resubmit')`, hours), doc)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("문서 %d 의 상신 이력이 없습니다", doc)
	}
	return nil
}
