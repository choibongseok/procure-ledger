package scim

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/access"
	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/org"
)

func logf(format string, a ...any) { log.Printf(format, a...) }

const (
	schemaError   = "urn:ietf:params:scim:api:messages:2.0:Error"
	schemaList    = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	schemaPatch   = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	schemaUser    = "urn:ietf:params:scim:schemas:core:2.0:User"
	schemaGroup   = "urn:ietf:params:scim:schemas:core:2.0:Group"
	schemaEnt     = "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"
	contentSCIM   = "application/scim+json"
	schemaSPC     = "urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"
	schemaRT      = "urn:ietf:params:scim:schemas:core:2.0:ResourceType"
	schemaSchema  = "urn:ietf:params:scim:schemas:core:2.0:Schema"
	reasonNone    = ""
	reasonProt    = "protected"
	reasonRisk    = "risk_gain"
	reasonMut     = "mutability"
	reasonUniq    = "uniqueness"
	reasonLast    = "last_admin"
	reasonAdopt   = "adopt_denied"
	reasonRate    = "rate"
	reasonSameDay = "same_day"
)

// apiError 는 SCIM 오류 응답으로 옮길 실패다(7.6 표). Reason 은 동기화 기록·감사의 짧은 코드다.
type apiError struct {
	Status int
	Type   string
	Detail string
	Reason string
}

func (e *apiError) Error() string { return e.Detail }

func errInvalidValue(detail string) *apiError {
	return &apiError{Status: 400, Type: "invalidValue", Detail: detail}
}
func errMutability(detail, reason string) *apiError {
	return &apiError{Status: 400, Type: "mutability", Detail: detail, Reason: reason}
}
func errUniqueness(detail, reason string) *apiError {
	return &apiError{Status: 409, Type: "uniqueness", Detail: detail, Reason: reason}
}
func errConflict(detail, reason string) *apiError {
	return &apiError{Status: 409, Detail: detail, Reason: reason}
}
func errNotFound(detail string) *apiError { return &apiError{Status: 404, Detail: detail} }

func writeSCIM(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentSCIM)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, e *apiError) {
	body := map[string]any{"schemas": []string{schemaError}, "status": strconv.Itoa(e.Status), "detail": e.Detail}
	if e.Type != "" {
		body["scimType"] = e.Type
	}
	if e.Status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="scim"`)
	}
	writeSCIM(w, e.Status, body)
}

// toAPIError 는 발령·서비스가 돌려준 오류를 SCIM 오류로 옮긴다. 알 수 없는 오류는 상세를 숨기고 500 이다.
func toAPIError(err error) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	var rg *org.RiskGainError
	if errors.As(err, &rg) {
		return errMutability("위험 권한이 새로 생기는 발령은 SCIM 으로 할 수 없습니다.", reasonRisk)
	}
	var rr *access.RiskyRoleError
	if errors.As(err, &rr) {
		return errInvalidValue("위험 권한이 든 역할은 SCIM 으로 줄 수 없습니다. 기본 역할 설정을 확인하세요.")
	}
	var ce *org.ConfirmError
	if errors.As(err, &ce) {
		return errConflict(ce.Error(), "")
	}
	if errors.Is(err, contract.ErrValidation) {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "이미 사용 중인 이메일"), strings.Contains(msg, "이미 사용 중인 사번"):
			return errUniqueness(msg, reasonUniq)
		case strings.Contains(msg, "관리자 권한을 가진 사람이 없어지게"):
			return errConflict(msg, reasonLast)
		}
		return errInvalidValue(strings.TrimPrefix(msg, contract.ErrValidation.Error()+": "))
	}
	if errors.Is(err, contract.ErrNotFound) {
		return errNotFound("대상을 찾을 수 없습니다.")
	}
	if errors.Is(err, contract.ErrInvalidState) {
		return errConflict(err.Error(), "")
	}
	logf("scim: %v", err)
	return &apiError{Status: 500, Detail: "서버 오류가 발생했습니다.", Reason: "internal"}
}

func resultOf(status int) string {
	switch {
	case status < 400:
		return "success"
	case status == http.StatusConflict:
		return "conflict"
	case status >= 500:
		return "error"
	}
	return "denied"
}

func maskEmail(e string) string {
	at := strings.LastIndex(e, "@")
	if at <= 0 {
		return "***"
	}
	return e[:1] + "***" + e[at:]
}

func tail4(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[len(s)-4:]
}
