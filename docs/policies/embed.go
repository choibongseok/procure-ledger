// Package policies 는 규정 문서(마크다운)를 바이너리에 내장한다.
// 운영에서 문서를 바꿀 때는 PL_POLICY_DIR 로 디스크 폴더를 지정하면 내장본 대신 그 폴더를 읽고, 내용이 바뀌면 다시 색인한다.
package policies

import "embed"

// FS 는 이 폴더의 *.md 다.
//
//go:embed *.md
var FS embed.FS
