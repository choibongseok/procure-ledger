package contract

import "context"

// SettingLLMImageTransmission 은 app_settings 의 키다. "true" 일 때만 이미지 증빙을 LLM 으로 보낸다.
const SettingLLMImageTransmission = "llm.image_transmission_enabled"

// SettingsManager 는 관리자 설정 저장소다. 구현: platform (internal/auth/settings.go).
// 읽기는 권한 검사 없이 서버 내부(docverify)가 쓴다. 쓰기는 llm:policy_manage(admin) 만 되고
// 변경 전후 값이 같은 트랜잭션에서 감사 로그(setting.update)에 남는다. 행이 없거나 읽기 실패는 꺼짐(fail-closed).
type SettingsManager interface {
	LLMImageTransmission(ctx context.Context) bool
	SetLLMImageTransmission(ctx context.Context, p Principal, enabled bool) error
}
