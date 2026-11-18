//go:build tools

// Package tools 는 1단계에서 쓸 의존성을 go.mod 에 미리 고정해 두는 자리다.
// 실제 코드에서 import 하게 되면 해당 줄은 지워도 된다.
//
//	go-sdk/mcp      : ai (cmd/erp-mcp, internal/mcp)
//	ledongthuc/pdf : ai (docverify 의 PDF 텍스트 추출, 순수 Go·cgo 없음)
package tools

import (
	_ "github.com/ledongthuc/pdf"
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
)
