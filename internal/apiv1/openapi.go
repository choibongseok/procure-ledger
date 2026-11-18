package apiv1

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 목록 응답의 항목 형을 서비스 반환 형과 이어 주는 별칭이다(listHandler 의 형 추론용).
type (
	itemRow    = domain.APIItem
	vendorRow  = domain.Vendor
	poRow      = domain.PurchaseOrder
	receiptRow = domain.APIReceipt
	journalRow = domain.Journal
	payableRow = domain.APIPayable
	taxInvRow  = domain.APITaxInvoice
)

// ErrorCodes 는 오류 코드 전체 목록이다(문서·코드 일치 시험이 본다).
var ErrorCodes = []string{"unauthenticated", "forbidden_scope", "not_found", "invalid_argument", "rate_limited", "cursor_expired", "conflict", "unavailable", "api_disabled"}

// OpenAPI 는 라우트 선언표(Routes)와 응답 형(Go 구조체)에서 OpenAPI 3.1 문서를 만든다. 같은 입력은 항상 같은 바이트다(키 정렬, 들여쓰기 2칸).
// 손으로 쓴 문서를 정본으로 두지 않는다(D14): 저장소의 docs/openapi/v1.json 은 이 함수의 결과이고 시험이 일치를 확인한다.
func OpenAPI() ([]byte, error) {
	comps := map[string]any{}
	paths := map[string]any{}
	for _, d := range Routes {
		op := map[string]any{
			"summary":     d.Summary,
			"operationId": operationID(d),
			"tags":        []string{tagOf(d)},
		}
		if d.Scope != "" {
			op["security"] = []any{map[string]any{"bearerAuth": []string{}}}
			op["x-required-scope"] = d.Scope
		} else {
			op["security"] = []any{}
		}
		var params []any
		for _, p := range d.Params {
			in := p.In
			if in == "" {
				in = "query"
			}
			params = append(params, map[string]any{"name": p.Name, "in": in, "required": p.Required || in == "path", "description": p.Desc, "schema": map[string]any{"type": p.Type}})
		}
		if d.Idem {
			params = append(params, map[string]any{"name": "Idempotency-Key", "in": "header", "required": false, "description": "1~64자의 영문·숫자·_ . : - . 같은 키·같은 본문의 재요청은 첫 응답을 그대로 돌려준다(Idempotent-Replayed: true 헤더). 같은 키로 다른 본문을 보내면 409(conflict). 24시간 보존", "schema": map[string]any{"type": "string"}})
		}
		if params != nil {
			op["parameters"] = params
		}
		resp := map[string]any{}
		switch d.Kind {
		case "list":
			resp["200"] = jsonResp("목록 한 쪽(번호 오름차순)", map[string]any{
				"type":     "object",
				"required": []string{"data", "next_cursor", "has_more"},
				"properties": map[string]any{
					"data":        map[string]any{"type": "array", "items": schemaRef(d.Item, comps)},
					"next_cursor": map[string]any{"type": []string{"string", "null"}, "description": "다음 쪽을 읽을 커서(더 없으면 null)"},
					"has_more":    map[string]any{"type": "boolean"},
				},
			})
		case "changes":
			resp["200"] = jsonResp("변경 피드(번호 순서 = 커밋 순서)", map[string]any{
				"type":     "object",
				"required": []string{"data", "next_cursor", "has_more"},
				"properties": map[string]any{
					"data":        map[string]any{"type": "array", "items": schemaRef(d.Item, comps)},
					"next_cursor": map[string]any{"type": "string", "description": "다음에 보낼 커서"},
					"has_more":    map[string]any{"type": "boolean"},
				},
			})
			resp["410"] = errResp("커서가 만료되었습니다(cursor_expired). 전체를 다시 읽는다")
		case "head":
			resp["200"] = jsonResp("현재 피드 위치", map[string]any{"type": "object", "required": []string{"data"}, "properties": map[string]any{"data": schemaRef(d.Item, comps)}})
		case "write":
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaRef(d.Req, comps)}}}
			resp[strconv.Itoa(d.Status)] = jsonResp("결과(data)", map[string]any{"type": "object", "required": []string{"data"}, "properties": map[string]any{"data": schemaRef(d.Item, comps)}})
			resp["409"] = errResp("이미 있는 값과 충돌하거나 현재 상태에서 할 수 없습니다(conflict). 마감된 달의 전표, 같은 external_ref 의 다른 내용, 같은 Idempotency-Key 의 다른 본문 등")
			resp["413"] = errResp("요청 본문이 너무 큽니다(5MB 이하)")
		case "openapi":
			resp["200"] = map[string]any{"description": "OpenAPI 3.1 문서", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object"}}}}
		}
		if d.Scope != "" {
			resp["400"] = errResp("요청 값이 올바르지 않습니다(invalid_argument)")
			resp["401"] = errResp("토큰이 올바르지 않거나 만료되었습니다(unauthenticated)")
			resp["403"] = errResp("토큰의 범위가 맞지 않거나 API 가 꺼져 있습니다(forbidden_scope·api_disabled)")
			resp["429"] = errResp("호출 한도를 넘었습니다(rate_limited). Retry-After 헤더가 다음 시도까지의 초를 알린다")
			resp["500"] = errResp("서버 오류(unavailable)")
		}
		op["responses"] = resp
		item, _ := paths[d.Path].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[d.Path] = item
		}
		item[strings.ToLower(d.Method)] = op
	}
	schemaRef(reflect.TypeOf(ErrorBody{}), comps)
	doc := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "ProcureLedger 연동 API",
			"version":     "1",
			"description": "외부 시스템이 품목·거래처·발주·입고·전표·채무를 읽고 변경 피드로 증분을 따라가며, 거래처·품목을 가져오고 전표 초안을 제출하는 API. 인증은 Authorization: Bearer pli_… 토큰이다. 번호·금액은 정수, 시각은 RFC3339 UTC, 필드 이름은 snake_case 다. 거래처의 사업자번호·계좌·연락처는 항상 가려진 값이고 사람 정보(작성자·승인자·사번)는 어떤 응답에도 없다.",
		},
		"paths": paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{"bearerAuth": map[string]any{"type": "http", "scheme": "bearer", "description": "관리자 화면에서 발급한 pli_ 토큰"}},
			"schemas":         comps,
		},
		"x-error-codes": ErrorCodes,
	}
	return json.MarshalIndent(doc, "", "  ")
}

func openAPIHandler(w http.ResponseWriter, r *http.Request) {
	rid := requestID(w)
	b, err := OpenAPI()
	if err != nil {
		writeError(w, rid, http.StatusInternalServerError, "unavailable", "서버 오류가 발생했습니다.")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(b)
}

func jsonResp(desc string, schema map[string]any) map[string]any {
	return map[string]any{"description": desc, "content": map[string]any{"application/json": map[string]any{"schema": schema}}}
}

func errResp(desc string) map[string]any {
	return map[string]any{"description": desc, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{
		"type": "object", "required": []string{"error"}, "properties": map[string]any{"error": map[string]any{"$ref": "#/components/schemas/ErrorBody"}}}}}}
}

func tagOf(d RouteDef) string {
	switch d.Kind {
	case "changes", "head":
		return "변경 피드"
	case "openapi":
		return "문서"
	case "write":
		return "쓰기"
	}
	return "자원"
}

func operationID(d RouteDef) string {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(d.Path, "/api/v1"), "/"), "/")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(strings.Title(strings.ReplaceAll(p, "-", " ")), " ", "") //nolint:staticcheck
	}
	return strings.ToLower(d.Method) + strings.Join(parts, "")
}

// schemaRef 는 구조체 형을 components 에 등록하고 참조를 돌려준다.
func schemaRef(t reflect.Type, comps map[string]any) map[string]any {
	name := t.Name()
	if _, ok := comps[name]; !ok {
		comps[name] = nil // 재귀 방지 자리
		comps[name] = structSchema(t, comps)
	}
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

func structSchema(t reflect.Type, comps map[string]any) map[string]any {
	props := map[string]any{}
	var required []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		sch := typeSchema(f.Type, comps)
		if doc := f.Tag.Get("doc"); doc != "" {
			sch["description"] = doc
		}
		props[name] = sch
		if !strings.Contains(opts, "omitempty") {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func typeSchema(t reflect.Type, comps map[string]any) map[string]any {
	switch t.Kind() {
	case reflect.Int, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": typeSchema(t.Elem(), comps)}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": typeSchema(t.Elem(), comps)}
	case reflect.Struct:
		return schemaRef(t, comps)
	case reflect.Ptr:
		return typeSchema(t.Elem(), comps)
	}
	panic(fmt.Sprintf("apiv1: OpenAPI 로 옮길 수 없는 형 %s", t))
}
