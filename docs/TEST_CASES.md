# 테스트 케이스

자동화 테스트 현황입니다. 1절은 영역별 개수, 2절은 대표 시나리오와 실제 테스트 함수의 대응표입니다.

## 1. 영역별 개수

(`go test -list '.*' ./...` 로 센 값)

| 영역 | 패키지 | Go 테스트 함수 |
|---|---|---|
| 결재·구매·재고·재무 서비스 | internal/service, internal/repository | 565 |
| 조직도·인사 발령·CSV 반영 | internal/org | 105 |
| 인증·세션·토큰·SCIM | internal/auth, internal/apitoken, internal/scim, internal/scimtest | 115 |
| 권한·감사·개인정보·직무 분리 | internal/rbac, internal/access, internal/audit, internal/privacy, internal/sod | 93 |
| 연동 API·변경 피드·웹훅·커넥터 | internal/apiv1, internal/changes, internal/webhooks, internal/connector | 46 |
| AI: 게이트웨이·MCP·증빙 판정·지식 검색·비용 | internal/llmgw, internal/mcp, internal/docverify, internal/knowledge, internal/aicost | 265 |
| 평가 러너 | cmd/eval, evals | 31 |
| HTTP 통합(실제 서버 조립) | cmd/server | 157 |
| 백업·복구·설치·마이그레이션 | internal/backup, internal/onboarding, internal/setup, migrations | 96 |
| 저장소 규약 검사 | internal/cicheck | 30 |
| 합계(위 표에 없는 패키지 포함) | 전체 | 1864 |

프론트(vitest)는 `npm --prefix web test` 출력의 Tests 줄이 기준이다. 예제 수신기(`examples/webhook-consumer-nest`)의 Jest 시험은 위 Go·프론트 개수에 들어가지 않고 3절에 따로 있다.

## 2. 대표 시나리오

| ID | 영역 | 시나리오 | 입력/조건 | 기대 결과 | 자동화 테스트(파일::함수) | 종류 |
|---|---|---|---|---|---|---|
| EVT-01 | 변경 피드 | 커서로 읽는 소비자가 커밋된 행을 놓치지 않는다 | 동시 쓰기 중 커서 기반 읽기 | 커밋된 변경이 빠짐없이 한 번 이상 나온다 | `internal/changes/changes_test.go::TestCursorConsumerNeverMissesCommittedRows` | integration |
| EVT-02 | 변경 피드 | 커서 이후 조회·필터·건수 상한·오류 | after 커서, 자원 필터, limit | 상한과 필터가 지켜지고 잘못된 커서는 오류 | `internal/changes/changes_test.go::TestAfterCursorFilterLimitAndErrors` | unit |
| EVT-03 | 변경 피드 | 업무 변경이 피드에 빠짐없이 기록된다(속성 시험) | 여러 도메인 쓰기를 임의 순서로 실행 | 쓰기마다 변경 이벤트가 대응한다 | `internal/service/feed_completeness_test.go::TestChangeFeedCompletenessProperty` | integration |
| EVT-04 | 웹훅 | 전송 실패 뒤 재시도로 성공 | 수신 측이 처음 몇 번 실패 | 백오프로 재시도해 결국 전달 | `internal/webhooks/webhooks_test.go::TestRetryBackoffThenSuccess` | unit |
| EVT-05 | 웹훅 | 재시도 한도 소진 | 수신 측이 계속 실패 | 재시도 8회 뒤 포기하고 상태를 남김 | `internal/webhooks/webhooks_test.go::TestRetryExhaustedGivesUpAfterEightRetries` | unit |
| EVT-06 | 웹훅 | 서명 비밀 교체 중 이중 서명 | 비밀 교체 직후 24시간 | 새·옛 서명이 함께 붙어 수신 측이 무중단 전환 | `internal/webhooks/webhooks_test.go::TestSecretRotationDualSignatureFor24Hours` | unit |
| EVT-07 | 웹훅 | 분당 전송 한도 | 한도를 넘는 이벤트 | 실패 처리하지 않고 뒤로 미룬다 | `internal/webhooks/webhooks_test.go::TestPerMinuteLimitDefersWithoutFailing` | unit |
| AUD-01 | 감사 | 해시 체인이 이어지고 검증된다 | 여러 기록 추가 뒤 검증 | 체인 검증 통과 | `internal/audit/store_test.go::TestChainLinksAndVerifies` | unit |
| AUD-02 | 감사 | 한 행을 바꾸면 그 지점을 알려 준다 | 기록 한 행을 직접 수정 | 검증이 변조된 번호를 보고 | `internal/audit/store_test.go::TestTamperedRowIsDetectedAtThatSeq` | unit |
| AUD-03 | 감사 | 체인 전체를 다시 계산해도 외부 앵커가 잡는다 | 전체 해시를 재계산해 덮어씀 | 앵커 대조에서 불일치 검출 | `internal/audit/anchor_test.go::TestAnchorDetectsFullChainRecomputation` | unit |
| AUD-04 | 감사 | 동시 기록에도 체인이 갈라지지 않는다 | 동시에 여러 기록 | 체인이 한 줄로 이어짐 | `internal/audit/concurrency_test.go::TestConcurrentRecordsKeepChainIntact` | integration |
| APR-01 | 결재 | 합의는 위임할 수 없다 | 합의 단계에서 위임 시도 | 거부 | `internal/service/doc_service_test.go::TestAgreeCannotBeDelegated` | unit |
| APR-02 | 결재 | 대기 단계 결재자 재지정 뒤 이전 위임자는 권한을 잃는다 | 관리자가 대기 단계를 재지정 | 이전 위임자는 승인할 수 없다 | `internal/service/reassign_test.go::TestReassignWaitingStepAndOldDelegateLosesEligibility` | integration |
| APR-03 | 결재 | HTTP 로 자기 결재를 시도하면 403 | 기안자가 자기 문서 승인 요청 | 403 | `internal/handler/erp_http_test.go::TestHTTPSelfApprovalIs403` | integration |
| APR-04 | 예산 | 예산 경계와 재무 단계 강제 | 예산 잔액 경계 금액으로 상신 | 초과면 결재선에 재무 단계가 더해진다 | `internal/service/b25_budget_test.go::TestBudgetBoundaryAndForcedFinance` | integration |
| APR-05 | 예산 | 동시 상신을 순서대로 판정 | 같은 예산에 동시 상신 | 초과 판정이 직렬화되어 일관 | `internal/service/b25_budget_test.go::TestBudgetConcurrentSubmitJudgedInOrder` | integration |
| INV-01 | 재고·재무 | 월 마감은 법인별이다 | 두 법인 중 한쪽만 마감 | 다른 법인의 쓰기는 막히지 않는다 | `internal/service/entity_isolation_test.go::TestEntityMonthCloseIsPerEntity` | integration |
| INV-02 | 재고·재무 | 마감된 달에는 전표를 쓸 수 없다 | 마감된 달 날짜로 전표 기록 | 거부 | `internal/service/b31_journal_test.go::TestJournalMonthCloseLock` | integration |
| AI-01 | LLM 게이트웨이 | 요청은 공급자에 가기 전에 마스킹된다 | 개인정보가 든 프롬프트 | 공급자가 받는 요청에는 원문이 없다 | `internal/llmgw/gateway_test.go::TestPromptIsMaskedBeforeProvider` | unit |
| AI-02 | LLM 게이트웨이 | 공급자 5xx 가 재시도 뒤 대체 공급자로 넘어간다 | 주 공급자가 5xx 반복 | 재시도 후 대체 공급자로 응답 | `internal/llmgw/fallback_test.go::TestFallbackAfterRetriesOn5xx` | unit |
| AI-03 | LLM 게이트웨이 | 대체 모델도 모델 허용 정책을 통과해야 한다 | 허용 목록 밖 대체 모델 | 대체 불가 | `internal/llmgw/fallback_test.go::TestFallbackModelMustPassPolicy` | unit |
| AI-04 | MCP | 권한 없는 도구 호출은 거부되고 감사에 남는다 | 권한 없는 사용자의 규정 검색 호출 | 거부 + 감사 기록 | `internal/mcp/policy_test.go::TestSearchPolicyDeniedWithoutPermissionIsAudited` | unit |
| AI-05 | MCP | 호출 한도 초과는 거부되고 감사에 남는다 | 분당 한도 초과 호출 | 거부 + 감사 기록 | `internal/mcp/rate_test.go::TestRateLimitDeniesAndAudits` | unit |
| API-01 | 연동 API | 연동 토큰 범위에 위험 권한이 없다 | 범위별 허용 권한 표 | 승인·지급·입출고 등은 어떤 범위에도 없다 | `internal/apiv1/apiv1_test.go::TestScopesNeverGrantRiskyPermissions` | unit |
| OPS-01 | 저장소 규약 | 실행 파일·대용량 파일이 추적되지 않는다 | 추적 파일 검사 | 없음 | `internal/cicheck/binary_guard_test.go::TestNoBinariesOrHugeFilesTracked` | unit |

## 3. 예제: NestJS 서명 알림 수신기

`examples/webhook-consumer-nest` 의 Jest 시험이다. 단위 43개(`npm test`)와 e2e 16개(`npm run test:e2e`)이고, 아래 표의 행과 1:1 이다(합계 59행). `make test-examples`(`make verify` 의 6단계)가 둘 다 돈다. 저장소 계약 시험 3개는 같은 함수를 PostgreSQL 로도 돌리며(`PL_TEST_PG_URL` 이 있을 때만, 없으면 건너뜀) 표에는 한 행으로 적었다. 실제 서버와의 확인은 자동 시험이 아니라 `make webhook-nest-live` 스크립트이고 설명은 예제 README 에 있다.

| ID | 영역 | 시나리오 | 입력/조건 | 기대 결과 | 자동화 테스트(파일::함수) | 종류 |
|---|---|---|---|---|---|---|
| WHN-01 | 수신기 예제: 서명 검증 | 서버 구현과 같은 서명 값 | 비밀 whsec_new, 시각 1700000000, 본문 고정 | openssl 로 구한 값과 같다 | `examples/webhook-consumer-nest/src/webhook/signature.spec.ts::서명 검증 서버 구현과 같은 값이다(고정 벡터)` | unit |
| WHN-02 | 수신기 예제: 서명 검증 | 맞는 서명 통과 | 현재 비밀로 만든 v1 서명 | 통과 | `examples/webhook-consumer-nest/src/webhook/signature.spec.ts::서명 검증 맞는 서명은 통과한다` | unit |
| WHN-03 | 수신기 예제: 서명 검증 | 본문 변조 거부 | 본문 한 글자 변경 | mismatch 로 거부 | `examples/webhook-consumer-nest/src/webhook/signature.spec.ts::서명 검증 본문이 한 바이트라도 바뀌면 거부한다` | unit |
| WHN-04 | 수신기 예제: 서명 검증 | 다른 비밀 거부 | 다른 비밀로 만든 서명 | mismatch 로 거부 | `examples/webhook-consumer-nest/src/webhook/signature.spec.ts::서명 검증 다른 비밀로 만든 서명은 거부한다` | unit |
| WHN-05 | 수신기 예제: 서명 검증 | 길이가 다른 서명 값 | v1=abcd | 예외 없이 mismatch | `examples/webhook-consumer-nest/src/webhook/signature.spec.ts::서명 검증 길이가 다른 서명 값도 예외 없이 거부한다` | unit |
| WHN-06 | 수신기 예제: 서명 검증 | v1 이 아닌 서명 | v2=<서명> | mismatch 로 거부 | `examples/webhook-consumer-nest/src/webhook/signature.spec.ts::서명 검증 v1 이 아닌 서명만 있으면 거부한다` | unit |
| WHN-07 | 수신기 예제: 서명 검증 | 헤더 누락 | 시각 또는 서명 헤더 없음 | missing_header | `examples/webhook-consumer-nest/src/webhook/signature.spec.ts::서명 검증 헤더가 없으면 거부한다` | unit |
| WHN-08 | 수신기 예제: 서명 검증 | 시각 형식 오류 | 시각 17e8 | bad_timestamp | `examples/webhook-consumer-nest/src/webhook/signature.spec.ts::서명 검증 시각이 숫자가 아니면 거부한다` | unit |
| WHN-09 | 수신기 예제: 서명 검증 | 시각 허용 오차 경계 | 현재 시각 기준 ±300초, ±301초 | 300초는 통과, 301초는 expired | `examples/webhook-consumer-nest/src/webhook/signature.spec.ts::서명 검증 허용 오차 안(5분 경계)은 통과하고 넘으면 만료로 거부한다` | unit |
| WHN-10 | 수신기 예제: 서명 검증 | 만료된 시각(리플레이) | 1시간 전 시각, 서명은 맞음 | expired | `examples/webhook-consumer-nest/src/webhook/signature.spec.ts::서명 검증 만료된 시각은 서명이 맞아도 거부한다(리플레이 방지)` | unit |
| WHN-11 | 수신기 예제: 비밀 교체 | 새·옛 서명 동시 수신, 새 비밀 보유 | v1=새,v1=옛 / 설정은 새 비밀만 | 통과 | `examples/webhook-consumer-nest/src/webhook/signature.spec.ts::서명 검증 비밀 교체 기간 서버가 새·옛 서명을 함께 보내면 새 비밀만 가진 수신기도 통과한다` | unit |
| WHN-12 | 수신기 예제: 비밀 교체 | 새·옛 서명 동시 수신, 옛 비밀 보유 | v1=새,v1=옛 / 설정은 옛 비밀만 | 통과 | `examples/webhook-consumer-nest/src/webhook/signature.spec.ts::서명 검증 비밀 교체 기간 옛 비밀만 가진 수신기도 같은 헤더로 통과한다` | unit |
| WHN-13 | 수신기 예제: 비밀 교체 | 두 비밀 모두 허용 | 설정 새·옛, 서명은 한쪽 것 하나 | 통과 | `examples/webhook-consumer-nest/src/webhook/signature.spec.ts::서명 검증 비밀 교체 기간 수신기가 두 비밀을 모두 허용하면 어느 쪽 서명 하나만 와도 통과한다` | unit |
| WHN-14 | 수신기 예제: 비밀 교체 | 어느 비밀도 아님 | 설정 새·옛, 서명은 제3의 비밀 | mismatch | `examples/webhook-consumer-nest/src/webhook/signature.spec.ts::서명 검증 비밀 교체 기간 두 비밀 어느 쪽도 아니면 거부한다` | unit |
| WHN-15 | 수신기 예제: 본문 해석 | 서버가 보내는 모양 | id·type·resource·object_id·op·status | 필드가 읽힌다 | `examples/webhook-consumer-nest/src/webhook/payload.spec.ts::본문 해석 서버가 보내는 모양을 읽는다` | unit |
| WHN-16 | 수신기 예제: 본문 해석 | 잘못된 본문 | JSON 아님, 배열, id 문자열·음수, resource 빈 값, object_id 소수 | PayloadError | `examples/webhook-consumer-nest/src/webhook/payload.spec.ts::본문 해석 JSON 이 아니거나 필수 칸이 없거나 형이 다르면 PayloadError` | unit |
| WHN-17 | 수신기 예제: 본문 해석 | 시험 알림 본문 | type ping, test true | test 가 참인 본문으로 읽힌다 | `examples/webhook-consumer-nest/src/webhook/payload.spec.ts::본문 해석 시험 알림은 type 이 ping 이거나 test 가 참인 본문이다` | unit |
| WHN-18 | 수신기 예제: 설정 | 기본값 | 필수 3개 값만 설정 | 포트 3000, 오차 300초, 메모리 저장소, head 시작 | `examples/webhook-consumer-nest/src/config/config.spec.ts::설정 검증 필수 값만 있으면 기본값으로 채워진다` | unit |
| WHN-19 | 수신기 예제: 설정 | 옛 비밀 병기 | PL_WEBHOOK_SECRET_PREVIOUS 설정 | 현재 비밀 뒤에 붙는다 | `examples/webhook-consumer-nest/src/config/config.spec.ts::설정 검증 교체 기간의 옛 비밀은 현재 비밀 뒤에 붙는다` | unit |
| WHN-20 | 수신기 예제: 설정 | 오류 모아 알림 | 주소·토큰·비밀·포트·저장소가 모두 잘못됨 | 문제를 한 번에 모두 알린다 | `examples/webhook-consumer-nest/src/config/config.spec.ts::설정 검증 문제를 하나씩이 아니라 모아서 알린다` | unit |
| WHN-21 | 수신기 예제: 설정 | postgres 저장소 | PL_STORE=postgres, DATABASE_URL 없음/있음 | 없으면 오류, 있으면 통과 | `examples/webhook-consumer-nest/src/config/config.spec.ts::설정 검증 postgres 저장소는 DATABASE_URL 이 있어야 한다` | unit |
| WHN-22 | 수신기 예제: 설정 | 허용 오차 범위 | PL_WEBHOOK_TOLERANCE_SECONDS 0 / 60 | 0 은 오류, 60 은 통과 | `examples/webhook-consumer-nest/src/config/config.spec.ts::설정 검증 허용 오차 범위를 벗어난 값은 거부한다` | unit |
| WHN-23 | 수신기 예제: 저장소 | 이벤트 기록 덮어쓰기 | 같은 번호를 received 로 저장한 뒤 applied 로 저장 | 번호로 찾고 상태가 바뀐다(메모리·PostgreSQL) | `examples/webhook-consumer-nest/src/store/event-store.spec.ts::메모리 저장소 계약 이벤트 기록은 번호로 찾고 상태를 덮어쓸 수 있다` | unit |
| WHN-24 | 수신기 예제: 저장소 | 피드 쪽 반영 | 줄 3개와 커서 3 | 객체 상태와 커서가 함께 갱신(메모리·PostgreSQL) | `examples/webhook-consumer-nest/src/store/event-store.spec.ts::메모리 저장소 계약 피드 쪽을 반영하면 객체 상태와 커서가 함께 갱신된다` | unit |
| WHN-25 | 수신기 예제: 저장소 | 오래된 줄 무시 | 번호 9 뒤에 번호 4 의 줄 반영 | 객체 상태 유지, 커서 유지(메모리·PostgreSQL) | `examples/webhook-consumer-nest/src/store/event-store.spec.ts::메모리 저장소 계약 더 오래된 줄은 객체 상태를 덮어쓰지 않고 커서는 줄어들지 않는다` | unit |
| WHN-26 | 수신기 예제: 변경 피드 클라이언트 | 커서 형식 | c1.42 / x1.1 | 번호로 풀리고 잘못된 형식은 오류 | `examples/webhook-consumer-nest/src/changes-client/changes-client.spec.ts::변경 피드 클라이언트 커서 형식 c1.<번호> 를 주고받는다` | unit |
| WHN-27 | 수신기 예제: 변경 피드 클라이언트 | head 조회 | GET /api/v1/changes/head | 끝 번호를 돌려준다 | `examples/webhook-consumer-nest/src/changes-client/changes-client.spec.ts::변경 피드 클라이언트 head 는 현재 끝 번호를 돌려준다` | unit |
| WHN-28 | 수신기 예제: 변경 피드 클라이언트 | 목록 조회 | 커서 4, limit 50 | 토큰은 헤더로만, 줄·다음 커서·has_more 를 푼다 | `examples/webhook-consumer-nest/src/changes-client/changes-client.spec.ts::변경 피드 클라이언트 list 는 토큰을 Authorization 헤더로만 보내고 줄을 풀어 준다` | unit |
| WHN-29 | 수신기 예제: 변경 피드 클라이언트 | 커서 만료 | HTTP 410 | CursorExpiredError | `examples/webhook-consumer-nest/src/changes-client/changes-client.spec.ts::변경 피드 클라이언트 410 은 CursorExpiredError 로 구분한다` | unit |
| WHN-30 | 수신기 예제: 변경 피드 클라이언트 | 그 밖의 오류 | HTTP 429 rate_limited | 상태·코드를 담은 ChangesApiError | `examples/webhook-consumer-nest/src/changes-client/changes-client.spec.ts::변경 피드 클라이언트 그 밖의 오류는 상태 코드와 오류 코드를 담은 ChangesApiError 가 된다` | unit |
| WHN-31 | 수신기 예제: 변경 피드 클라이언트 | 연결 실패 | fetch 거부 | network 코드의 ChangesApiError | `examples/webhook-consumer-nest/src/changes-client/changes-client.spec.ts::변경 피드 클라이언트 연결 실패도 ChangesApiError 로 감싼다` | unit |
| WHN-32 | 수신기 예제: 변경 피드 클라이언트 | 응답 형식 오류 | 필수 칸 없는 200 응답 | 502 bad_response | `examples/webhook-consumer-nest/src/changes-client/changes-client.spec.ts::변경 피드 클라이언트 응답 형식이 어긋나면 502 로 알린다` | unit |
| WHN-33 | 수신기 예제: 알림 처리 | 피드 재조회로 상태 반영 | 본문 status 를 위조한 알림 | 상태는 피드 값, 이벤트는 applied | `examples/webhook-consumer-nest/src/webhook/webhook.service.spec.ts::알림 처리 알림을 받으면 본문이 아니라 피드를 읽어 객체 상태를 맞춘다` | unit |
| WHN-34 | 수신기 예제: 알림 처리 | 중복 이벤트 | 같은 이벤트 번호 두 번 | 두 번째는 duplicate, 피드를 다시 읽지 않는다 | `examples/webhook-consumer-nest/src/webhook/webhook.service.spec.ts::알림 처리 같은 이벤트 번호가 다시 오면 아무것도 하지 않는다` | unit |
| WHN-35 | 수신기 예제: 알림 처리 | 순서 역전 | 번호 3 처리 뒤 번호 1 도착 | stale 로 기록만, 상태는 되돌아가지 않는다 | `examples/webhook-consumer-nest/src/webhook/webhook.service.spec.ts::알림 처리 순서가 뒤집혀 커서보다 오래된 이벤트가 늦게 오면 기록만 한다` | unit |
| WHN-36 | 수신기 예제: 알림 처리 | 누락 복구 | 알림 2·3 이 오지 않고 4 만 도착 | 피드에서 2~4 가 함께 반영 | `examples/webhook-consumer-nest/src/webhook/webhook.service.spec.ts::알림 처리 빠진 알림은 다음 알림이 피드를 읽을 때 함께 메워진다` | unit |
| WHN-37 | 수신기 예제: 알림 처리 | 여러 쪽 읽기 | 줄 5개, 쪽 크기 2 | 세 번에 나눠 끝까지 읽는다 | `examples/webhook-consumer-nest/src/webhook/webhook.service.spec.ts::알림 처리 피드를 여러 쪽에 걸쳐 끝까지 읽는다` | unit |
| WHN-38 | 수신기 예제: 알림 처리 | 피드 조회 실패 뒤 재전송 | 첫 시도 피드 오류, 재시도는 정상 | 첫 시도는 오류(received), 재시도는 applied | `examples/webhook-consumer-nest/src/webhook/webhook.service.spec.ts::알림 처리 피드 조회가 실패하면 오류를 올리고, 같은 이벤트를 다시 받으면 처리한다` | unit |
| WHN-39 | 수신기 예제: 알림 처리 | 피드가 번호에 못 미침 | 피드는 비었는데 번호 5 알림 | FeedBehindError(재시도 유도) | `examples/webhook-consumer-nest/src/webhook/webhook.service.spec.ts::알림 처리 피드가 이벤트 번호에 닿지 못하면 FeedBehindError 로 재시도를 유도한다` | unit |
| WHN-40 | 수신기 예제: 알림 처리 | 커서 만료 | 410 cursor_expired | 현재 끝으로 커서를 다시 잡고 applied | `examples/webhook-consumer-nest/src/webhook/webhook.service.spec.ts::알림 처리 커서가 만료되면 현재 끝으로 다시 잡고 처리 완료로 본다` | unit |
| WHN-41 | 수신기 예제: 알림 처리 | head 에서 시작 | 저장된 커서 없음, 초기 커서 head | 이전 줄은 읽지 않는다 | `examples/webhook-consumer-nest/src/webhook/webhook.service.spec.ts::알림 처리 저장된 커서가 없고 head 로 시작하면 이전 줄은 읽지 않는다` | unit |
| WHN-42 | 수신기 예제: 알림 처리 | 동시 알림 | 알림 3건 동시 처리 | 피드를 겹쳐 읽지 않고 모두 반영 | `examples/webhook-consumer-nest/src/webhook/webhook.service.spec.ts::알림 처리 동시에 들어온 알림도 피드를 겹쳐 읽지 않고 모두 반영한다` | unit |
| WHN-43 | 수신기 예제: 알림 처리 | 시험 알림 | test true 본문 | 저장소·피드를 건드리지 않는다 | `examples/webhook-consumer-nest/src/webhook/webhook.service.spec.ts::알림 처리 시험 알림(ping)은 저장소를 건드리지 않는다` | unit |
| WHN-44 | 수신기 예제: HTTP | 서명된 알림 수신 | 서명된 알림 1건 | 200 applied, 상태 반영 | `examples/webhook-consumer-nest/test/webhook.e2e-spec.ts::POST /webhooks/procure-ledger 서명된 알림을 받으면 피드를 다시 읽어 상태를 맞춘다` | e2e |
| WHN-45 | 수신기 예제: HTTP | 원본 바이트 검증 | 공백·키 순서가 다른 JSON 본문에 서명 | 200(다시 직렬화하지 않고 원본으로 검증) | `examples/webhook-consumer-nest/test/webhook.e2e-spec.ts::POST /webhooks/procure-ledger 원본 바이트로 검증한다: 공백·키 순서가 다른 본문도 서명이 맞으면 통과한다` | e2e |
| WHN-46 | 수신기 예제: HTTP | 서명 오류 | 다른 비밀로 만든 서명 | 401 invalid_signature, 피드 조회 없음 | `examples/webhook-consumer-nest/test/webhook.e2e-spec.ts::POST /webhooks/procure-ledger 서명이 틀리면 401 이고 상태는 바뀌지 않는다` | e2e |
| WHN-47 | 수신기 예제: HTTP | 본문 변조 | 서명 뒤 본문의 object_id 변경 | 401 | `examples/webhook-consumer-nest/test/webhook.e2e-spec.ts::POST /webhooks/procure-ledger 본문을 바꾸면 서명이 어긋나 401 이다` | e2e |
| WHN-48 | 수신기 예제: HTTP | 서명 헤더 없음 | X-PL-Signature 없음 | 401 | `examples/webhook-consumer-nest/test/webhook.e2e-spec.ts::POST /webhooks/procure-ledger 서명 헤더가 없으면 401 이다` | e2e |
| WHN-49 | 수신기 예제: HTTP | 시각 만료 | 10분 전 시각으로 서명 | 401 | `examples/webhook-consumer-nest/test/webhook.e2e-spec.ts::POST /webhooks/procure-ledger 서명 시각이 5분을 벗어나면 서명이 맞아도 401 이다` | e2e |
| WHN-50 | 수신기 예제: HTTP | 비밀 교체 | 새·옛 서명을 함께 보냄, 수신기는 새 비밀만 | 200 | `examples/webhook-consumer-nest/test/webhook.e2e-spec.ts::POST /webhooks/procure-ledger 비밀 교체 기간: 서버가 새·옛 서명을 함께 보내면 새 비밀만 가진 수신기가 받는다` | e2e |
| WHN-51 | 수신기 예제: HTTP | 비밀 교체 | 수신기에 새·옛 비밀, 옛 서명만 도착 | 200 | `examples/webhook-consumer-nest/test/webhook.e2e-spec.ts::POST /webhooks/procure-ledger 비밀 교체 기간: 수신기에 새·옛 비밀을 모두 두면 옛 비밀 서명만 와도 받는다` | e2e |
| WHN-52 | 수신기 예제: HTTP | 멱등 | 같은 이벤트 두 번 | 두 번째 200 duplicate, 피드 조회 1회 | `examples/webhook-consumer-nest/test/webhook.e2e-spec.ts::POST /webhooks/procure-ledger 같은 이벤트를 다시 받으면 200 으로 무시한다` | e2e |
| WHN-53 | 수신기 예제: HTTP | 순서 역전 | 번호 3 뒤 번호 1 도착 | 200 stale, 상태 유지 | `examples/webhook-consumer-nest/test/webhook.e2e-spec.ts::POST /webhooks/procure-ledger 순서가 뒤집혀 늦게 온 낡은 이벤트는 200 으로 기록만 한다` | e2e |
| WHN-54 | 수신기 예제: HTTP | 누락 복구 | 알림 1·2 유실, 3 만 도착 | 객체 1·2·3 이 모두 반영 | `examples/webhook-consumer-nest/test/webhook.e2e-spec.ts::POST /webhooks/procure-ledger 빠진 알림은 다음 알림이 와서 피드를 읽을 때 복구된다` | e2e |
| WHN-55 | 수신기 예제: HTTP | 재시도 유도 | 피드 오류 중 알림, 복구 뒤 재전송 | 503 feed_unavailable 뒤 200 applied | `examples/webhook-consumer-nest/test/webhook.e2e-spec.ts::POST /webhooks/procure-ledger 피드를 읽지 못하면 503 을 돌려 서버가 다시 보내게 하고, 재전송은 처리된다` | e2e |
| WHN-56 | 수신기 예제: HTTP | 본문 검증 | 서명은 맞지만 형식이 틀린 본문 | 400 | `examples/webhook-consumer-nest/test/webhook.e2e-spec.ts::POST /webhooks/procure-ledger 서명은 맞지만 본문이 올바르지 않으면 400 이다` | e2e |
| WHN-57 | 수신기 예제: HTTP | 헤더 대조 | X-PL-Event-Id 와 본문 id 가 다름 | 400 | `examples/webhook-consumer-nest/test/webhook.e2e-spec.ts::POST /webhooks/procure-ledger X-PL-Event-Id 와 본문 id 가 다르면 400 이다` | e2e |
| WHN-58 | 수신기 예제: HTTP | 시험 알림 | ping 본문 | 200 ping, 피드 조회 없음 | `examples/webhook-consumer-nest/test/webhook.e2e-spec.ts::POST /webhooks/procure-ledger 시험 알림(ping)은 200 으로 받고 피드를 읽지 않는다` | e2e |
| WHN-59 | 수신기 예제: HTTP | 상태 점검 | GET /healthz | 200 {"ok":true} | `examples/webhook-consumer-nest/test/webhook.e2e-spec.ts::POST /webhooks/procure-ledger GET /healthz 는 200 이다` | e2e |
