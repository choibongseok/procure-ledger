# 서명 알림 수신기 예제 (NestJS)

ProcureLedger 의 변경 알림(웹훅)을 받는 NestJS(TypeScript) 서비스입니다. 서명을 검증하고, 이벤트 번호로 중복을 걸러내고, 알림 본문을 믿지 않고 변경 피드(`/api/v1/changes`)를 커서로 다시 읽어 상태를 맞춥니다. 서버 쪽 규칙은 [연동 가이드](../../docs/integrations.md) 3절에 있습니다.

## 왜 이렇게 만들었나

알림은 "무언가 바뀌었다"는 신호일 뿐이고 본문에 값도 없습니다. 그래서 수신기가 지켜야 할 것은 세 가지입니다.

| 문제 | 서버(Go) 쪽 규칙 | 이 예제의 처리 |
|---|---|---|
| 위조·변조·재생 | `X-PL-Signature: v1=<hex HMAC-SHA256(비밀, 시각 + "." + 본문 바이트)>`, 시각은 `X-PL-Timestamp`(`internal/webhooks.Sign`) | 파싱 전에 원본 바이트로 같은 식을 계산해 상수 시간으로 비교하고, 시각이 5분(`PL_WEBHOOK_TOLERANCE_SECONDS`) 안인지 봅니다. 맞지 않으면 401 |
| 비밀 교체 | 교체 뒤 24시간은 `v1=<새>,v1=<옛>` 두 서명을 함께 보냄 | 헤더의 서명을 모두 읽어 설정된 비밀(`PL_WEBHOOK_SECRET`, 교체 기간에는 `PL_WEBHOOK_SECRET_PREVIOUS`)과 모든 조합을 비교합니다. 새 비밀만 가져도, 옛 비밀만 가져도 교체 기간 헤더는 통과합니다 |
| 중복 | 전달은 적어도 한 번. 같은 이벤트는 같은 번호(`X-PL-Event-Id` = 본문 `id` = 피드 번호) | 번호로 처리 기록을 찾아 이미 끝난 번호는 200 으로 무시합니다 |
| 순서·누락 | 같은 객체의 이벤트만 번호 순서를 보장하고, 다른 객체끼리는 보장하지 않음 | 피드를 어디까지 읽었는지(커서)를 저장해 두고, 커서보다 오래된 번호는 기록만 합니다. 새 번호가 오면 커서 뒤를 피드에서 다시 읽으므로 빠진 알림도 함께 메워집니다 |
| 재시도 | 2xx 성공, 429·408·5xx 는 1분~24시간 간격으로 8번 재시도, 400·401·403·404·410 은 포기 | 서명·형식 오류는 401·400(재시도 없음), 피드를 읽지 못한 때만 503(서버가 다시 보냄) |

처리 순서는 이렇습니다.

```
POST /webhooks/procure-ledger
  1. 원본 바이트로 서명·시각 검증          실패 401
  2. 본문 해석, X-PL-Event-Id 와 본문 id 대조    실패 400
  3. 이미 처리한 번호?                      200 duplicate
  4. 번호를 "받음"으로 기록
  5. 커서 >= 번호?                          200 stale (기록만)
  6. GET /api/v1/changes?cursor=… 를 끝까지 읽어 객체 상태와 커서를 한 번에 갱신
     (읽지 못하면 503, 읽고도 번호에 못 닿으면 503)   성공 200 applied
  7. 번호를 "처리 완료"로 기록
```

"처리 완료"는 상태를 맞춘 뒤에야 표시합니다. 피드를 못 읽어 503 을 돌려주면 그 번호는 아직 완료가 아니므로, 서버가 다시 보냈을 때 중복으로 버려지지 않고 다시 처리됩니다. 객체 상태는 피드 줄의 번호가 더 클 때만 덮어써서, 늦게 도착한 낡은 줄이 상태를 되돌리지 못합니다.

피드 커서가 보존 기간을 지나 410 `cursor_expired` 가 나오면 현재 끝으로 커서를 다시 잡고 경고를 남깁니다. 그 사이의 변경은 이 예제가 따라잡지 않습니다. 연동 가이드의 "최초 적재" 절차(목록 API 전체 읽기)로 다시 채워야 합니다.

## 구조

```
src/
  config/          환경변수 읽기와 검증(문제를 모아서 한 번에 알림)
  store/           EventStore 인터페이스, 메모리 구현(기본), PostgreSQL 구현(선택)
  changes-client/  /changes·/changes/head 클라이언트(토큰은 Authorization 헤더로만)
  webhook/         컨트롤러, 서명 검증, 본문 해석, 이벤트 처리, 피드 동기화
  testing/         시험용 가짜 피드(빌드에 들어가지 않음)
test/              supertest e2e
scripts/           실제 서버와의 확인 스크립트
```

- 요청 본문은 `NestFactory.create(AppModule, { rawBody: true })` 로 원본을 보존하고, 컨트롤러가 그 바이트로 서명을 검증한 뒤에 직접 JSON 을 풉니다. 파싱된 객체를 다시 직렬화하면 서명이 어긋납니다.
- 로그는 Nest `Logger` 를 쓰고, 비밀·토큰·본문은 남기지 않습니다.
- `GET /healthz` 와 `GET /state`(맞춘 객체 상태와 커서, 점검용)가 있습니다. `/state` 는 인증이 없으므로 실제 배포에서는 닫거나 보호하세요.

## 실행

Node 22 이상이 필요합니다. 의존성 버전은 `package.json` 에 고정했고 `package-lock.json` 을 함께 둡니다.

```
cd examples/webhook-consumer-nest
npm ci
npm run build

export PL_API_BASE_URL=http://127.0.0.1:8080   # ProcureLedger 서버
export PL_API_TOKEN=pli_...                    # changes:read 범위의 연동 토큰
export PL_WEBHOOK_SECRET=whsec_...             # 구독을 등록할 때 한 번만 보이는 비밀
npm start
```

그다음 ProcureLedger 관리 화면(또는 `POST /api/admin/integrations/webhooks`)에서 이 수신기의 주소(`https://…/webhooks/procure-ledger`)로 구독을 등록합니다. 서버는 https 와 외부 주소만 허용하므로 로컬 시험에서는 서버를 `PL_NOTIFY_INSECURE_LOCAL=1` 로 띄웁니다. 설정 전체는 `.env.example` 에 있습니다(셸에서 내보내세요. 서비스가 `.env` 를 직접 읽지는 않습니다).

| 환경변수 | 뜻 | 기본 |
|---|---|---|
| `PL_API_BASE_URL`, `PL_API_TOKEN` | 서버 주소와 연동 토큰 | 필수 |
| `PL_WEBHOOK_SECRET` | 서명 비밀 | 필수 |
| `PL_WEBHOOK_SECRET_PREVIOUS` | 교체 기간(24시간)에만 두는 옛 비밀 | 없음 |
| `PL_WEBHOOK_TOLERANCE_SECONDS` | 서명 시각 허용 오차 | 300 |
| `PL_STORE`, `DATABASE_URL` | `memory` 또는 `postgres` | memory |
| `PL_INITIAL_CURSOR` | 저장된 커서가 없을 때 `head`(지금부터) 또는 `start`(처음부터) | head |
| `PORT` | 수신 포트 | 3000 |

비밀을 교체할 때는 새 비밀을 받아 `PL_WEBHOOK_SECRET` 에 넣고, 옛 비밀을 `PL_WEBHOOK_SECRET_PREVIOUS` 에 두었다가 24시간 뒤에 지웁니다. 서버가 교체 기간에 두 서명을 모두 보내므로 수신기를 끊김 없이 바꿀 수 있습니다.

PostgreSQL 저장소는 `PL_STORE=postgres DATABASE_URL=postgres://…` 로 켭니다. 테이블 3개(`pl_webhook_events`, `pl_feed_objects`, `pl_feed_cursor`)를 처음 기동할 때 만듭니다. 메모리 저장소는 재시작하면 비워지고 커서도 사라지므로 `PL_INITIAL_CURSOR` 규칙으로 다시 시작합니다.

### 한계

- 피드 동기화의 직렬화는 프로세스 안에서만 됩니다. 인스턴스를 여럿 띄우려면 커서 갱신에 DB 잠금(advisory lock 등)이 더 필요합니다.
- 받은 상태는 피드 줄(자원·번호·동작·상태)뿐입니다. 거래처 이름 같은 값이 필요하면 `/api/v1/vendors?id=…` 같은 읽기 API 를 더 부르면 됩니다(본문의 `api_url`).

## 시험

```
npm test            # 단위 43개(+ PostgreSQL 계약 3개는 아래 환경변수가 있을 때만)
npm run test:e2e    # supertest e2e 16개, 변경 피드는 가짜로 대체(Go 서버 불필요)
npm run test:all    # 둘 다
```

저장소 루트에서는 `make test-examples`(설치·타입 검사·`test:all`)이고 `make verify` 의 6단계에도 들어 있습니다. 시험 목록은 [docs/TEST_CASES.md](../../docs/TEST_CASES.md) 3절에 1:1 로 있습니다.

- 단위: 서명 성공·실패·시각 만료·교체 기간, 본문 해석, 설정 검증, 저장소 계약, 피드 클라이언트, 중복·순서 역전·누락 복구·피드 실패 후 재전송·커서 만료·동시 알림.
- e2e: 같은 흐름을 HTTP 로. 서명은 서버와 같은 식으로 시험이 직접 계산해 보냅니다. 공백·키 순서가 다른 본문으로 원본 바이트 검증도 확인합니다.
- PostgreSQL 저장소 계약 시험은 빈 시험 DB 를 가리키는 `PL_TEST_PG_URL=postgres://… npm test` 일 때만 돕니다(테이블을 비웁니다. 운영 DB 를 가리키지 마세요).

### 실제 서버로 한 번 받아 보기

자동 시험은 서버를 가짜로 대체하므로, 실제 서명이 맞는지는 `make webhook-nest-live` 로 따로 확인합니다. 서버를 빌드해 개발 모드로 띄우고(임시 DB), 연동 API 를 켜고 토큰·구독을 만든 뒤 수신기를 띄워서 거래처를 등록하고, 서버가 보낸 서명 알림이 수신기에 반영되는지 봅니다. 비밀 교체 뒤 새 비밀만 가진 수신기로도 받아지는지까지 봅니다. 한 번 돌린 결과입니다(2026-11-18).

```
  통과    서버 기동
  통과    개발용 로그인(관리자·재무)
  통과    연동 API 켜기
  통과    연동 토큰 발급(changes:read)
  통과    알림 구독 등록(비밀 whsec_…)
  통과    수신기 기동
  통과    시험 알림(ping)을 수신기가 2xx 로 받음
  통과    거래처 1 등록 -> 서명된 알림 수신 -> 피드 재조회로 상태 반영
  통과    서명 비밀 교체
  통과    새 비밀만 가진 수신기 재기동
  통과    비밀 교체 뒤 거래처 2 알림도 수신(새 서명 검증)
  발송 기록(서버): {"sent":2}
  수신기 상태: {"cursor":2,"objects":["vendor#1@1","vendor#2@2"]}
```
