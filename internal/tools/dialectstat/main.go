// dialectstat 는 SQLite 전용 구문의 개수를 다시 센다(B37 명세 3.2·3.3 표 갱신용).
//
//	go run ./internal/tools/dialectstat            # 저장소 루트에서
//	go run ./internal/tools/dialectstat -root .    # 다른 위치에서
//
// 출력은 마크다운 표다. 기준선 시험(internal/dbutil)과 같은 계산을 쓴다.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/choibongseok/procure-ledger/internal/dbutil/dialectscan"
)

func main() {
	root := flag.String("root", ".", "저장소 루트")
	flag.Parse()
	mig, err := dialectscan.Migrations(filepath.Join(*root, "migrations"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "dialectstat:", err)
		os.Exit(1)
	}
	pkgs, err := dialectscan.GoSource(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dialectstat:", err)
		os.Exit(1)
	}
	fmt.Print(dialectscan.Markdown(mig, pkgs))
}
