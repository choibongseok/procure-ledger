// Package web 은 프론트 빌드 산출물(web/dist)을 Go 바이너리에 임베드한다.
// `make build` 가 npm 빌드를 먼저 돌린다. dist 에는 .gitkeep 만 커밋되어 있어 go test 가 프론트 빌드 없이 돈다.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist 는 dist 디렉터리를 루트로 하는 파일 시스템이다.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // 임베드 경로는 컴파일 시점에 고정이라 발생하지 않는다.
	}
	return sub
}
