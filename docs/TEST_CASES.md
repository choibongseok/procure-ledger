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

프론트(vitest)는 `npm --prefix web test` 출력의 Tests 줄이 기준이다.

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
