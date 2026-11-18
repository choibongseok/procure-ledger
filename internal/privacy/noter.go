// Package privacy 는 개인정보 원문 열람 기록(B41 ①)이다.
//
// 원문이 응답(화면·내보내기·MCP)에 실리는 서비스 경로는 응답을 만들기 전에 Noter.Note(또는 쓰기 트랜잭션 안에서 NoteTx)를 부른다.
// 기록하지 못하면 오류를 돌려주고, 호출자는 원문을 싣지 않고 실패로 응답한다(fail-closed, 명세 D2). 조용한 마스킹 강등은 금지다.
package privacy

import (
	"context"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 범주(명세 5.1).
const (
	CatContact    = "contact"     // 다른 사람의 이메일·휴대전화 원문
	CatAccount    = "account"     // 거래처·회사·지급 받는 계좌 원문
	CatCard       = "card"        // 소지자가 아닌 사람의 카드 내역 열람
	CatEmployeeNo = "employee_no" // 관리 화면·내보내기·가져오기의 사번
	CatAttendance = "attendance"  // 근태·휴가·휴직 사유 원문과 증빙(B50 11절)
)

// 정보주체 종류.
const (
	SubjectUser         = "user"
	SubjectVendor       = "vendor"
	SubjectCompany      = "company"
	SubjectPayment      = "payment"
	SubjectCounterparty = "counterparty" // 세금계산서 거래 상대방(B43)
)

// 필드 이름(값은 담지 않는다).
const (
	FieldEmail      = "email"
	FieldPhone      = "phone"
	FieldAccountNo  = "account_no"
	FieldBusinessNo = "business_no"
	FieldCardTxn    = "card_txn"
	FieldEmployeeNo = "employee_no"

	FieldLeaveReason   = "leave_reason"   // 휴가 신청 사유(B50)
	FieldAbsenceReason = "absence_reason" // 휴직 발령 사유(B50)
	FieldProofFile     = "proof_file"     // 휴가 증빙 첨부(진단서 등, B50)
)

// 화면 이름 상수. Access.Screen 은 이 상수, "내보내기 <데이터 세트 이름>", "MCP <도구 이름>", "API <경로>"(B42) 중 하나여야 한다(임의 문자열 금지: 값이 섞여 들어가지 않게).
const (
	ScreenPersonCard       = "조직도 사람 카드"
	ScreenPeopleList       = "조직도 사람 목록"
	ScreenPeopleExport     = "조직 구성원 내보내기"
	ScreenVendorList       = "거래처 목록"
	ScreenVendorDetail     = "거래처 상세"
	ScreenPOPrint          = "발주서 출력"
	ScreenAccountReveal    = "계좌 원문 보기"
	ScreenPayeeReveal      = "지급 받는 계좌 보기"
	ScreenCardTxn          = "카드 내역"
	ScreenCardExport       = "카드 내역 내보내기"
	ScreenEmployeeAdmin    = "사번 관리 화면"
	ScreenImportPreview    = "가져오기 미리보기"
	ScreenAppointments     = "인사 발령"
	ScreenVerifyDoc        = "문서 검증"
	ScreenUserAdmin        = "사용자 관리 화면"
	ScreenAuthUsers        = "로그인 설정 사용자 목록"
	ScreenTaxInvoiceList   = "세금계산서 목록"
	ScreenTaxInvoiceDetail = "세금계산서 상세"
	ScreenKnowledgeDoc     = "사내 문서 원문 보기"   // B47: 업로드 매뉴얼·규정의 원문(정보주체를 식별하지 않는다: UnknownSubjects)
	ScreenKnowledgeFile    = "사내 문서 원본 내려받기" // B47
	ScreenAttendanceReason = "근태 사유 보기"      // B50: 휴가·휴직 사유 원문(본인 열람은 기록하지 않는다)
	ScreenAttendanceProof  = "근태 증빙 내려받기"    // B50: 병가 진단서 등 증빙 첨부
)

var knownScreens = map[string]bool{
	ScreenPersonCard: true, ScreenPeopleList: true, ScreenPeopleExport: true, ScreenVendorList: true, ScreenVendorDetail: true, ScreenPOPrint: true,
	ScreenAccountReveal: true, ScreenPayeeReveal: true, ScreenCardTxn: true, ScreenCardExport: true, ScreenEmployeeAdmin: true,
	ScreenImportPreview: true, ScreenAppointments: true, ScreenVerifyDoc: true, ScreenUserAdmin: true, ScreenAuthUsers: true,
	ScreenTaxInvoiceList: true, ScreenTaxInvoiceDetail: true, ScreenKnowledgeDoc: true, ScreenKnowledgeFile: true, ScreenAttendanceReason: true, ScreenAttendanceProof: true,
}

// ValidScreen 은 화면 이름이 허용 형식인지다.
func ValidScreen(s string) bool {
	if len([]rune(s)) == 0 || len([]rune(s)) > 80 {
		return false
	}
	return knownScreens[s] || strings.HasPrefix(s, "내보내기 ") || strings.HasPrefix(s, "MCP ") || strings.HasPrefix(s, "API ")
}

// Subject 는 원문이 속한 정보주체 하나다.
type Subject struct {
	Type string // user | vendor | company | payment
	ID   int64
}

// Access 는 원문 열람 한 번(응답 한 번)이다. 목록이면 응답 한 번에 한 번 부르고 Subjects 에 원문이 실린 모든 정보주체를 담는다.
type Access struct {
	Category string    // CatContact 등
	Screen   string    // ValidScreen
	Dataset  string    // 내보내기 데이터 세트 코드(없으면 "")
	Export   bool      // 파일 내보내기인가
	Fields   []string  // 원문이 실린 필드 이름(중복은 기록기가 합친다)
	Subjects []Subject // 정보주체(본인이 본인 것을 본 경우는 호출자가 뺀다)
	// UnknownSubjects 는 식별하지 못한 정보주체 수다(행 단위 식별이 없는 내보내기). 기록의 건수에는 더해지고 정보주체 행은 만들지 않는다.
	UnknownSubjects int
	// Anomaly 가 참이면 MCP 로 원문이 나간 이상 상태(exposure=mcp_anomaly)로 기록한다. MCP 래퍼만 쓴다.
	Anomaly bool
}

// Noter 는 서비스가 받는 기록 인터페이스다. 구현은 *Recorder.
type Noter interface {
	// Note 는 읽기 경로용이다: 자체 짧은 트랜잭션에서 기록한다. 오류면 호출자는 원문을 싣지 않는다.
	Note(ctx context.Context, p contract.Principal, a Access) error
	// NoteTx 는 이미 열린 쓰기 트랜잭션 안에서 기록한다(계좌 reveal·내보내기처럼 감사와 함께 쓰는 경로). 오류를 돌려주면 호출자가 롤백한다.
	NoteTx(ctx context.Context, tx contract.Tx, p contract.Principal, a Access) error
}

// 본인 제외: 호출자가 쓰는 도우미. 정보주체에서 열람자 본인(user)을 뺀다.
func ExcludeSelf(p contract.Principal, subs []Subject) []Subject {
	out := make([]Subject, 0, len(subs))
	for _, s := range subs {
		if s.Type == SubjectUser && s.ID == p.UserID {
			continue
		}
		out = append(out, s)
	}
	return out
}

type discard struct{}

func (discard) Note(context.Context, contract.Principal, Access) error { return nil }
func (discard) NoteTx(context.Context, contract.Tx, contract.Principal, Access) error {
	return nil
}

// Discard 는 아무것도 기록하지 않는 Noter 다. 기록 저장소가 없는 시험 환경에서만 쓴다(운영 조립은 항상 *Recorder).
var Discard Noter = discard{}
