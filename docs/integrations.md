# 연동 가이드

외부 시스템(회계 프로그램·BI·사내 도구)이 ProcureLedger 와 데이터를 주고받는 방법입니다. 이 문서는 연동을 만드는 개발자와 연동을 허락하는 관리자를 위한 것입니다. 명세 정본은 해당 기능 명세, 필드 정의의 정본은 OpenAPI 문서 `docs/openapi/v1.json`(서버의 `GET /api/v1/openapi.json` 과 같은 내용)입니다.

연동은 세 갈래입니다.

| 하고 싶은 일 | 쓰는 것 |
|---|---|
| 품목·거래처·발주·입고·전표·채무를 읽는다 | 읽기 API(`/api/v1/...`)와 변경 피드 |
| 바뀐 것을 바로 알고 싶다 | 변경 웹훅(알림만 오고 값은 API 로 읽는다) |
| 거래처·품목을 들이거나 전표 초안을 올린다 | 쓰기 API 나 커넥터 예시 |

## 1. 시작하기

1. 관리자가 연동 관리 화면에서 **API 를 켭니다**(켜는 값은 환경변수가 아니라 화면 설정입니다). 허용할 출발지를 좁히려면 서버 환경에 `PL_API_ALLOW_CIDR` 를 정합니다.
2. 관리자가 **토큰을 발급**합니다. 이름·범위·유효 기간(1~365일)·허용 출발지를 고릅니다. 토큰 원문(`pli_` 로 시작)은 **발급 때 한 번만** 보입니다. 잃으면 폐기하고 새로 발급합니다.
3. 요청마다 `Authorization: Bearer <토큰>` 헤더를 보냅니다. 쿠키나 개인 토큰은 인정하지 않습니다.

범위는 필요한 만큼만 고릅니다.

| 범위 | 할 수 있는 일 |
|---|---|
| `items:read` `vendors:read` `purchase_orders:read` `receipts:read` `journals:read` `payables:read` `tax_invoices:read` | 해당 자원 읽기. 발주·입고는 전사 범위입니다. 세금계산서는 사업자번호가 항상 가려서 나가고 이메일·주소·대표자·품목 이름은 없습니다 |
| `changes:read` | 변경 피드. 읽기 범위를 가진 자원의 줄만 보입니다 |
| `vendors:write` `items:write` | 거래처·품목 가져오기(미리보기 후 반영) |
| `journals:draft` | 전표 초안 제출(초안까지만) |

토큰은 발급한 사람의 권한을 물려받지 않고 범위가 정한 권한만 가집니다. 승인·지급·입출고·구매요청은 어떤 범위에도 없습니다. 거래처의 사업자번호·계좌·전화·이메일은 읽기 응답에서 **항상 가려진 값**이고, 사람 정보(작성자·승인자·사번)는 어떤 응답에도 없습니다.

## 2. 읽기

```
curl -H "Authorization: Bearer $TOKEN" "http://localhost:8080/api/v1/vendors?limit=200&after=0"
```

- 목록은 번호(`id`) 오름차순입니다. 응답의 `next_cursor` 를 `cursor` 로 넘겨 계속 읽고, `has_more` 가 거짓이면 끝입니다. 한 쪽은 최대 200건입니다.
- 금액·수량은 정수(원, 개), 시각은 RFC3339 UTC, 필드 이름은 `snake_case` 입니다.

### 최초 적재와 증분

1. 적재를 시작하기 **전에** `GET /api/v1/changes/head` 로 현재 커서를 받아 둡니다.
2. 자원별 목록을 `after=0` 부터 끝까지 읽어 적재합니다.
3. 받아 둔 커서로 `GET /api/v1/changes?cursor=<커서>` 를 읽어 적재 중에 바뀐 것과 이후 변경을 따라갑니다. 응답의 `next_cursor` 를 저장해 다음에 이어 읽습니다.
4. 피드의 한 줄은 자원·번호·동작(`created`·`updated`·`status_changed`·`canceled`)·새 상태뿐이고 값은 없습니다. 바뀐 객체는 목록 API 로 다시 읽습니다. 같은 객체를 두 번 읽어도 결과가 같으므로 안전합니다.
5. 커서가 너무 오래되면 `410 cursor_expired` 입니다(보존 기간 지남). 1번부터 전체 적재를 다시 합니다.

피드는 토큰당 초당 5회까지 읽을 수 있습니다.

## 3. 변경 웹훅

관리자가 웹훅 구독을 등록하면(이름·주소·받을 이벤트 종류) 변경이 생길 때 서버가 그 주소로 서명한 요청을 보냅니다. 서명 비밀은 구독을 등록할 때 **한 번만** 보입니다.

- 요청 본문에는 값이 없습니다: `id`(이벤트 번호)·`type`(예: `journal.status_changed`)·`resource`·`object_id`·`op`·`status`·`occurred_at`·`api_url`. 값은 API 로 읽습니다.
- 주소는 https 여야 하고 내부 주소(사설·루프백 등)는 쓸 수 없습니다. 관리자가 허용 호스트 목록을 정하면 그 밖의 호스트는 거부됩니다.
- 연동 API 가 만든 변경은 기본으로 받지 않습니다(되돌림 고리 방지). 구독 옵션으로 받을 수 있습니다.

### 헤더

| 헤더 | 뜻 |
|---|---|
| `X-PL-Event-Id` | 이벤트 번호(중복 제거 키) |
| `X-PL-Event-Type` | 자원.동작 |
| `X-PL-Delivery` | 발송 건 번호(재시도해도 같음) |
| `X-PL-Timestamp` | 서명에 쓴 초 단위 Unix 시각 |
| `X-PL-Signature` | `v1=<hex>` (비밀 교체 직후 24시간은 `v1=<새>,v1=<옛>`) |
| `X-PL-Coalesced` | 있으면, 같은 객체의 짧은 시간 연속 변경 몇 건을 한 건으로 합쳤다는 뜻 |

### 서명 검증

서명은 `HMAC-SHA256(비밀, 시각 + "." + 본문 바이트)` 의 16진수입니다. 받는 쪽은 (1) 시각이 현재와 5분 안인지 보고(리플레이 방지, 서버 시계를 NTP 로 맞춰 두세요), (2) 서명을 상수 시간으로 비교하고, (3) `X-PL-Event-Id` 로 중복을 거릅니다.

Go

```go
func verify(secret string, ts string, body []byte, header string) bool {
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || math.Abs(float64(time.Now().Unix()-n)) > 300 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	for _, part := range strings.Split(header, ",") { // 교체 직후에는 서명이 둘이다
		if got, ok := strings.CutPrefix(strings.TrimSpace(part), "v1="); ok && hmac.Equal([]byte(got), []byte(want)) {
			return true
		}
	}
	return false
}
```

Python(표준 라이브러리)

```python
import hmac, hashlib, time

def verify(secret: str, ts: str, body: bytes, header: str) -> bool:
    if abs(time.time() - int(ts)) > 300:
        return False
    want = hmac.new(secret.encode(), ts.encode() + b"." + body, hashlib.sha256).hexdigest()
    return any(hmac.compare_digest(p.strip()[3:], want) for p in header.split(",") if p.strip().startswith("v1="))
```

셸(`openssl`, 시험용)

```
printf '%s.%s' "$TS" "$BODY" | openssl dgst -sha256 -hmac "$SECRET" -hex
```

### 재시도·중복·순서

- 2xx 로 응답하면 성공입니다. 429·408·5xx·연결 오류는 1분·5분·30분·2시간·6시간·12시간·24시간 간격으로 총 8번 다시 보냅니다. 응답의 `Retry-After`(최대 1시간)는 존중합니다. 400·401·403·404·410 등은 재시도하지 않고 그 건을 포기합니다.
- 전달은 **적어도 한 번**입니다. 같은 이벤트가 두 번 올 수 있으니 `X-PL-Event-Id` 로 거르세요.
- 같은 구독 안에서 **같은 객체**의 이벤트는 번호 순서로 옵니다(앞 건이 대기·전송 중이면 뒤 건이 기다립니다). 다른 객체끼리의 순서는 보장하지 않습니다. 순서가 어긋났는지 걱정되면 `api_url` 로 읽은 최신 상태가 정본입니다.
- 포기한 건이 연속 5건이면 구독이 자동으로 중지되고 관리자에게 알림이 갑니다. 원인을 고친 뒤 관리자가 재개하고, 포기된 건은 관리자가 다시 보낼 수 있습니다. 피드는 그대로라 증분 조회로 따라잡아도 됩니다.
- 구독마다 분당 발송 상한이 있고(기본 60), 넘는 건은 다음 주기로 밀립니다.
- 서명 비밀이 샜다고 의심되면 관리자가 비밀을 다시 발급합니다. 24시간은 옛 비밀과 새 비밀 서명이 함께 가므로 끊김 없이 바꿀 수 있습니다.

## 4. 쓰기

쓰기는 두 가지뿐입니다. 서비스 계층의 같은 함수를 거치므로 화면에서 할 때와 같은 검증·감사·권한 분리 규칙이 적용됩니다. 토큰마다 **분당 30회**까지 쓸 수 있고(읽기 한도와 따로), 넘으면 `429` 와 `Retry-After`(초)가 옵니다.

### 거래처·품목 가져오기

두 단계입니다. 미리보기는 아무것도 반영하지 않습니다.

```
POST /api/v1/imports/vendors/preview
{ "rows": [ { "name": "샘플상사", "business_no": "555-99-00011", "payment_days": 45 } ] }
```

응답의 `counts`(추가·변경·동일·오류)와 행별 판정(`rows`)을 확인합니다. 오류 행이 있으면 `apply_token` 이 없고 반영할 수 없습니다. 행의 키는 가져오기 양식의 열 식별자입니다(거래처 `name`·`business_no`·`payment_days`·`phone`·`email`·`account_no`, 품목 `sku`·`name`·`unit`·`safety_stock`·`lot_tracked`). 미리보기 응답의 변경 내용에서도 사업자번호·계좌·연락처는 가려진 값입니다.

```
POST /api/v1/imports/vendors/batches/<batch_no>/apply
Idempotency-Key: 2026-11-13-vendors-1
{ "apply_token": "<미리보기의 apply_token>", "rows": [ …미리보기와 같은 행… ] }
```

`apply_token` 은 한 번만 쓸 수 있고 24시간 안에 써야 합니다. 서버는 같은 행인지 다시 확인하고 한 트랜잭션으로 반영합니다(일부만 반영되는 일이 없습니다). 품목은 `/api/v1/imports/items/...` 입니다. 한 번에 최대 5,000행, 본문은 5MB 까지입니다.

### 전표 초안

```
POST /api/v1/journals
Idempotency-Key: ERP-1001
{ "posting_date": "2026-11-10", "memo": "소모품 구입", "external_ref": "ERP-1001",
  "lines": [ { "account_code": "5220", "side": "D", "amount": 12000 },
             { "account_code": "1010", "side": "C", "amount": 12000 } ] }
```

- 차변 합계와 대변 합계가 같아야 합니다. 같지 않으면 `400 invalid_argument` 로 거부됩니다(화면의 임시 저장과 달리 연동 초안은 처음부터 균형이 맞아야 합니다).
- API 는 **초안까지만** 만듭니다. 승인 요청·승인·취소 경로가 없습니다. 재무가 화면에서 검토해 승인을 요청하고 전자결재로 승인합니다.
- 초안의 작성자는 토큰을 발급한 사람입니다. 한 사람이 전표를 작성하고 최종 승인까지 하면 권한 분리 규칙(`jv.create_approve`)이 판정하므로, **토큰 발급자는 전표를 승인하는 사람이 아니어야** 합니다.
- `external_ref`(1~64자)가 같은 요청은 멱등입니다. 같은 토큰이 같은 값을 같은 내용으로 다시 보내면 기존 초안을 돌려주고(200), 내용이 다르면 `409 conflict` 입니다.
- 마감된 달의 전기일은 `409` 로 거부되고 아무것도 남지 않습니다.

### 멱등 키

쓰기 `POST` 는 `Idempotency-Key`(1~64자의 영문·숫자·`_ . : -`) 헤더를 받습니다. 같은 키로 같은 본문을 다시 보내면 첫 응답을 그대로 돌려주고 `Idempotent-Replayed: true` 헤더를 붙입니다(24시간). 같은 키로 다른 본문을 보내면 `409 conflict` 입니다. 네트워크 오류로 결과를 모를 때 같은 키로 안전하게 다시 보낼 수 있습니다.

## 5. 커넥터 예시

CSV 와 연동 API 사이를 JSON 매핑 파일로 옮기는 예시가 `examples/connector/` 에 있습니다. 연동 API 가 닿는 호스트에서 `erp-admin connector run --mapping <파일> --direction in|out [--dry-run]` 로 실행하고, 토큰은 환경변수 `PL_CONNECTOR_TOKEN` 으로만 받습니다. 변환은 닫힌 목록(`trim`·`upper`·`int`·`digits_to_dashed_biz`·`date_ymd`·`map`·`const`)이라 설정 파일이 코드 실행 경로가 되지 않습니다. 시험은 `make connector-demo` 로 한 번에 돌릴 수 있고, 자세한 설명은 `examples/connector/README.md` 에 있습니다.

## 6. 오류와 한도

오류 응답은 `{"error": {"code", "message", "request_id"}}` 입니다. `request_id` 를 문의할 때 알려 주세요.

| 상태 | `code` | 뜻과 대응 |
|---|---|---|
| 400 | `invalid_argument` | 요청 값이 올바르지 않습니다. `message`(한국어)를 보고 고칩니다 |
| 401 | `unauthenticated` | 토큰이 없거나 틀렸거나 폐기·만료되었습니다. 인증 실패가 몰리면 출발지별로 잠시 막힙니다 |
| 403 | `forbidden_scope` | 토큰의 범위 밖이거나 허용 출발지 밖입니다 |
| 403 | `api_disabled` | 관리자가 API 를 껐습니다 |
| 404 | `not_found` | 대상이 없습니다 |
| 409 | `conflict` | 충돌입니다: 마감된 달, 같은 `external_ref` 의 다른 내용, 같은 `Idempotency-Key` 의 다른 본문 |
| 410 | `cursor_expired` | 피드 커서가 보존 기간을 지났습니다. 전체 적재부터 다시 합니다 |
| 413 | `invalid_argument` | 본문이 5MB 를 넘었습니다 |
| 429 | `rate_limited` | 호출 한도를 넘었습니다. `Retry-After` 초만큼 기다립니다 |
| 5xx | `unavailable` | 서버 오류입니다. 잠시 뒤 다시 시도합니다 |

한도는 토큰당 분당 읽기 600회(관리 화면에서 바꿀 수 있음)와 쓰기 30회, 서버 전체 동시 요청 상한, 피드 조회 초당 5회입니다. 429 를 받으면 `Retry-After` 를 지켜 다시 보내세요.

## 7. 보안 점검표

- 토큰은 환경변수나 비밀 저장소에 두고 소스·설정 파일·채팅에 붙이지 않습니다. 필요한 범위만, 짧은 유효 기간으로 발급합니다.
- 가능하면 토큰마다 허용 출발지(CIDR)를 좁히고, 서버 전체 제한은 `PL_API_ALLOW_CIDR` 로 정합니다.
- 전표 초안 토큰의 발급자는 전표를 승인하는 사람이 아니어야 합니다.
- 웹훅 수신 쪽은 서명을 상수 시간으로 비교하고, 시각 편차 5분을 확인하고, `X-PL-Event-Id` 로 중복을 걸러냅니다. 비밀이 샜다고 의심되면 즉시 다시 발급합니다.
- 수신 주소는 https 이고 인터넷에서 닿는 호스트여야 합니다. 사내망 주소로는 보낼 수 없습니다.
- 서버 주소(`PL_BASE_URL`)가 설정되어 있어야 웹훅 본문의 `api_url` 이 만들어집니다.
- 연동의 모든 쓰기·거부·인증 실패는 감사 기록에 남고, 읽기는 토큰·자원·분 단위 요약으로 남습니다. 값은 어느 기록에도 없습니다.
