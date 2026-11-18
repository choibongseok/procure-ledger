# 커넥터 예시

JSON 매핑 파일 하나로 CSV 와 ProcureLedger 연동 API 사이에서 거래처·품목·전표를 옮기는 예시입니다. 특정 회사나 제품을 모릅니다. 열 이름은 모두 꾸며 낸 이름입니다.

| 파일 | 방향 | 하는 일 |
|---|---|---|
| `mapping-vendors-in.json` + `in/vendors.csv` | 들이기(in) | 거래처를 가져오기 미리보기 → 반영으로 들인다 |
| `mapping-journals-in.json` + `in/journals.csv` | 들이기(in) | 전표 초안을 제출한다(같은 `DocNo` 의 줄이 한 전표) |
| `mapping-vendors-out.json` | 내보내기(out) | 거래처를 CSV 로 내보낸다 |
| `mapping-journals-out-rest.json` | 내보내기(out) | 전표를 외부 REST 로 보낸다(주소는 예시) |

## 5분 시험

서버를 띄우지 않고 시험용으로 조립한 서버 위에서 전체 흐름을 돌립니다.

```
make connector-demo
```

`go test ./cmd/server -run TestConnectorDemo` 와 같습니다. 거래처 2건과 전표 초안 2건을 들이고, 같은 파일을 다시 실행해도 결과가 같은지(거래처는 "동일" 행만, 전표는 `external_ref` 멱등), 거래처를 CSV 로 내보낸 결과에 원문(사업자번호·연락처)이 없는지 확인합니다.

## 내 서버에서 실행

1. 관리자 화면(연동 관리)에서 API 를 켜고, 필요한 범위(`vendors:write`·`journals:draft`·`vendors:read` 등)로 토큰을 발급합니다. 토큰 원문은 발급 때 한 번만 보입니다.
2. 서버 호스트에서 토큰을 환경변수로 넘겨 실행합니다. 토큰은 파일이나 명령 인자에 적지 않습니다.

```
export PL_CONNECTOR_TOKEN=<발급받은 토큰>
erp-admin connector run --mapping examples/connector/mapping-vendors-in.json --direction in --dry-run --base-url http://localhost:8080
erp-admin connector run --mapping examples/connector/mapping-vendors-in.json --direction in --base-url http://localhost:8080
```

`--dry-run` 은 미리보기까지만 하고 아무것도 반영하지 않습니다. 오류 행이 하나라도 있으면 반영하지 않고 행 번호와 사유(값 없음)를 보여 줍니다. 서버 주소는 `--base-url` 또는 `PL_BASE_URL` 입니다.

## 매핑 파일

```json
{
  "version": 1,
  "name": "sample-vendors-in",
  "direction": "in",
  "target": "vendors",
  "source": { "kind": "csv", "path": "in/vendors.csv" },
  "fields": [
    { "to": "name", "from": "VendorName", "required": true, "trim": true },
    { "to": "business_no", "from": "BizNo", "transform": "digits_to_dashed_biz" }
  ],
  "on_error": "stop"
}
```

- `target`: 거래처(`vendors`)·품목(`items`)·전표(`journals`). `to` 는 가져오기 양식의 열 식별자입니다(전표는 `external_ref`·`posting_date`·`trade_date`·`memo`·`account_code`·`side`·`amount`·`vendor_id`·`line_memo`).
- 변환은 닫힌 목록입니다: `trim`·`upper`·`int`·`digits_to_dashed_biz`·`date_ymd`(`format` 으로 입력 형식 지정)·`map`(값 사전)·`const`. 임의 코드나 표현식은 쓸 수 없습니다.
- `on_error`: `stop`(기본, 오류 행이 있으면 아무것도 보내지 않음) 또는 `skip`(오류 행을 빼고 나머지를 보냄).
- 파일 안의 상대 경로는 매핑 파일이 있는 폴더가 기준입니다. 토큰처럼 보이는 값이 파일에 있으면 로드를 거부합니다.
- 내보내기(out)는 읽기 API 를 번호 순서로 읽고, 마지막으로 읽은 번호를 `connector_state.json` 에 둡니다. 새로 생긴 항목만 이어서 내보냅니다(바뀐 항목은 변경 웹훅이나 변경 피드로 따라갑니다). 외부 REST 로 보내는 쪽은 주소가 https 여야 하고, 인증 값은 환경변수 이름(`auth_env`)으로만 참조합니다. 내부 주소(사설·루프백)는 `--allow-local` 없이는 거부됩니다.

이 명령은 연동 API 만 쓰며 DB 를 열지 않습니다(서버와 다른 호스트에서도 돌릴 수 있습니다). 실행 결과는 표준 출력의 건수·결과 코드로 나오고, 서버 쪽 기록은 API 가 남깁니다(가져오기 반영·전표 초안 감사, 변경 피드). 행의 값은 어디에도 남지 않습니다.
