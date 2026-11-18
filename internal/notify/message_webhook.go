package notify

import "fmt"

// KindTest 는 연결 시험 발송 건의 종류 코드다. 알림 종류 표에는 없고(설정 대상이 아님) 발송 기록에만 나타난다.
const KindTest = "connection_test"

// TestText 는 연결 시험 메시지다(고정 문구).
const TestText = "[ProcureLedger] 연결 시험입니다."

// ComposeWebhook 은 웹훅(채널·방) 메시지 한 줄을 틀로 만든다(명세 8절). 값으로 들어가는 것은 종류 이름·건수·검증된 앱 링크뿐이다.
// 방에는 받는 사람 이름도 문서 제목도 금액도 나가지 않는다. 건수가 2건 이상이면 문서 하나의 링크가 아니라 종류별 목록 링크를 쓴다.
func ComposeWebhook(kind string, count int, baseURL, firstLink string) (string, error) {
	k, ok := KindByCode(kind)
	if !ok {
		return "", fmt.Errorf("알 수 없는 알림 종류입니다")
	}
	if count < 1 {
		count = 1
	}
	path := firstLink
	if count > 1 {
		path = listLink(kind)
	}
	link, err := LinkURL(baseURL, path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%s %d건이 있습니다. 열기: %s", subjectPrefix, k.Name, count, link), nil
}

// listLink 는 같은 종류가 여러 건일 때 가리키는 앱 안 목록 경로다(문서 하나를 가리키지 않는다).
func listLink(kind string) string {
	switch kind {
	case "approval_pending", "approval_nudge":
		return "/approvals"
	}
	return "/notifications"
}
