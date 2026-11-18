package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// AC-51: 퇴사한 사람의 과거 감사 기록은 그대로 보이고 행위자 이름에 "(퇴사)" 가 붙는다. 재직자에게는 붙지 않는다.
func TestAuditLogMarksLeftActor(t *testing.T) {
	e := newOrgEnv(t)
	ctx := context.Background()
	staff2 := e.userID("staff2@pl.test")
	var name string
	_ = e.d.DB.QueryRow(`SELECT name FROM users WHERE id = ?`, staff2).Scan(&name)
	p := contract.Principal{UserID: staff2, Name: name, Role: contract.RoleStaff, Channel: contract.ChannelMCP}
	record := func(who contract.Principal, target string) {
		if err := e.d.Txs.WithTx(ctx, func(tx contract.Tx) error {
			return e.d.Audit.Record(ctx, tx, audit.Entry{Actor: who, Action: "mcp.search_policy", TargetType: audit.TargetPolicy, TargetID: target, Result: audit.ResultSuccess})
		}); err != nil {
			t.Fatal(err)
		}
	}
	record(p, "before-leave")
	day := time.Now().In(time.FixedZone("KST", 9*3600)).AddDate(0, 0, -1).Format("2006-01-02")
	e.must("admin", "POST", "/api/org/events", fmt.Sprintf(`{"kind":"leave","userId":%d,"lastDay":"%s","leaveKind":"자진","reason":"퇴사 처리","confirm":true}`, staff2, day), 201)

	code, body := e.do("auditor", "GET", "/api/audit-logs?action=mcp.search_policy", "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var out struct {
		Records []struct {
			ActorID   int64  `json:"actorId"`
			ActorName string `json:"actorName"`
			TargetID  string `json:"targetId"`
		} `json:"records"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Records) != 1 {
		t.Fatalf("%v %s", err, body)
	}
	if out.Records[0].ActorName != name+" (퇴사)" {
		t.Fatalf("퇴사자 행위자 이름 %q, 기대 %q", out.Records[0].ActorName, name+" (퇴사)")
	}
	// 재직자의 기록에는 표시가 붙지 않는다.
	staff := e.userID("staff@pl.test")
	var sname string
	_ = e.d.DB.QueryRow(`SELECT name FROM users WHERE id = ?`, staff).Scan(&sname)
	record(contract.Principal{UserID: staff, Name: sname, Role: contract.RoleStaff, Channel: contract.ChannelMCP}, "active")
	_, body = e.do("auditor", "GET", "/api/audit-logs?action=mcp.search_policy", "")
	if !strings.Contains(body, `"actorName":"`+sname+`"`) || strings.Contains(body, sname+" (퇴사)") {
		t.Fatalf("재직자 이름에 표시가 붙음: %s", body)
	}
}
