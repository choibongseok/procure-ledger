# syntax=docker/dockerfile:1
# ProcureLedger 배포 이미지.
# 프론트 빌드 -> Go 빌드(프론트 임베드, cgo 없음) -> 최소 런타임 이미지의 3단계다.
# 로컬 검증(make verify)은 Docker 없이 돈다. 이 파일은 배포 산출물 정의에만 쓴다.

FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.26.11-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...
# 비루트 사용자가 쓸 데이터 디렉터리(SQLite 파일 위치)를 미리 만들어 둔다.
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /app/
COPY --from=build --chown=nonroot:nonroot /data /data
ENV PL_ADDR=:8080 \
    PL_DB_PATH=/data/erp.db
# 운영 기본값: dev IdP(PL_DEV_IDP)는 설정하지 않는다 = 꺼짐. 외부 IdP 는 PL_OIDC_* 와 PL_BASE_URL 로 연결한다.
VOLUME ["/data"]
EXPOSE 8080
USER nonroot
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s CMD ["/app/server", "healthcheck"]
ENTRYPOINT ["/app/server"]
