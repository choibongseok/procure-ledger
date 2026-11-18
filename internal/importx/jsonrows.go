package importx

import (
	"fmt"
	"time"

	"github.com/choibongseok/procure-ledger/internal/export"
)

// BuildXLSXFromRows 는 연동 API(B42 3단계)가 받은 JSON 행을 양식 XLSX 로 바꾼다. 행의 키는 양식 열의 식별자(name·business_no·payment_days …)이고 값은 문자열이다.
// 이렇게 만든 파일은 Parser.Parse 가 그대로 읽으므로 형식·길이·필수·열거·중복 검증과 이후의 미리보기·반영이 화면 업로드와 같은 코드를 탄다(API 가 새 판정을 만들지 않는다).
// 모든 칸은 문자열 형식이다(사업자번호·계좌번호·SKU 의 선행 0 이 사라지지 않게). 알 수 없는 열 이름·행 수 초과는 검증 오류다.
func BuildXLSXFromRows(target string, rows []map[string]string) ([]byte, error) {
	t, ok := Targets[target]
	if !ok {
		return nil, verr("알 수 없는 가져오기 대상입니다.")
	}
	if len(rows) == 0 {
		return nil, verr("행이 없습니다.")
	}
	if len(rows) > t.MaxRows {
		return nil, verr("한 번에 %d행까지 보낼 수 있습니다(받은 행 %d).", t.MaxRows, len(rows))
	}
	idx := map[string]int{}
	cols := make([]export.Column, len(t.Columns))
	for i, c := range t.Columns {
		idx[c.ID] = i
		cols[i] = export.Column{Label: c.Header(), Kind: export.KindText}
	}
	out := make([][]export.Cell, len(rows))
	for r, row := range rows {
		cells := make([]export.Cell, len(t.Columns))
		for k, v := range row {
			i, known := idx[k]
			if !known {
				return nil, verr("%d번째 행: 알 수 없는 열 이름입니다(%s).", r+1, k)
			}
			if v != "" {
				cells[i] = v
			}
		}
		out[r] = cells
	}
	return export.WriteXLSX(export.Table{Dataset: export.Dataset{Code: t.Code, Label: t.Label, Columns: cols}, Rows: out},
		export.Meta{At: time.Unix(0, 0).UTC(), By: "연동 API", Filters: fmt.Sprintf("양식 버전 %d", TemplateVersion)})
}
