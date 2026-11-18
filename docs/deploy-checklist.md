# 배포 체크리스트

 이 문서는 "배포 경로 정의"까지다. 클라우드 배포 실행은 범위 밖이다(spec 6절).

## 산출물

| 산출물 | 위치 | 설명 |
|---|---|---|
| 단일 바이너리 | `make build` → `bin/server` | 프론트(`web/dist`)를 임베드한다. cgo 없음(SQLite 는 modernc). |
| 컨테이너 이미지 | `Dockerfile` | 프론트 빌드 → Go 빌드 → distroless(nonroot). 데이터는 `/data`. |
| CI | `.github/workflows/ci.yml` | PR·main 푸시마다 `make verify`, 이미지 빌드 확인(푸시 안 함). |
| 릴리스 | `.github/workflows/release.yml` | `v*` 태그 → `make verify` → GHCR 이미지 푸시. |

## 컨테이너 검증 (선택, `make verify` 에는 들어가지 않음)

```
make verify-docker      # scripts/verify-docker.sh
```

Docker 데몬이 있는 머신에서 다음을 차례로 확인하고, 어떤 경로로 끝나도 컨테이너를 지운다(이미지 `procure-ledger:verify` 는 캐시로 남는다).

1. `docker build` 성공
2. 이미지 기본 설정(환경변수 없이)으로 컨테이너 기동, 호스트 포트는 OS 가 고른 빈 포트(127.0.0.1 한정)
3. 이미지의 `HEALTHCHECK` 가 `healthy` 가 되고 `GET /healthz` 가 `{"status":"ok"}`
4. 기본 설정에서 dev IdP 가 꺼져 있다: `/api/auth/config` 가 `devIdp:false`, `/dev-idp/authorize` 가 IdP 가 아니라 SPA html, 로그에 dev IdP 경고 없음, 미인증 `/api/me` 는 401

Docker 데몬이 없으면 바로 실패한다(`make verify` 에는 영향 없음). 실패하면 단계와 컨테이너 로그 끝 30줄을 출력한다.

## 릴리스 절차

1. main 에서 `make verify` 가 녹색인지 확인한다(출력 마지막 줄이 `결과: 전체 통과`).
2. `git tag vX.Y.Z && git push origin vX.Y.Z`.
3. Actions 의 release 워크플로가 끝나면 `ghcr.io/<소유자>/<저장소>:X.Y.Z` 이미지가 생긴다.

## 기동 전 점검 (운영)

설정은 환경변수로만 한다. 비밀값은 이미지·저장소에 넣지 않는다.

| 변수 | 운영 값 | 비고 |
|---|---|---|
| `PL_DEV_IDP` | **설정하지 않음(꺼짐)** | `1` 이면 비밀번호 없는 dev IdP 와 시드 계정이 켜진다. 운영에서 켜지 마세요. `PL_BASE_URL` 이 로컬 주소(localhost·127.0.0.1·::1)가 아니면 기동이 실패하고, 외부 IdP 와 같이 켜도 실패한다. |
| `PL_BASE_URL` | 사용자가 접속하는 주소(`https://erp.example.com`) | OIDC 리다이렉트 기준. `https` 이면 세션 쿠키에 Secure 가 자동으로 붙는다. |
| `PL_OIDC_ISSUER` | 외부 IdP 발급자 URL | 설정하면 표준 OIDC(Authorization Code + PKCE) 로 로그인한다. |
| `PL_OIDC_CLIENT_ID` / `PL_OIDC_CLIENT_SECRET` | IdP 에서 발급 | 시크릿은 비밀 저장소에서 주입. |
| `PL_OIDC_REDIRECT_URL` | 비우면 `PL_BASE_URL/api/auth/callback` | IdP 에 같은 주소를 등록한다. |
| `PL_DB_PATH` | `/data/erp.db` | 볼륨을 마운트한다. 백업은 이 파일(`-wal`·`-shm` 포함)을 정지 후 복사하거나 SQLite 백업 API 로 한다. |
| `PL_DB_URL` | (비움) | PostgreSQL 로 운영할 때만 `postgres://…?sslmode=verify-full`. 지정하면 `PL_DB_PATH` 는 무시된다. 구성·이관·백업은 `docs/postgresql.md`. |
| `PL_LLM_PROVIDER` / `PL_LLM_MODEL` / `PL_LLM_API_KEY_ENV` | 운영 공급자·모델 | 모델 ID 는 코드에 없다. 키는 환경변수 이름만 설정에 두고 값은 비밀 저장소에서 주입. |

체크 항목

- [ ] `PL_DEV_IDP` 가 비어 있다. 기동 로그에 `dev IdP 가 켜져 있습니다` 경고가 없다.
- [ ] `GET /api/auth/config` 응답이 `{"devIdp": false, ...}` 이고 `GET /dev-idp/authorize` 가 SPA 로 떨어진다(IdP 응답이 아님).
- [ ] 외부 IdP 로 최초 로그인하기 전에 사용자가 등록되어 있다. 등록되지 않은 사용자는 로그인이 거부되고 감사 로그에 `session.login` 거부가 남는다(자동 가입 없음).
- [ ] 첫 관리자는 **설치 마법사**로 만든다(DB 를 직접 고치지 않는다). 빈 DB 로 서버를 처음 띄우면 서버 로그(콘솔)에 `setup: 초기 설정이 필요합니다 ... 설치 코드를 입력하세요: XXXX-...` 가 한 번 출력된다. 브라우저에서 `/setup` 을 열어 그 코드를 넣고 회사 정보·첫 관리자(이메일·비밀번호)·데모 데이터 여부를 정한 뒤 "설치 완료"를 누르고, 로그인 화면에서 방금 정한 이메일·비밀번호로 로그인한다. 로그를 외부로 수집하는 환경이거나 자동 설치면 `PL_SETUP_CODE`(16자 이상, 비밀)로 코드를 직접 정해 별도 채널로 전달한다(이때 값은 로그에 찍히지 않는다). 코드는 서버 전체 기준 연속 5회 틀리면 15분 동안 입력이 잠기고(올바른 코드도 거부), 24시간 뒤 만료된다(재기동하면 새 코드). 설치가 끝나면 마법사 경로는 영구히 404 다. 이미 사용자가 있는 DB(기존 설치·업그레이드)는 마법사가 열리지 않는다. 이후 역할·부서는 사용자 관리 화면에서 바꾼다.
- [ ] 서버 호스트 운영 명령은 `erp-admin`(`access-recompute`, `auth-unlock <이메일>`)이다. 기존 `server access-recompute`·`server auth-unlock` 은 같은 구현의 별칭으로 그대로 동작한다.
- [ ] HTTPS 종단(리버스 프록시)이 있고 `PL_BASE_URL` 이 `https://` 이다. 프록시가 `Origin`·`Host` 를 바꾸지 않는다(CSRF 의 Origin 검사가 `Host` 와 비교한다).
- [ ] `/data` 볼륨이 영속이고 백업이 잡혀 있다.
- [ ] 컨테이너 헬스체크가 통과한다(`/app/server healthcheck`, 내부적으로 `/healthz`).

## 배포 후 스모크

```
curl -fsS $BASE/healthz                      # {"status":"ok"}
curl -fsS $BASE/api/auth/config              # devIdp:false
curl -s -o /dev/null -w '%{http_code}' $BASE/api/me   # 401 (미인증)
```

로그인 후(외부 IdP) `auditor` 계정으로 감사 로그 화면에서 "변조 여부 검증" 을 눌러 `변조가 없습니다` 가 나오는지 확인한다.
체인만으로는 마지막 N건 삭제와 체인 전체 재계산을 구분하지 못한다. 외부 앵커를 켜면 검증 결과의 "외부 앵커" 줄이 이를 가려낸다.

### 감사 체인 외부 앵커(`PL_AUDIT_ANCHOR_KEY`, `PL_AUDIT_ANCHOR_PATH`, `PL_AUDIT_ANCHOR_INTERVAL`)

- [ ] `PL_AUDIT_ANCHOR_KEY` 에 16바이트 이상의 무작위 키를 비밀 저장소로 주입한다. 코드·DB·이미지·`.env` 파일에 두지 않는다. 키가 없으면 앵커가 꺼지고 검증 결과·화면에 "외부 대조를 하지 않았습니다" 가 표시된다(통과가 아니다).
- [ ] `PL_AUDIT_ANCHOR_PATH` 를 **DB 와 다른 볼륨·권한**의 파일로 지정한다. 앱 계정에는 추가(append)만 허용하고, DB 를 쓸 수 있는 계정이 이 파일을 지우거나 고칠 수 없게 한다. 키가 있는데 파일을 쓸 수 없으면 서버가 기동하지 않는다.
- [ ] 앵커가 꺼진 채 기동하면 서버 로그에 "경고: … 외부 앵커가 꺼져 있습니다" 한 줄이 남는다(키 없이 경로만 준 경우 포함). 검증 API 최상위의 `externallyVerified`·`level`·`summary` 로 완전 통과(`verified`)와 체인만 확인(`chain_only`)을 구분한다. `ok` 만 보지 않는다.
- [ ] 앵커 파일이 삭제되거나 끝이 잘리면 검증이 `anchor_lost` 로 실패하고, 수동 앵커는 409 로 거부되며 `audit_log.anchor_lost` 가 감사 로그에 남는다. 원인을 확인한 뒤 감사자가 `POST /api/audit-logs/anchor/restart`(화면의 "앵커 다시 시작")로 명시적으로 다시 시작한다. 이후에도 "앵커 유실 이력 있음"이 검증 결과에 계속 표시된다. 근거는 DB 의 `audit_anchor_stamps` 다. 파일이 아니라 DB 까지 함께 바꾸는 공격자는 못 잡는다.
- [ ] 기록은 `PL_AUDIT_ANCHOR_INTERVAL`(기본 5m)마다, 그리고 종료 시 한 번 한다. 새 감사 기록이 없으면 줄을 늘리지 않는다. 앵커 파일은 정기적으로 백업·원격 보관한다.
- [ ] 앵커 파일 한 줄은 시각·마지막 번호·머리 해시·서명이며 각 줄이 앞 줄의 해시를 포함한다. 검증은 서명·연결과 DB 해시를 대조해 "꼬리 삭제 / 내용 재계산 / 앵커 파일 손상" 을 구분해 알려 준다.
- 수동으로 지금 기록하려면 감사 담당(auditor)이 `POST /api/audit-logs/anchor` 를 호출한다.

한계(알고 운영한다)
- 키와 앵커 파일을 둘 다 가진 공격자는 앵커를 새로 만들어 쓸 수 있다. 키와 파일의 접근 주체를 분리한다.
- 마지막 앵커 이후 구간(기록 간격 안)은 외부 대조가 없다. 이 구간만의 꼬리 삭제는 잡히지 않는다.
- 앵커 파일 끝의 줄을 통째로 잘라 내면 파일은 정상으로 보이고 대조 범위만 줄어든다. 파일을 전부 지우면 빈 앵커로 보이며 화면은 경고를 낸다. 백업과 비교해 확인한다.
- 키를 교체하면 기존 줄이 위조로 판정된다. 교체할 때는 기존 파일을 보관하고 새 경로로 옮긴다.

## 롤백

- 이미지는 이전 태그로 되돌린다. 마이그레이션은 앞으로만 적용(`NNNN_*.sql`, 파일당 한 트랜잭션)이므로, 스키마가 바뀐 릴리스를 되돌릴 때는 배포 전 DB 백업으로 복구한다.

## 로컬 검증용 dev IdP (참고)

`PL_DEV_IDP=1 PL_ADDR=127.0.0.1:8080 ./bin/server` 로 켜면 시드 계정으로 로그인 화면에서 바로 로그인한다.
계정: `admin@pl.test`, `finance@pl.test`, `lead@pl.test`(구매팀 팀장), `lead2@pl.test`(구매팀 팀장 2), `head@pl.test`(구매팀 부서장),
`staff@pl.test`(구매팀), `staff2@pl.test`(영업팀), `auditor@pl.test`. 구매팀 구매요청의 결재선은 lead → head → finance 다.
