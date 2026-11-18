// 명령: eval — 평가 러너.
//
//	go run ./cmd/eval --provider mock          CI·make verify 용. 결정적이며 네트워크를 쓰지 않는다.
//	go run ./cmd/eval --provider anthropic     수동 실행. ANTHROPIC_API_KEY(설정의 키 환경변수)와 PL_LLM_MODEL 이 필요하다.
//	                                           키가 없으면 실패하지 않고 건너뛴다(종료 코드 0).
//	go run ./cmd/eval --set <코드>[@버전] --provider mock --db <파일>   회사 평가 세트(B46)를 돌리고 같은 저장 규칙을 쓴다(CI 는 mock·시험 세트만).
//	go run ./cmd/eval --live --provider mock,bedrock:<모델>,vertex:<모델>,openai:<모델>   공급자별 골든셋을 차례로 돌리고 비교 표를 낸다(B45).
//	                                           bedrock·vertex·openai 는 --live 와 환경변수(키)가 있을 때만 실제 호출한다(없으면 그 공급자만 건너뜀).
//	                                           bedrock: AWS_REGION·AWS_ACCESS_KEY_ID·AWS_SECRET_ACCESS_KEY(·AWS_SESSION_TOKEN)
//	                                           vertex:  GOOGLE_CLOUD_PROJECT·GOOGLE_CLOUD_LOCATION·GOOGLE_APPLICATION_CREDENTIALS(서비스 계정 JSON 파일 경로)
//	                                           openai:  OPENAI_COMPAT_BASE_URL(공개 https)·OPENAI_COMPAT_API_KEY
//
// 회귀 감지: 저장소에 커밋된 기준선(evals/baseline.json, 바이너리에 내장)과 케이스별로 비교해, 기준선에서
// 통과하던(또는 기준선에 없던) 케이스가 실패하면 종료 코드 1 이다. 기준선에서 사라진 케이스는 경고만 한다.
// 기본 실행은 비교만 한다. 기준선 갱신은 --update-baseline 으로만, 이력 저장은 --db 로만 한다(기본은 저장소·DB 를 건드리지 않는다).
//
// 문서 판정 골든셋과 MCP 도구 선택 케이스를 케이스별 표로 채점하고, 합격 기준(evals 패키지의 상수)에
// 못 미치면 종료 코드 1 이다. 종료 코드: 0 통과(또는 키 없음으로 건너뜀), 1 기준 미달, 2 실행 설정 오류.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	fs.SetOutput(errw)
	provider := fs.String("provider", "mock", "mock | anthropic | bedrock | vertex | openai (쉼표로 여러 개, 공급자:모델 형식으로 모델 지정)")
	model := fs.String("model", os.Getenv("PL_LLM_MODEL"), "공급자의 모델 ID(기본: PL_LLM_MODEL). 여러 공급자는 공급자:모델 로 따로 지정한다")
	keyEnv := fs.String("key-env", "ANTHROPIC_API_KEY", "anthropic API 키를 읽을 환경변수 이름")
	live := fs.Bool("live", false, "bedrock·vertex·openai 의 실제 외부 호출을 허용한다(키가 있을 때만 호출, 없으면 건너뜀)")
	only := fs.String("only", "", "docs | tools | policy | quotes | knowledge | journals (기본: 전부, journals 는 전표 서비스가 연결된 뒤에만)")
	baselinePath := fs.String("baseline", "", "비교할 기준선 파일(기본: mock 은 저장소에 커밋된 evals/baseline.json 내장본, 그 밖의 공급자는 비교 없음)")
	updateBaseline := fs.Bool("update-baseline", false, "합격한 이번 결과로 기준선 파일을 갱신한다(기본 실행은 비교만 한다)")
	dbPath := fs.String("db", "", "이 SQLite 파일에 실행 이력을 저장한다(관리자 화면 표의 원본). 비우면 저장하지 않는다")
	setArg := fs.String("set", "", "DB 의 회사 평가 세트를 돌린다: <코드>[@버전] (--db 필수, 기본 골든셋 대신 그 세트만, 응답 전문은 저장·출력하지 않는다)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *setArg != "" {
		return runSetMode(*setArg, *provider, *model, *keyEnv, *dbPath, *live, out, errw)
	}

	specs := splitProviders(*provider, *model)
	if len(specs) == 0 {
		fmt.Fprintln(errw, "공급자가 없습니다(--provider)")
		return 2
	}
	if *updateBaseline && (len(specs) != 1 || specs[0].name != "mock") {
		fmt.Fprintln(errw, "--update-baseline 은 mock 한 개일 때만 쓸 수 있습니다(외부 공급자 결과는 기준선 파일을 건드리지 않는다).")
		return 2
	}
	var results []runResult
	worst := 0
	for _, sp := range specs {
		prov, mdl, policy, skip, err := buildEvalProvider(sp, *keyEnv, *live)
		if err != nil {
			fmt.Fprintln(errw, "공급자 설정 오류:", err)
			return 2
		}
		if skip != "" {
			fmt.Fprintln(out, "건너뜀:", skip)
			continue
		}
		mp := &meteredProvider{Provider: prov}
		rep, err := Run(context.Background(), Config{Provider: mp, Model: mdl, PolicyPattern: policy, Only: *only})
		if err != nil {
			fmt.Fprintln(errw, "평가 실행 오류:", err)
			return 2
		}
		rep.Print(out, prov.Name())
		cost := mp.costMicro(envInt64("PL_LLM_PRICE_IN_MICRO_PER_MTOK"), envInt64("PL_LLM_PRICE_OUT_MICRO_PER_MTOK"))
		code := finish(rep, finishOptions{Provider: sp.name, Model: mdl, BaselinePath: *baselinePath, Update: *updateBaseline, Only: *only, DBPath: *dbPath,
			SuiteHash: suiteHashOf(rep), LatencyMS: mp.latency.Milliseconds(), CostMicro: cost}, out, errw)
		if code > worst {
			worst = code
		}
		results = append(results, runResult{name: sp.name, model: mdl, rep: rep, latencyMS: mp.latency.Milliseconds(), costMicro: cost})
	}
	if len(results) >= 2 {
		printCompare(out, results)
	}
	return worst
}
