// Package aicost 는 부서별 AI 비용 배부(B49,)의 서비스다.
// 1단계: 월 집계(llm_usage 의 저장 비용을 월·법인·부서·용도·공급자·모델별로), 월 환율, 실제 청구액 입력, 전사·부서장 조회.
// 금액의 원본은 llm_usage.cost_micro_usd 이고(호출 당시 공급자 가격) 이 패키지는 그 값을 모을 뿐 토큰으로 다시 곱하지 않는다.
package aicost

import (
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/sod"
)

// Options 는 서비스 의존성이다.
type Options struct {
	DB    *sql.DB
	Txs   contract.TxManager
	Audit audit.Recorder
	Now   func() time.Time
	// Journals 는 회계 서비스의 배부 전표 입구다(없으면 확정할 수 없다). SoD 는 권한 분리 판정기(없으면 판정하지 않는다).
	Journals domain.AICostJournalHook
	SoD      sod.Evaluator
}

// Service 는 AI 비용 서비스다.
type Service struct{ o Options }

// New 는 서비스를 만든다. 필수 의존성이 없으면 오류다.
func New(o Options) (*Service, error) {
	if o.DB == nil || o.Txs == nil || o.Audit == nil {
		return nil, errors.New("aicost: DB·Txs·Audit 가 필요합니다")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{o: o}, nil
}

func (s *Service) now() time.Time { return s.o.Now() }

func valErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", contract.ErrValidation, fmt.Sprintf(format, a...))
}

var reMonth = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// currentMonth 는 오늘(KST)의 월이다.
func (s *Service) currentMonth() string { return s.now().In(contract.KST).Format("2006-01") }

// normMonth 는 빈 값을 이번 달로 바꾸고 형식을 확인한다.
func (s *Service) normMonth(m string) (string, error) {
	if m == "" {
		return s.currentMonth(), nil
	}
	if !reMonth.MatchString(m) {
		return "", valErr("월은 YYYY-MM 형식으로 입력하세요")
	}
	return m, nil
}

// monthRange 는 KST 달력 월을 llm_usage.at(UTC RFC3339 문자열) 비교용 [from, to) 로 바꾼다. 같은 형식의 UTC 문자열은 글자 순서가 시간 순서다.
func monthRange(month string) (from, to string, err error) {
	t, err := time.ParseInLocation("2006-01", month, contract.KST)
	if err != nil {
		return "", "", valErr("월은 YYYY-MM 형식으로 입력하세요")
	}
	return t.UTC().Format(time.RFC3339), t.AddDate(0, 1, 0).UTC().Format(time.RFC3339), nil
}

// prevMonth 는 n 달 전의 월이다.
func prevMonth(month string, n int) string {
	t, err := time.ParseInLocation("2006-01", month, contract.KST)
	if err != nil {
		return month
	}
	return t.AddDate(0, -n, 0).Format("2006-01")
}

// microToKRW 는 USD 마이크로 단위 금액을 월 환율(x1000)로 원 단위(반올림)로 바꾼다. 곱셈이 넘치지 않게 큰 수로 계산한다.
func microToKRW(micro, fxX1000 int64) int64 {
	if fxX1000 <= 0 || micro == 0 {
		return 0
	}
	n := new(big.Int).Mul(big.NewInt(micro), big.NewInt(fxX1000))
	d := big.NewInt(1_000_000_000) // micro(1e-6) × x1000(1e3)
	half := new(big.Int).Div(d, big.NewInt(2))
	if n.Sign() < 0 {
		n.Sub(n, half)
	} else {
		n.Add(n, half)
	}
	return n.Quo(n, d).Int64()
}
