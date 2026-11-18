package rbac

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// NewGuard 는 RouteTable.Guard 로 쓰는 권한 강제 래퍼다. DefaultGuard 와 같은 규칙(401/403, fail-closed)에
// 더해, 로그인했지만 권한이 없는 요청(403)을 감사 로그에 denied 로 남긴다.
// 기록이 실패해도 요청은 이미 거부된 상태이므로 403 은 그대로다(실패는 서버 로그로 남긴다).
func NewGuard(rec audit.Recorder, txs contract.TxManager) func(perm contract.Permission, next http.Handler) http.Handler {
	return func(perm contract.Permission, next http.Handler) http.Handler {
		if perm == contract.PermPublic {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, err := contract.Authorize(r.Context(), perm)
			switch err {
			case nil:
				next.ServeHTTP(w, r)
			case contract.ErrUnauthenticated:
				http.Error(w, err.Error(), http.StatusUnauthorized)
			case contract.ErrSetupRequired:
				// 제한 세션: 화면이 비밀번호 변경·2단계 등록으로 안내한다. 반복 호출로 감사가 불어나지 않게 기록하지 않는다.
				contract.WriteSetupRequired(w, p.Setup)
			default:
				recordDenied(r, rec, txs, p, "필요 권한 "+string(perm))
				http.Error(w, err.Error(), http.StatusForbidden)
			}
		})
	}
}

func recordDenied(r *http.Request, rec audit.Recorder, txs contract.TxManager, p contract.Principal, detail string) {
	if rec == nil || txs == nil {
		return
	}
	target := r.Pattern
	if target == "" {
		target = r.Method + " " + r.URL.Path
	}
	// 요청 컨텍스트가 취소돼도 거부 기록은 남긴다.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	err := txs.WithTx(ctx, func(tx contract.Tx) error {
		return rec.Record(ctx, tx, audit.Entry{
			Actor: p, Action: "access.denied", TargetType: audit.TargetRoute, TargetID: target,
			Result: audit.ResultDenied, Detail: detail,
		})
	})
	if err != nil {
		log.Printf("rbac: 거부 기록 실패: %v", err)
	}
}

// SessionOnly 는 라우트 등록 시 선언하는 인증 수단 제약이다: 로그인 세션 쿠키로 인증된 요청만 통과하고
// 개인 토큰(Bearer)으로 들어온 요청은 403 과 함께 감사 로그(access.denied)에 남긴다.
// 장기 자격을 만드는 동작(토큰 발급·폐기)에 쓴다. 핸들러 안의 분기가 아니라 등록 지점의 선언이다.
//
//	r.HandleFunc("POST", "/api/tokens", contract.PermTokenManage, rbac.SessionOnly(rec, txs, h))
func SessionOnly(rec audit.Recorder, txs contract.TxManager, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := contract.PrincipalFrom(r.Context())
		if ok && p.Auth == contract.AuthToken {
			recordDenied(r, rec, txs, p, "개인 토큰으로는 접근할 수 없는 기능(로그인 세션 필요)")
			http.Error(w, "개인 토큰으로는 이 기능을 쓸 수 없습니다. 로그인한 화면에서 하세요", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}
