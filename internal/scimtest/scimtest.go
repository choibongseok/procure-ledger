// Package scimtest 는 SCIM 서버 시험용 모의 클라이언트다(B40, 명세 15절). 시험 전용 패키지이며 서버 바이너리에 들어가지 않는다.
// 실제 IdP 가 보내는 요청 모양(Entra 의 조회 후 생성·문자열 불리언, Okta 의 userName 조회·경로 없는 replace)을 흉내 낸다.
package scimtest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const (
	EnterpriseSchema = "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"
	UserSchema       = "urn:ietf:params:scim:schemas:core:2.0:User"
	PatchSchema      = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
)

// Client 는 한 SCIM 토큰으로 서버를 부르는 모의 IdP 다.
type Client struct {
	T     testing.TB
	Base  string // 서버 주소(예: http://127.0.0.1:1234). /scim/v2 는 붙이지 않는다.
	Token string // pls_… 원문. 비면 Authorization 헤더를 보내지 않는다.
	HTTP  *http.Client
}

// Resp 는 응답이다.
type Resp struct {
	Status int
	Header http.Header
	Raw    string
	Body   map[string]any
}

// Do 는 요청 하나를 보낸다. body 는 nil, string(그대로), 그 밖(JSON 으로 직렬화)이다.
func (c *Client) Do(method, path string, body any) Resp {
	c.T.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		j, err := json.Marshal(b)
		if err != nil {
			c.T.Fatal(err)
		}
		rd = bytes.NewReader(j)
	}
	req, err := http.NewRequest(method, c.Base+"/scim/v2"+path, rd)
	if err != nil {
		c.T.Fatal(err)
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/scim+json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	h := c.HTTP
	if h == nil {
		h = &http.Client{}
	}
	res, err := h.Do(req)
	if err != nil {
		c.T.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := Resp{Status: res.StatusCode, Header: res.Header, Raw: string(raw)}
	_ = json.Unmarshal(raw, &out.Body)
	return out
}

func (c *Client) Get(path string) Resp             { c.T.Helper(); return c.Do("GET", path, nil) }
func (c *Client) Post(path string, body any) Resp  { c.T.Helper(); return c.Do("POST", path, body) }
func (c *Client) Put(path string, body any) Resp   { c.T.Helper(); return c.Do("PUT", path, body) }
func (c *Client) Patch(path string, body any) Resp { c.T.Helper(); return c.Do("PATCH", path, body) }
func (c *Client) Delete(path string) Resp          { c.T.Helper(); return c.Do("DELETE", path, nil) }
func Filter(expr string) string                    { return "?filter=" + url.QueryEscape(expr) }

// ID 는 응답 본문의 리소스 id 다.
func (r Resp) ID() string {
	s, _ := r.Body["id"].(string)
	return s
}

// ScimType 은 오류 응답의 scimType 이다.
func (r Resp) ScimType() string {
	s, _ := r.Body["scimType"].(string)
	return s
}

// Total 은 목록 응답의 totalResults 다.
func (r Resp) Total() int {
	f, _ := r.Body["totalResults"].(float64)
	return int(f)
}

// Resources 는 목록 응답의 리소스들이다.
func (r Resp) Resources() []map[string]any {
	raw, _ := r.Body["Resources"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, x := range raw {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// Active 는 사용자 리소스의 active 다.
func (r Resp) Active() bool {
	b, _ := r.Body["active"].(bool)
	return b
}

// User 는 사용자 생성 본문이다. 부서·직위가 비어 있으면 보내지 않는다(서버 기본값 시험).
func User(userName, externalID, displayName, department, title string) map[string]any {
	u := map[string]any{"schemas": []string{UserSchema, EnterpriseSchema}, "userName": userName, "displayName": displayName, "active": true}
	if externalID != "" {
		u["externalId"] = externalID
	}
	if title != "" {
		u["title"] = title
	}
	if department != "" {
		u[EnterpriseSchema] = map[string]any{"department": department}
	}
	return u
}

// Op 는 PATCH 연산 하나다.
func Op(op, path string, value any) map[string]any {
	m := map[string]any{"op": op, "value": value}
	if path != "" {
		m["path"] = path
	}
	return m
}

// Patch 는 PatchOp 본문이다.
func Patch(ops ...map[string]any) map[string]any {
	return map[string]any{"schemas": []string{PatchSchema}, "Operations": ops}
}

// EntraSync 는 Entra 의 프로비저닝 한 주기를 흉내 낸다: externalId 로 조회 → 없으면 POST, 있으면 PATCH(active 는 문자열 "True"/"False").
func (c *Client) EntraSync(externalID, userName, displayName, department, title string, active bool) Resp {
	c.T.Helper()
	found := c.Get("/Users" + Filter(`externalId eq "`+externalID+`"`))
	if found.Total() == 0 {
		u := User(userName, externalID, displayName, department, title)
		u["active"] = active
		return c.Post("/Users", u)
	}
	val := "False"
	if active {
		val = "True"
	}
	return c.Patch("/Users/"+found.Resources()[0]["id"].(string), Patch(Op("Replace", "active", val)))
}

// OktaSync 는 Okta 의 한 주기를 흉내 낸다: userName 한 건 조회 → 없으면 POST, 있으면 PUT(전체 교체, externalId 는 보내지 않음).
func (c *Client) OktaSync(userName, displayName, department, title string, active bool) Resp {
	c.T.Helper()
	found := c.Get("/Users" + Filter(`userName eq "`+userName+`"`) + "&startIndex=1&count=1")
	u := User(userName, "", displayName, department, title)
	u["active"] = active
	if found.Total() == 0 {
		return c.Post("/Users", u)
	}
	return c.Put("/Users/"+found.Resources()[0]["id"].(string), u)
}
