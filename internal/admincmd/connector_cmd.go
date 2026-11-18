package admincmd

// 커넥터 실행 명령(B42 3단계 D13, 명세 11절). 구현은 internal/connector. init() 에서 commands 에 덧붙인다.
// 매핑 파일(JSON)은 운영자 디스크에 두고 서버에 올리지 않는다. 토큰은 환경변수 PL_CONNECTOR_TOKEN(연동 API 토큰 원문)으로만 받는다.
// 이 명령은 **연동 API 만** 쓴다: DB 를 열지 않고 서버 조립(Build)도 거치지 않는다(서버와 다른 호스트에서도 돌 수 있고 DB 접근 권한이 필요 없다).
// 서버 쪽 기록은 API 가 남긴다(api.import_apply·api.journal_draft·변경 피드·읽기 요약). 실행 결과는 표준 출력에 건수·결과 코드로만 나온다.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/choibongseok/procure-ledger/internal/connector"
)

func init() {
	commands = append(commands,
		command{"connector", "connector run --mapping 파일 --direction in|out [--dry-run] [--base-url URL] [--state 파일] [--allow-local]",
			"JSON 매핑 파일로 거래처·품목·전표를 연동 API 로 옮기거나(in) 읽어 CSV·REST 로 내보낸다(out). 토큰은 환경변수 PL_CONNECTOR_TOKEN. 결과는 표준 출력에 나오고 서버 쪽 기록은 연동 API 가 남긴다.", runConnector},
	)
}

func runConnector(ctx context.Context, args []string, out io.Writer) error {
	usage := errors.New("사용법: connector run --mapping 파일 --direction in|out [--dry-run] [--base-url URL] [--state 파일] [--allow-local]")
	if len(args) == 0 || args[0] != "run" {
		return usage
	}
	fs := flag.NewFlagSet("connector run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mapping := fs.String("mapping", "", "")
	direction := fs.String("direction", "", "")
	dry := fs.Bool("dry-run", false, "")
	baseURL := fs.String("base-url", "", "")
	state := fs.String("state", "", "")
	allowLocal := fs.Bool("allow-local", false, "")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || *mapping == "" || (*direction != "in" && *direction != "out") {
		return usage
	}
	m, err := connector.Load(*mapping)
	if err != nil {
		return err
	}
	if m.Direction != *direction {
		return fmt.Errorf("매핑 파일의 방향(%s)과 --direction(%s)이 다릅니다", m.Direction, *direction)
	}
	token := os.Getenv("PL_CONNECTOR_TOKEN")
	if token == "" {
		return errors.New("환경변수 PL_CONNECTOR_TOKEN 에 연동 API 토큰이 없습니다(토큰은 파일이나 인자로 받지 않습니다)")
	}
	base := *baseURL
	if base == "" {
		base = os.Getenv("PL_BASE_URL")
	}
	if base == "" {
		return errors.New("서버 주소가 없습니다(--base-url 또는 PL_BASE_URL)")
	}
	res, runErr := connector.Run(ctx, connector.Options{Mapping: m, BaseDir: filepath.Dir(*mapping), Client: &connector.Client{Base: base, Token: token}, DryRun: *dry, StatePath: *state, AllowLocal: *allowLocal})
	fmt.Fprintf(out, "커넥터 %s(%s %s): 결과 %s, 행 %d, 성공 %d, 실패 %d\n", m.Name, m.Direction, m.Target, res.Code, res.Total, res.OK, res.Failed)
	for _, msg := range res.Messages {
		fmt.Fprintln(out, "  - "+msg)
	}
	if runErr != nil {
		return runErr
	}
	if res.Code == connector.CodeRowErrors {
		return errors.New("행 오류가 있어 반영하지 않았습니다(위 목록을 고쳐 다시 실행하세요)")
	}
	return nil
}
