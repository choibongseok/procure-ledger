// Package dbmigrate 는 SQLite 데이터베이스 파일을 빈 PostgreSQL 데이터베이스로 옮기는 일회성 이관 도구의 본체다(B37 5단계, 명세 37 11절).
//
// 원칙: 원본은 읽기 전용으로만 연다. 대상은 한 트랜잭션으로 채우고, 검증이 모두 통과해야 커밋한다.
// 보호 트리거는 표 단위 `ALTER TABLE … DISABLE TRIGGER USER` 로 그 트랜잭션 안에서만 끄고 같은 트랜잭션에서 다시 켠다.
// 트리거를 영구히 우회하는 설정값·세션 변수는 만들지 않는다. 명령줄 연결(erp-admin db-migrate)은 이 패키지를 부르기만 한다.
package dbmigrate

// Skip 은 이관하지 않는 표와 그 사유다. 여기에 없는 표는 모두 이관한다(표 분류의 정본은 이 상수 한 곳이다).
// 제외 표는 임시·캐시 성격이라 이관하지 않아도 사용자가 다시 로그인하면 되는 것들뿐이다.
var Skip = map[string]string{
	"schema_migrations":     "마이그레이션 이력은 대상에서 이 도구가 PG 용 마이그레이션을 적용하며 만든다",
	"sessions":              "로그인 세션은 임시 값이다(이관 뒤 다시 로그인한다)",
	"oidc_auth_requests":    "진행 중인 로그인 요청은 임시 값이다",
	"mcp_rate_window":       "호출 제한 창은 임시 값이다",
	"mcp_user_rate_window":  "호출 제한 창은 임시 값이다",
	"login_tickets":         "일회용 로그인 표는 임시 값이다",
	"saml_requests":         "진행 중인 SAML 요청은 임시 값이다",
	"saml_assertions_seen":  "재사용 방지용 단기 기록이다",
	"password_reset_tokens": "일회용 비밀번호 재설정 링크는 임시 값이다",
}

// Classified 는 표 이름을 "이관"과 "제외"로 가른다(AC-1750).
func Classified(table string) (migrate bool, reason string) {
	if r, ok := Skip[table]; ok {
		return false, r
	}
	return true, ""
}
