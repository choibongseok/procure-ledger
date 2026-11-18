package aicost

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strconv"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// ExportCSV 는 월·법인의 부서별 배부 결과를 CSV 로 만든다(cost:read). 확정된 달은 확정 결과, 아니면 규칙으로 계산한 미리보기다(상태 칸이 다르다).
// 건수·금액만 담고 사용자 이름·프롬프트는 없다. 내려받기는 감사에 남는다(ai_cost.export).
func (s *Service) ExportCSV(ctx context.Context, month string, entityID int64) (string, []byte, error) {
	p, err := contract.Authorize(ctx, contract.PermCostRead)
	if err != nil {
		return "", nil, err
	}
	entity, err := s.scopedEntity(ctx, entityID)
	if err != nil {
		return "", nil, err
	}
	month, err = s.normMonth(month)
	if err != nil {
		return "", nil, err
	}
	type row struct {
		name                                    string
		headcount, calls, direct, shared, total int64
	}
	var rows []row
	status := "미리보기"
	run, err := s.GetRun(ctx, month, entity)
	if err != nil {
		return "", nil, err
	}
	if run != nil && run.Status == runConfirmed {
		status = "확정"
		for _, l := range run.Lines {
			rows = append(rows, row{l.DepartmentName, l.Headcount, l.Calls, l.DirectKRW, l.SharedKRW, l.TotalKRW})
		}
	} else {
		pv, _, err := s.computePreview(ctx, month, entity, 0)
		if err != nil {
			return "", nil, err
		}
		if len(pv.Blockers) > 0 {
			return "", nil, fmt.Errorf("%w: %s", contract.ErrInvalidState, pv.Blockers[0])
		}
		for _, l := range pv.Lines {
			rows = append(rows, row{l.DepartmentName, l.Headcount, l.Calls, l.DirectKRW, l.SharedKRW, l.TotalKRW})
		}
	}
	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF") // 엑셀이 한글을 바로 읽도록 UTF-8 표식
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"월", "법인", "부서", "월말 인원", "호출", "직접 비용(원)", "공용 비용 몫(원)", "부담액(원)", "상태"})
	for _, r := range rows {
		_ = w.Write([]string{month, strconv.FormatInt(entity, 10), csvSafe(r.name), strconv.FormatInt(r.headcount, 10), strconv.FormatInt(r.calls, 10),
			strconv.FormatInt(r.direct, 10), strconv.FormatInt(r.shared, 10), strconv.FormatInt(r.total, 10), status})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", nil, err
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "ai_cost.export", TargetType: audit.TargetAICost, TargetID: fmt.Sprintf("export/%s/%d", month, entity), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "month", New: month}, {Key: "exported_rows", New: strconv.Itoa(len(rows))}}})
	})
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("ai-cost-%s.csv", month), buf.Bytes(), nil
}

// csvSafe 는 스프레드시트가 수식으로 읽을 수 있는 앞글자(= + - @)를 막는다(부서 이름은 조직 관리자가 정한 글자다).
func csvSafe(s string) string {
	if s != "" && (s[0] == '=' || s[0] == '+' || s[0] == '-' || s[0] == '@') {
		return "'" + s
	}
	return s
}
