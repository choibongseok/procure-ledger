package web

import (
	"embed"
)

// 감사 로그 화면의 한국어 라벨 정본(TypeScript)을 서버도 읽을 수 있게 임베드한다(B28 감사 로그 내보내기).
// 라벨 표를 Go 에 한 벌 더 두지 않는다: 새 감사 동작·키의 라벨은 이 파일에만 더하면 화면과 내보내기에 함께 나온다.
//
//go:embed src/admin/auditLabels.ts src/admin/targetTypes.ts
var labelFS embed.FS

// AuditLabelSources 는 auditLabels.ts 와 targetTypes.ts 의 원문이다.
func AuditLabelSources() (labels, targets string) {
	a, _ := labelFS.ReadFile("src/admin/auditLabels.ts")
	b, _ := labelFS.ReadFile("src/admin/targetTypes.ts")
	return string(a), string(b)
}
