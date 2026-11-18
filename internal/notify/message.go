// Package notify 는 알림 채널(B36,)의 발송 큐·메일 발송기·관리 기능이다().
// 업무 서비스가 만든 앱 안 알림을 같은 트랜잭션에서 발송 건으로 큐에 올리고(Enqueue), 서버의 발송 작업(Run)이 트랜잭션 밖에서 보낸다.
//
// 불변식
//   - 업무 트랜잭션은 발송 때문에 느려지거나 실패하지 않는다(Enqueue 는 읽기·한 행 쓰기뿐, 실패해도 알림 자체는 만들어진다).
//   - 외부로 나가는 메시지는 앱 안 알림보다 적은 정보만 담는다: 건수·종류 이름·앱 링크뿐이다(8절). 제목·본문·이름·금액은 틀에 들어갈 길이 없다.
//   - 발송 건에는 메시지 본문·주소를 저장하지 않는다. 메시지는 발송 직전에 만든다.
//   - 메시지 생성에 LLM 을 쓰지 않는다.
package notify

import (
	"fmt"
	"strings"
	"unicode"
)

// KindInfo 는 알림 종류 하나의 한국어 이름과 외부 메시지 틀이다(명세 3.2·8절). 이 표가 종류의 정본이다.
type KindInfo struct {
	Kind    string
	Name    string // 한국어 이름(관리 화면·기본 메일 제목)
	Subject string // 메일 제목 틀
	Line    string // 본문 첫 문장 틀
}

const subjectPrefix = "[ProcureLedger] "

func generic(kind, name string) KindInfo {
	return KindInfo{Kind: kind, Name: name, Subject: subjectPrefix + name, Line: "확인할 알림이 있습니다."}
}

// Kinds 는 알림 종류 표다(기존 19종 + B36 2종 + B41 4종 + B42 1종). 표에 없는 종류는 외부로 보내지 않는다.
var Kinds = []KindInfo{
	{Kind: "approval_pending", Name: "결재 요청", Subject: subjectPrefix + "결재 요청", Line: "결재할 문서가 도착했습니다."},
	{Kind: "approval_rejected", Name: "반려", Subject: subjectPrefix + "반려", Line: "문서가 반려되었습니다."},
	generic("approval_done", "결재 완료"),
	generic("agreement_objected", "합의 이견"),
	generic("document_cc", "참조 문서"),
	{Kind: "receipt_arrived", Name: "수신 문서 도착", Subject: subjectPrefix + "수신 문서", Line: "수신 문서가 도착했습니다."},
	generic("receipt_returned", "수신 반송"),
	generic("receipt_done", "수신 처리 완료"),
	generic("submission_cancelled", "상신 취소"),
	{Kind: "approval_nudge", Name: "결재 독촉", Subject: subjectPrefix + "결재 독촉", Line: "처리하지 않은 결재가 있습니다."},
	generic("approval_reassigned", "결재자 변경"),
	generic("absence_assigned", "대결자 지정"),
	generic("absence_started", "대결 기간 시작"),
	generic("absence_ended", "부재 종료"),
	generic("absence_revoked", "부재 해제"),
	{Kind: "card_reminder", Name: "카드 소명 기한 안내", Subject: subjectPrefix + "카드 소명", Line: "소명할 카드 내역이 있습니다."},
	{Kind: "card_overdue_head", Name: "카드 소명 지연", Subject: subjectPrefix + "카드 소명", Line: "소명할 카드 내역이 있습니다."},
	{Kind: "card_returned", Name: "카드 소명 반려", Subject: subjectPrefix + "카드 소명", Line: "소명할 카드 내역이 있습니다."},
	{Kind: "card_reopened", Name: "카드 소명 보완 필요", Subject: subjectPrefix + "카드 소명", Line: "소명할 카드 내역이 있습니다."},
	{Kind: "payment_due", Name: "지급 예정", Subject: subjectPrefix + "지급 예정", Line: "지급 예정 건이 있습니다."},
	{Kind: "month_close_check", Name: "월 마감 점검", Subject: subjectPrefix + "월 마감 점검", Line: "마감 전 점검 항목이 있습니다."},
	{Kind: "sod_violation", Name: "권한 분리 위반", Subject: subjectPrefix + "권한 분리 위반", Line: "권한 분리 규칙 위반이 기록되었습니다. 앱에서 확인하세요."},
	generic("privacy_request_pending", "정보주체 열람 요청"),
	generic("privacy_request_done", "정보주체 열람 요청 결과"),
	generic("privacy_monthly_report", "권한 분리 월별 점검"),
	{Kind: "webhook_paused", Name: "변경 웹훅 자동 중지", Subject: subjectPrefix + "변경 웹훅 자동 중지", Line: "변경 웹훅 구독이 연속 실패로 중지되었습니다. 앱에서 확인하세요."},
	{Kind: "tax_invoice_failed", Name: "세금계산서 전송 실패", Subject: subjectPrefix + "세금계산서 전송 실패", Line: "전송하지 못한 세금계산서가 있습니다. 앱에서 확인하세요."},
	generic("tax_invoice_sync", "세금계산서 수신 요약"),
	{Kind: "tax_cert_expiring", Name: "대행사 인증서 만료 임박", Subject: subjectPrefix + "대행사 인증서 만료 임박", Line: "전자세금계산서 대행사 인증서의 만료가 가깝습니다. 앱에서 확인하세요."},
}

// KindByCode 는 종류 코드로 정보를 찾는다.
func KindByCode(kind string) (KindInfo, bool) {
	for _, k := range Kinds {
		if k.Kind == kind {
			return k, true
		}
	}
	return KindInfo{}, false
}

// KindName 은 화면에 보일 종류의 한국어 이름이다. 모르는 종류는 "알림"이다(영문 코드를 보이지 않는다).
func KindName(kind string) string {
	if kind == KindTest {
		return "연결 시험"
	}
	if k, ok := KindByCode(kind); ok {
		return k.Name
	}
	return "알림"
}

// Message 는 외부로 나가는 메시지다. 텍스트(HTML 없음), UTF-8.
type Message struct {
	Subject string
	Body    string
}

const maxBody = 4096

// LinkURL 은 알림 행의 앱 안 경로를 절대 URL 로 만든다. 경로가 "/" 로 시작하지 않거나(`//` 로 시작하는 외부 주소 포함)
// 제어문자·공백이 있으면 거부한다(링크 주입 방지). baseURL 은 http(s) 로 시작해야 한다.
func LinkURL(baseURL, path string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return "", fmt.Errorf("앱 주소가 설정되지 않았습니다")
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "\\") {
		return "", fmt.Errorf("앱 링크 경로가 올바르지 않습니다")
	}
	for _, r := range path {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", fmt.Errorf("앱 링크 경로가 올바르지 않습니다")
		}
	}
	return base + path, nil
}

// SettingsLinkPath 는 메일 끝 "알림 설정" 안내가 가리키는 수신 설정 화면의 앱 경로다.
const SettingsLinkPath = "/notifications/settings"

// Compose 는 메일 메시지를 틀로 만든다. 값으로 들어가는 것은 건수와 검증된 앱 링크뿐이다.
// 알림의 제목·본문(문서 제목·이름·금액 등)은 이 함수에 들어오지 않는다.
func Compose(kind string, count int, baseURL, linkPath string) (Message, error) {
	k, ok := KindByCode(kind)
	if !ok {
		return Message{}, fmt.Errorf("알 수 없는 알림 종류입니다")
	}
	link, err := LinkURL(baseURL, linkPath)
	if err != nil {
		return Message{}, err
	}
	line := k.Line
	if count > 1 {
		line += fmt.Sprintf(" (모두 %d건)", count)
	}
	// 수신 거부 안내(명세 4.2): 메일 전체 끄기는 설정 화면에서만 한다. 메일 안에서 상태를 바꾸는 링크는 만들지 않는다.
	settings, err := LinkURL(baseURL, SettingsLinkPath)
	if err != nil {
		return Message{}, err
	}
	body := line + "\n열기: " + link + "\n\n알림 설정: " + settings + "\n"
	if len(body) > maxBody {
		return Message{}, fmt.Errorf("메시지가 너무 깁니다")
	}
	return Message{Subject: k.Subject, Body: body}, nil
}
