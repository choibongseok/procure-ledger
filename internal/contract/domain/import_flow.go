package domain

import (
	"context"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// B35 기초 데이터 가져오기의 공통 흐름(platform, internal/importflow)과 대상별 반영(erp)이 맞물리는 계약이다. 추가만 한다.
//
// 흐름: 양식 내려받기 → 파일 올리기(미리보기, 저장·변경 없음) → 확인 후 반영(같은 파일을 반영 토큰과 함께 다시 올린다) → 이력.
// 파일은 서버에 보관하지 않는다(명세 D8). 반영은 파일을 다시 읽어 미리보기와 같은 판정인지 확인한 뒤 한 트랜잭션으로 한다.
// 대상별 도메인 반영(품목·거래처·기초 재고 등)은 ImportTargetHandler 를 구현해 ImportService.RegisterTarget 으로 붙인다.

// 가져오기 묶음 상태(명세 4절).
const (
	ImportPreviewed = "previewed"
	ImportApplied   = "applied"
	ImportExpired   = "expired"
	ImportCanceled  = "canceled"
)

// 행 판정(명세 3.5). 오류가 하나라도 있는 묶음은 반영할 수 없다. 경고(반영은 되지만 주의할 값)는 판정이 아니라 행의 Warn 문구이고 추가·변경·동일 어느 행에도 붙는다.
const (
	ImportVerdictAdd    = "add"    // 키가 없던 행
	ImportVerdictChange = "change" // 키는 있고 값이 다름
	ImportVerdictSame   = "same"   // 키와 값이 같음
	ImportVerdictError  = "error"  // 반영 불가
)

// ImportPerm 은 대상별 권한이다: 마스터(계정과목·창고·거래처·품목)는 import:master, 금액성(부서 예산·기초 재고·미지급 잔액)은 import:balance.
func ImportPerm(target string) contract.Permission {
	switch target {
	case ImportBudget, ImportOpeningStock, ImportOpeningPayable:
		return contract.PermImportBalance
	}
	return contract.PermImportMaster
}

// ImportNeedsBaseDate 는 미리보기에 기준일 입력이 필요한 대상이다(기초 재고·미지급 잔액).
func ImportNeedsBaseDate(target string) bool {
	return target == ImportOpeningStock || target == ImportOpeningPayable
}

// ImportChange 는 바뀌는 칸 하나다. Old·New 는 화면에 보여 줄 값이고 민감한 값(계좌번호·연락처)은 대상 어댑터가 가린 값만 넣는다.
type ImportChange struct {
	Column string `json:"column"`
	Old    string `json:"old"`
	New    string `json:"new"`
}

// ImportRowVerdict 는 대상 어댑터가 행마다 내린 판정이다. Reason 은 오류·경고의 한국어 사유이고 원본 값(이름·금액·계좌)을 담지 않는다.
type ImportRowVerdict struct {
	RowNo   int            `json:"rowNo"`
	Key     string         `json:"key"`     // 화면에 보일 키(품목 SKU·창고 코드 등. 사업자번호 같은 민감 키는 가린 값)
	Verdict string         `json:"verdict"` // add·change·same·error·warn
	Reason  string         `json:"reason,omitempty"`
	Warn    string         `json:"warn,omitempty"` // 경고 문구(없으면 빈 값)
	Changes []ImportChange `json:"changes,omitempty"`
}

// ImportPlanInput 은 대상 어댑터의 Plan·Apply 가 받는 파일 밖 입력이다.
type ImportPlanInput struct {
	Target   string
	BaseDate string // 기초 재고·미지급 잔액의 기준일 YYYY-MM-DD(그 밖은 빈 값)
	BatchNo  string // Apply 때만 채워진다(미리보기 때는 빈 값). 마스터 변경 감사에 출처로 덧붙인다
}

// ImportPlan 은 Plan 의 결과다. Verdicts 는 입력 행과 같은 순서·같은 개수다.
type ImportPlan struct {
	Verdicts []ImportRowVerdict
	// Summary 는 검산 문장이다(명세 3.5.4 예: "품목 12종·수량 합 340·재고 금액 합 1,250,000원"). 없으면 비워 둔다.
	Summary []string
}

// ImportApplyOutcome 은 Apply 의 결과다.
type ImportApplyOutcome struct {
	JournalNos []string // 같이 만든 기초 전표 번호(없으면 비어 있다)
}

// ImportTargetHandler 는 대상 하나의 도메인 반영이다(erp). 한 대상에 하나만 붙는다.
//
// Plan 은 읽기 전용이다(q 로 조회만 한다). 파서가 이미 형식·길이·필수·열거를 검증한 정규화 행(ImportRow.Cells)을 받아 키로 DB 와 비교한다.
// Apply 는 호출한 서비스가 연 한 트랜잭션 안에서 불린다. 저장 함수를 같은 tx 로 호출하고, 하나라도 실패하면 오류를 돌려 전체를 취소한다.
// Apply 는 add·change 판정 행만 받는다(same 은 건너뛴다). 감사는 서비스가 반영 한 건으로 남기고, 대상 쪽 기존 감사(vendor.update 등)는 어댑터가 부른다.
type ImportTargetHandler interface {
	Target() string
	Plan(ctx context.Context, q contract.Tx, rows []ImportRow, in ImportPlanInput) (ImportPlan, error)
	Apply(ctx context.Context, tx contract.Tx, p contract.Principal, rows []ImportRow, plan []ImportRowVerdict, in ImportPlanInput) (ImportApplyOutcome, error)
}

// ImportCounts 는 미리보기·반영 건수다.
type ImportCounts struct {
	Total int `json:"total"`
	Add   int `json:"add"`
	Same  int `json:"same"`
	Chg   int `json:"change"`
	Err   int `json:"error"`
	Warn  int `json:"warn"`
}

// ImportPreview 는 미리보기 결과다.
type ImportPreview struct {
	BatchNo       string             `json:"batchNo"` // 변경할 내용이 없거나(전부 동일) 오류만 있어도 만들어진다. 전부 동일이면 비어 있다
	Token         string             `json:"token"`   // 반영 토큰. 이 응답에서만 보인다. 반영할 수 없으면 비어 있다
	Target        string             `json:"target"`
	BaseDate      string             `json:"baseDate,omitempty"`
	Counts        ImportCounts       `json:"counts"`
	Applicable    bool               `json:"applicable"` // 오류 0건이고 반영할 변경이 있다
	Message       string             `json:"message,omitempty"`
	KeyRule       string             `json:"keyRule"`
	Summary       []string           `json:"summary,omitempty"`
	FileWarnings  []string           `json:"fileWarnings,omitempty"`
	Rows          []ImportRowVerdict `json:"rows"`          // 상위 ImportPreviewRows 행(오류·경고·변경·추가 순)
	RowsTruncated bool               `json:"rowsTruncated"` // 전체 행이 더 있다(오류 파일로 받는다)
	SameFileBatch string             `json:"sameFileBatch,omitempty"`
}

// ImportPreviewRows 는 미리보기 응답이 싣는 행 수의 상한이다(명세 8절, 전체는 오류 파일).
const ImportPreviewRows = 200

// ImportBatch 는 이력의 묶음 한 건이다.
type ImportBatch struct {
	No         string       `json:"no"`
	Target     string       `json:"target"`
	TargetName string       `json:"targetName"`
	Status     string       `json:"status"`
	BaseDate   string       `json:"baseDate,omitempty"`
	Counts     ImportCounts `json:"counts"`
	Summary    []string     `json:"summary,omitempty"`
	JournalNos []string     `json:"journalNos,omitempty"`
	CreatedBy  string       `json:"createdBy"`
	CreatedAt  string       `json:"createdAt"`
	AppliedAt  string       `json:"appliedAt,omitempty"`
}

// ImportBatchDetail 은 묶음 상세다. 행은 최대 ImportDetailRows 행이다.
type ImportBatchDetail struct {
	ImportBatch
	Rows      []ImportRowVerdict `json:"rows"`
	Truncated bool               `json:"truncated"`
}

// ImportDetailRows 는 묶음 상세 응답의 행 수 상한이다.
const ImportDetailRows = 500

// ImportFilter 는 이력 조회 조건이다.
type ImportFilter struct {
	Target string
	Status string
	Limit  int
}

// ImportColumnGuide 는 양식 열 하나의 작성 안내다(양식 정의에서 만든 문구, 화면에 표로 보인다).
type ImportColumnGuide struct {
	Label    string `json:"label"`
	Required bool   `json:"required"`
	Help     string `json:"help"`
	Example  string `json:"example"`
}

// ImportTargetStatus 는 화면의 대상 카드 한 장이다(마지막 반영 묶음·건수, 권한이 없거나 아직 연결되지 않은 대상은 사유).
type ImportTargetStatus struct {
	Target      string              `json:"target"`
	Name        string              `json:"name"`
	NeedsDate   bool                `json:"needsBaseDate"`
	KeyRule     string              `json:"keyRule"`
	Allowed     bool                `json:"allowed"`
	Ready       bool                `json:"ready"` // 대상 어댑터가 붙어 있다
	Reason      string              `json:"reason,omitempty"`
	LastBatchNo string              `json:"lastBatchNo,omitempty"`
	LastAdded   int                 `json:"lastAdded"`
	LastChanged int                 `json:"lastChanged"`
	LastAt      string              `json:"lastAt,omitempty"`
	Columns     []ImportColumnGuide `json:"columns"`
}

// ImportFile 은 내려받는 파일이다.
type ImportFile struct {
	Name string
	Data []byte
}

// ImportService 는 공통 가져오기 흐름이다(platform). 웹 핸들러가 호출하고, MCP 에는 노출하지 않는다.
type ImportService interface {
	RegisterTarget(h ImportTargetHandler)
	Targets(ctx context.Context) ([]ImportTargetStatus, error)
	Template(ctx context.Context, target string) (ImportFile, error)
	Preview(ctx context.Context, target, baseDate string, data []byte) (ImportPreview, error)
	Apply(ctx context.Context, target, batchNo, token string, data []byte) (ImportBatch, error)
	Cancel(ctx context.Context, target, batchNo string) error
	History(ctx context.Context, f ImportFilter) ([]ImportBatch, error)
	Batch(ctx context.Context, no string) (ImportBatchDetail, error)
	ErrorFile(ctx context.Context, no string) (ImportFile, error)
}
