package scim

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
)

type patchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

type patchBody struct {
	Schemas    []string  `json:"schemas"`
	Operations []patchOp `json:"Operations"`
}

var simplePaths = map[string]string{
	"active": "active", "username": "userName", "displayname": "displayName", "title": "title", "externalid": "externalId",
}

// fragment 는 PATCH 연산 하나를 userIn 에 합칠 JSON 조각으로 바꾼다. 알 수 없는 경로는 invalidPath 다(7.5).
func fragment(op patchOp) (frag []byte, ignored string, e *apiError) {
	p := strings.TrimSpace(op.Path)
	low := strings.ToLower(p)
	val := op.Value
	if len(val) == 0 {
		return nil, "", &apiError{Status: 400, Type: "invalidSyntax", Detail: "연산에 value 가 필요합니다."}
	}
	wrap := func(k string, v []byte) []byte {
		b, _ := json.Marshal(map[string]json.RawMessage{k: json.RawMessage(v)})
		return b
	}
	ent := strings.ToLower(schemaEnt)
	switch {
	case p == "":
		return val, "", nil // 경로 없는 add·replace: 값이 속성 객체다(Okta)
	case simplePaths[low] != "":
		return wrap(simplePaths[low], val), "", nil
	case low == "name.givenname" || low == "name.familyname" || low == "name.formatted":
		sub := strings.TrimPrefix(low, "name.")
		canon := map[string]string{"givenname": "givenName", "familyname": "familyName", "formatted": "formatted"}[sub]
		return wrap("name", wrap(canon, val)), "", nil
	case strings.HasPrefix(low, "phonenumbers"):
		if strings.Contains(low, `"work"`) || low == "phonenumbers" {
			item, _ := json.Marshal(map[string]any{"type": "work", "value": json.RawMessage(val)})
			return wrap("phoneNumbers", []byte("["+string(item)+"]")), "", nil
		}
		return nil, "phoneNumbers", nil
	case low == ent+":department":
		return wrap(schemaEnt, wrap("department", val)), "", nil
	case low == ent+":employeenumber":
		return wrap(schemaEnt, wrap("employeeNumber", val)), "", nil
	case strings.HasPrefix(low, "emails") || low == "name" || strings.HasPrefix(low, ent+":"):
		return nil, p, nil // 이메일·이름 묶음·그 밖의 확장 속성은 받되 적용하지 않는다
	}
	return nil, "", &apiError{Status: 400, Type: "invalidPath", Detail: "지원하지 않는 경로입니다: " + p}
}

// applyPatch 는 연산들을 하나의 userIn 으로 모은다. 같은 속성을 두 번 바꾸면 뒤 연산이 이긴다(규격 순서).
func applyPatch(r io.Reader) (*userIn, []string, *apiError) {
	b, err := io.ReadAll(r)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, nil, &apiError{Status: 413, Type: "tooMany", Detail: "요청 본문이 너무 큽니다(1MiB 이하)."}
		}
		return nil, nil, &apiError{Status: 400, Type: "invalidSyntax", Detail: "요청 본문을 읽을 수 없습니다."}
	}
	var body patchBody
	if err := json.Unmarshal(b, &body); err != nil {
		return nil, nil, &apiError{Status: 400, Type: "invalidSyntax", Detail: "JSON 형식이 올바르지 않습니다."}
	}
	if len(body.Operations) == 0 {
		return nil, nil, &apiError{Status: 400, Type: "invalidSyntax", Detail: "Operations 가 비어 있습니다."}
	}
	in := &userIn{}
	var warns []string
	for _, op := range body.Operations {
		switch strings.ToLower(strings.TrimSpace(op.Op)) {
		case "add", "replace":
			frag, ign, e := fragment(op)
			if e != nil {
				return nil, nil, e
			}
			if ign != "" {
				warns = append(warns, "속성 "+ign+" 은(는) 이 버전에서 적용하지 않아 무시했습니다")
				continue
			}
			if err := json.Unmarshal(frag, in); err != nil {
				return nil, nil, errInvalidValue("연산의 값 형식이 올바르지 않습니다: " + op.Path)
			}
		case "remove":
			p := strings.ToLower(strings.TrimSpace(op.Path))
			switch {
			case p == "":
				return nil, nil, &apiError{Status: 400, Type: "noTarget", Detail: "remove 에는 path 가 필요합니다."}
			case strings.HasPrefix(p, "phonenumbers"):
				warns = append(warns, "연락처 제거는 이 버전에서 적용하지 않아 무시했습니다")
			case simplePaths[p] != "" || strings.HasPrefix(p, "name.") || strings.HasPrefix(p, strings.ToLower(schemaEnt)+":"):
				return nil, nil, errMutability("이 속성은 제거할 수 없습니다: "+op.Path, reasonMut)
			default:
				return nil, nil, &apiError{Status: 400, Type: "noTarget", Detail: "제거할 대상이 없습니다: " + op.Path}
			}
		default:
			return nil, nil, &apiError{Status: 400, Type: "invalidSyntax", Detail: "지원하지 않는 연산입니다: " + op.Op}
		}
	}
	return in, warns, nil
}

func (s *Service) patchUser(w http.ResponseWriter, r *http.Request) {
	in, warns, ae := applyPatch(r.Body)
	if ae != nil {
		writeAPIError(w, ae)
		return
	}
	s.runWrite(w, r, http.MethodPatch, "update", func(ctx context.Context, tx contract.Tx, rc *reqCtx) (writeResult, error) {
		uid, err := s.target(ctx, tx, rc, r.PathValue("id"), in, true)
		if err != nil {
			return writeResult{}, err
		}
		if err := dbutil.AcquireKeyLock(ctx, tx, "scim:user:"+strconv.FormatInt(uid, 10)); err != nil {
			return writeResult{}, err
		}
		return s.updateExisting(ctx, tx, rc, uid, in, writeResult{Warnings: warns})
	})
}
