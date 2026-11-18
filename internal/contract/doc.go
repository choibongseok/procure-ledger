// Package contract 는 세 영역(platform, erp, ai)이 공유하는 계약이다.
//
// 시그니처 변경은 platform 에게 요청하고, platform 은 바꾼 뒤 영향받는 영역에 알린다.
//
// 구성과 구현 담당
//
//	contract            Principal, Role, Permission 표, 오류, 트랜잭션 헬퍼, 라우트·MCP 도구 등록기  (구현: platform)
//	contract/audit      audit.Recorder (해시 체인 감사 로그)                                        (구현: platform)
//	contract/llm        llm.Client                                                               (구현: ai, internal/llmgw)
//	contract/domain     도메인 타입과 서비스 인터페이스                                              (구현: erp / ai, 각 인터페이스 주석 참고)
//	contract/deps       핸들러·도구가 받는 의존성 묶음                                               (조립: cmd/server/wire_*.go)
//	contract/fake       위 인터페이스의 가짜 구현. erp·ai 테스트는 platform 구현 없이 이걸로 돈다.
//
// 이 패키지(루트)는 internal 하위 다른 패키지를 import 하지 않는다(순환 방지).
package contract
