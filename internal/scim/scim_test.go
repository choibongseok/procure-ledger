package scim

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// SCIM 주체는 UserID 가 0 이 아니고(0 이면 org 의 위험 권한 게이트가 꺼진다) role:manage 같은 위험 권한이 없다(AC-4004·4005).
func TestActorIsRestricted(t *testing.T) {
	a := newActor(tokenRow{ID: 3, Name: "엔트라", Source: "entra", CreatedBy: 42})
	if a.UserID != 42 || a.Channel != contract.ChannelSCIM || a.Auth != contract.AuthSCIM || a.TokenID != 3 {
		t.Fatalf("주체: %+v", a)
	}
	if !a.Can(contract.PermOrgManage) || !a.Can(contract.PermSCIMProvision) {
		t.Fatal("발령·SCIM 권한이 없음")
	}
	for _, p := range contract.AllPermissions {
		if p != contract.PermOrgManage && a.Can(p) {
			t.Errorf("SCIM 주체가 %s 를 가짐", p)
		}
	}
	for _, p := range contract.HighRiskPermissions {
		if a.Can(p) {
			t.Errorf("SCIM 주체가 위험 권한 %s 를 가짐", p)
		}
	}
}

// 소스 검사: scim 패키지는 UserID 가 0 인 주체(시스템 발령자)로 발령을 등록하지 않는다. 0 이면 위험 권한 게이트가 꺼진다.
func TestNoSystemActorInSource(t *testing.T) {
	re := regexp.MustCompile(`UserID:\s*0\b`)
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if re.MatchString(line) {
				t.Errorf("%s:%d: UserID 0 주체는 쓸 수 없음(위험 권한 게이트가 꺼진다): %s", e.Name(), i+1, strings.TrimSpace(line))
			}
		}
	}
}

func TestFilterParser(t *testing.T) {
	conds, args, cls, e := parseFilter(`userName eq "A@x.test" and active eq true`)
	if e != nil || len(conds) != 2 || len(cls) != 2 || args[0] != "a@x.test" {
		t.Fatalf("정상: %v %v %v", conds, args, e)
	}
	// 따옴표 안의 " and " 는 연결자가 아니다.
	if _, _, cls, e := parseFilter(`displayName eq "a and b"`); e != nil || len(cls) != 1 {
		t.Errorf("따옴표 안 and: %v %v", cls, e)
	}
	for _, bad := range []string{`userName eq "a" or userName eq "b"`, `userName pr`, `foo eq "x"`, `id sw "1"`, `active co true`, `userName eq a`, `emails[type eq "work"].value eq "x"`} {
		if _, _, _, e := parseFilter(bad); e == nil || e.Type != "invalidFilter" {
			t.Errorf("%q 는 invalidFilter 여야 함: %v", bad, e)
		}
	}
	// LIKE 와일드카드는 이스케이프한다.
	_, args, _, _ = parseFilter(`userName sw "a_b%"`)
	if args[0] != `a\_b\%%` {
		t.Errorf("이스케이프: %v", args)
	}
}

func TestDisplayNameAndActive(t *testing.T) {
	in := &userIn{}
	if in.displayNameOf() != "" {
		t.Error("빈 값")
	}
	fam, giv := "김", "철수"
	in.Name = &struct {
		Formatted  *string `json:"formatted"`
		FamilyName *string `json:"familyName"`
		GivenName  *string `json:"givenName"`
	}{FamilyName: &fam, GivenName: &giv}
	if in.displayNameOf() != "김철수" {
		t.Errorf("한글 이름: %q", in.displayNameOf())
	}
	for raw, want := range map[string]bool{`true`: true, `false`: false, `"True"`: true, `"False"`: false} {
		u := &userIn{Active: []byte(raw)}
		if v, set, err := u.activeOf(); err != nil || !set || v != want {
			t.Errorf("active %s: %v %v %v", raw, v, set, err)
		}
	}
	if _, _, err := (&userIn{Active: []byte(`"maybe"`)}).activeOf(); err == nil {
		t.Error("잘못된 active 가 통과함")
	}
}

func TestMaskEmail(t *testing.T) {
	if got := maskEmail("alice@corp.test"); got != "a***@corp.test" {
		t.Error(got)
	}
	if strings.Contains(maskEmail("alice@corp.test"), "lice") {
		t.Error("가려지지 않음")
	}
}
