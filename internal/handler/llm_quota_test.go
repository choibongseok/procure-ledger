package handler_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func putQuota(e *aiEnv, body string) (int, string) {
	w := e.do("PUT", "/api/llm/quotas", "admin", []byte(body), "application/json")
	return w.Code, w.Body.String()
}

func quotaRows(t *testing.T, e *aiEnv) int {
	t.Helper()
	var n int
	if err := e.d.DB.QueryRow(`SELECT COUNT(*) FROM llm_daily_quotas`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 잘못된 한도·범위 값은 400 한국어 문구로 거부하고 저장도 감사 기록도 하지 않는다. 성공 응답에는 실제 id 가 있다.
func TestQuotaInputValidation(t *testing.T) {
	e := newAIEnv(t)
	before := e.rec.Count()
	for name, c := range map[string]struct{ body, want string }{
		"한도 null":     {`{"scopeType":"role","scopeValue":"staff","dailyTokenLimit":null}`, "하루 한도"},
		"한도 누락":       {`{"scopeType":"role","scopeValue":"staff"}`, "하루 한도"},
		"한도 문자열":      {`{"scopeType":"role","scopeValue":"staff","dailyTokenLimit":"1,000"}`, "하루 한도"},
		"한도 음수":       {`{"scopeType":"role","scopeValue":"staff","dailyTokenLimit":-5}`, "0 이상"},
		"없는 역할 이름":    {`{"scopeType":"role","scopeValue":"lead","dailyTokenLimit":100}`, "알 수 없는 역할"},
		"한글 역할 이름":    {`{"scopeType":"role","scopeValue":"직원","dailyTokenLimit":100}`, "알 수 없는 역할"},
		"없는 부서 번호":    {`{"scopeType":"department","scopeValue":"99999","dailyTokenLimit":100}`, "없는 부서"},
		"없는 사용자 번호":   {`{"scopeType":"user","scopeValue":"99999","dailyTokenLimit":100}`, "없는 사용자"},
		"번호가 아닌 부서 값": {`{"scopeType":"department","scopeValue":"영업","dailyTokenLimit":100}`, "번호"},
	} {
		code, body := putQuota(e, c.body)
		if code != http.StatusBadRequest || !strings.Contains(body, c.want) {
			t.Fatalf("%s: 400 + %q 이어야 함: %d %s", name, c.want, code, body)
		}
	}
	if n := quotaRows(t, e); n != 0 {
		t.Fatalf("거부된 입력이 저장됨: %d행", n)
	}
	if got := e.rec.Count() - before; got != 0 {
		t.Fatalf("거부된 입력이 감사에 남음: %d건", got)
	}
	code, body := putQuota(e, `{"scopeType":"role","scopeValue":"staff","dailyTokenLimit":0}`)
	if code != http.StatusOK {
		t.Fatalf("0 은 유효한 값(AI 호출 차단): %d %s", code, body)
	}
	var got struct{ ID int64 }
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.ID == 0 {
		t.Fatalf("응답 id 가 0: %s", body)
	}
}
