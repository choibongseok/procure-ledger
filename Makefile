# ProcureLedger 빌드·검증 진입점.
# 로컬 검증은 Docker 없이 돈다. 완료 판정은 main 에서 돌린 `make verify` 출력으로만 한다.

# 셸에 남아 있는 GOROOT(다른 Go 버전)가 툴체인과 어긋나는 사고를 막는다.
unexport GOROOT

.PHONY: build web test verify verify-ui verify-docker verify-pg openapi openapi-check connector-demo clean

# -trimpath: 빌드한 사람의 절대 경로(홈 폴더 이름 등)가 바이너리에 남지 않게 한다(Dockerfile 과 같다).
build: web
	go build -trimpath -o bin/ ./cmd/...

web:
	npm --prefix web ci --no-audit --no-fund
	npm --prefix web run build

test:
	go test ./...

verify:
	bash scripts/verify.sh

# 선택 타깃(verify 에 포함되지 않는다): 헤드리스 브라우저(Playwright Chromium)로 빌드 산출물의 화면을 돈다(B14).
# 스크린샷은 ui-screenshots/(gitignore)에 남는다. 처음에는 Chromium 을 내려받는다.
verify-ui:
	bash scripts/verify-ui.sh

# 선택 타깃(verify 에 포함되지 않는다): 로컬에 설치된 PostgreSQL 의 임시 클러스터(initdb, 유닉스 소켓)로 PG 용 시험을 돈다(B37). Docker 불필요,
# 기존 PG 서버는 건드리지 않고, PG 가 없으면 "건너뜀"을 표시한다(PL_VERIFY_PG=required 이면 실패).
verify-pg:
	bash scripts/verify-pg.sh

# 선택 타깃(verify 에 포함되지 않는다): Docker 가 있는 머신에서 이미지 빌드·기동·헬스체크·dev IdP 꺼짐 확인.
verify-docker:
	bash scripts/verify-docker.sh

# 연동 API(B42) OpenAPI 문서: 라우트 선언표(internal/apiv1.Routes)에서 docs/openapi/v1.json 을 생성한다. openapi-check 는 저장소의 문서가 생성 결과와 같은지(D14)만 본다.
openapi:
	PL_UPDATE_OPENAPI=1 go test ./internal/apiv1 -run 'TestOpenAPIMatchesCommitted' -count=1

openapi-check:
	go test ./internal/apiv1 -run 'TestOpenAPIMatchesCommitted|TestRoutesAndOpenAPIAgree|TestErrorCodesMatch' -count=1

# 커넥터 예시(B42 3단계): examples/connector 의 매핑으로 실제 서버(시험용 조립) 위에서 거래처·전표를 들이고 거래처를 CSV 로 내보낸다. 설명은 examples/connector/README.md.
connector-demo:
	go test ./cmd/server -run 'TestConnectorDemo' -count=1 -v

clean:
	rm -rf bin
	find web/dist -mindepth 1 ! -name .gitkeep -delete
