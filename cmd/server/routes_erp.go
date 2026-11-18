package main

import (
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/deps"
	"github.com/choibongseok/procure-ledger/internal/handler"
)

// registerERP 는 erp 라우트(구매요청·결재·재고·입고)를 등록한다. 새 Register 함수는 여기에만 추가한다.
func registerERP(r contract.Registrar, d deps.Deps) {
	handler.RegisterPurchase(r, d)
	handler.RegisterApproval(r, d)
	handler.RegisterInventory(r, d)
	handler.RegisterLeave(r, d)      // B50 근태·휴가
	handler.RegisterAttendance(r, d) // B50 출퇴근·정정·근태 마감·근무 유형
	handler.RegisterNotification(r, d)
	handler.RegisterForms(r, d)
	handler.RegisterApprovalDocs(r, d)
	handler.RegisterInbox(r, d)
	handler.RegisterAuthority(r, d)
	handler.RegisterStock(r, d)
	handler.RegisterBudget(r, d)
	handler.RegisterRetention(r, d) // 보존 만료 문서 폐기(B29)
	handler.RegisterMonthClose(r, d)
	handler.RegisterMonthStock(r, d)
	handler.RegisterLedger(r, d)          // B31 t2 계정과목·전표
	handler.RegisterPayments(r, d)        // B32 t2 지급 요청·지급 처리·자금 계좌·자금 일보
	handler.RegisterVAT(r, d)             // B32 t2 부가세 매입 집계
	handler.RegisterConsolidated(r, d)    // B44 t2 연결 재무 집계
	handler.RegisterSites(r, d)           // B44 t2 현재 법인의 사업장 선택기 목록
	handler.RegisterTaxInvoice(r, d)      // B43 t2 전자세금계산서
	handler.RegisterTaxMatch(r, d)        // B43 2단계 t2 3자 대조·수신 동기화
	handler.RegisterCompany(r, d)         // B34 t2 회사 설정
	handler.RegisterImportReconcile(r, d) // B35 t2 기초 데이터 대조 보고서
	handler.RegisterCards(r, d)           // B32 t2 법인카드
	handler.RegisterNudge(r, d)           // B29 t2 결재 독촉
	handler.RegisterDocSearch(r, d)       // B29 t2 문서 통합 검색
	handler.RegisterReports(r, d)         // B28 t2 업무 보고서 3종과 내보내기
	handler.RegisterListExports(r, d)     // B28 t2 목록 내보내기(구매요청·발주·입고·재고·수불부·문서함·예산)
	handler.RegisterPOAndRating(r, d)
	handler.RegisterQuotes(r, d)
	handler.RegisterPersonalLines(r, d)
}
