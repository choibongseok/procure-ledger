// 명령: importfixture — e2e 전용 픽스처(B35 AC-1590).
//
//	go run ./internal/tools/importfixture -target <대상> -rows <행.json> -out <파일.xlsx>
//
// 가져오기 양식(머리글 줄)에 JSON 으로 준 행을 채워 XLSX 를 만든다. 사람이 엑셀로 채우는 일을 e2e 에서 흉내 내려는 도구이며,
// 양식 정의(importx.Targets)를 그대로 쓰므로 머리글이 서버 양식과 항상 같다. 서버 바이너리에는 들어가지 않는다.
// rows 는 [{"열 ID":"값", ...}, ...] 이고 열 ID 는 importx 의 열 정의 ID 다(예: sku, warehouse, qty).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/choibongseok/procure-ledger/internal/export"
	"github.com/choibongseok/procure-ledger/internal/importx"
)

func main() {
	target := flag.String("target", "", "가져오기 대상 코드(account·warehouse·vendor·item·budget·opening_stock·opening_payable)")
	rowsFile := flag.String("rows", "", "행을 담은 JSON 파일")
	out := flag.String("out", "", "만들 XLSX 파일")
	flag.Parse()
	if *target == "" || *rowsFile == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "-target, -rows, -out 이 필요합니다")
		os.Exit(2)
	}
	if err := run(*target, *rowsFile, *out); err != nil {
		fmt.Fprintln(os.Stderr, "픽스처 실패:", err)
		os.Exit(1)
	}
}

func run(target, rowsFile, out string) error {
	t, ok := importx.Targets[target]
	if !ok {
		return fmt.Errorf("알 수 없는 대상: %s", target)
	}
	raw, err := os.ReadFile(rowsFile)
	if err != nil {
		return err
	}
	var in []map[string]string
	if err := json.Unmarshal(raw, &in); err != nil {
		return err
	}
	cols := make([]export.Column, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = export.Column{Label: c.Header(), Kind: export.KindText}
	}
	rows := make([][]export.Cell, len(in))
	for i, r := range in {
		row := make([]export.Cell, len(t.Columns))
		for j, c := range t.Columns {
			if v := r[c.ID]; v != "" {
				row[j] = v
			}
		}
		rows[i] = row
	}
	data, err := export.WriteXLSX(export.Table{Dataset: export.Dataset{Code: t.Code, Label: t.Label, Columns: cols}, Rows: rows},
		export.Meta{At: time.Now(), By: "시험", Filters: fmt.Sprintf("양식 버전 %d | e2e 픽스처", importx.TemplateVersion)})
	if err != nil {
		return err
	}
	return os.WriteFile(out, data, 0o600)
}
