package rehearsal

import (
	"os"
	"path/filepath"
	"testing"
)

func mustTouch(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// 판정 규칙(D15): 임시 디렉터리 아래이거나 .rehearsal 마커가 있는 디렉터리의 SQLite 만 허용한다. 그 밖과 PG 는 거부한다.
func TestAllowedMatrix(t *testing.T) {
	temp := t.TempDir() // 임시 디렉터리 아래
	other := t.TempDir()

	// 임시 디렉터리 기준을 다른 값으로 바꿔 "운영 경로" 를 만든다.
	prod := filepath.Join(other, "srv", "data", "erp.db")
	mustTouch(t, prod)
	if err := allowed(false, prod, temp); err == nil {
		t.Error("임시 디렉터리 밖의 운영 경로가 허용됨")
	}
	// 마커가 있으면 임시 디렉터리 밖이어도 허용된다(erp-admin 이 리허설 작업 디렉터리에 만든다).
	work := filepath.Join(other, "rehearsal-work")
	mustTouch(t, filepath.Join(work, Marker))
	mustTouch(t, filepath.Join(work, "db.sqlite"))
	if err := allowed(false, filepath.Join(work, "db.sqlite"), temp); err != nil {
		t.Errorf("마커 디렉터리가 거부됨: %v", err)
	}
	// 임시 디렉터리 아래(아직 없는 파일 포함)는 허용된다.
	if err := allowed(false, filepath.Join(temp, "x", "db.sqlite"), temp); err != nil {
		t.Errorf("임시 디렉터리 아래가 거부됨: %v", err)
	}
	if err := allowed(false, filepath.Join(temp, "db.sqlite"), temp); err != nil {
		t.Errorf("임시 디렉터리 바로 아래가 거부됨: %v", err)
	}
	// 거부: PG, 빈 경로, 메모리 DB.
	for name, c := range map[string]struct {
		pg   bool
		path string
	}{"PG": {true, filepath.Join(temp, "db.sqlite")}, "빈 경로": {false, ""}, "메모리": {false, ":memory:"}} {
		if err := allowed(c.pg, c.path, temp); err == nil {
			t.Errorf("%s 가 허용됨", name)
		}
	}
	// ".." 로 임시 디렉터리를 빠져나가는 경로는 허용되지 않는다.
	escape := filepath.Join(temp, "..", filepath.Base(other), "srv", "data", "erp.db")
	if err := allowed(false, escape, temp); err == nil {
		t.Error(".. 로 빠져나간 운영 경로가 허용됨")
	}
	// 임시 디렉터리 이름만 닮은 형제 디렉터리(접두 일치)는 허용되지 않는다.
	sib := temp + "-sibling"
	mustTouch(t, filepath.Join(sib, "db.sqlite"))
	t.Cleanup(func() { _ = os.RemoveAll(sib) })
	if err := allowed(false, filepath.Join(sib, "db.sqlite"), temp); err == nil {
		t.Error("접두만 같은 형제 디렉터리가 허용됨")
	}
	// 마커 이름의 디렉터리(파일이 아님)도 존재로 본다 — 마커 없는 일반 디렉터리와 구분만 한다.
	if err := allowed(false, filepath.Join(other, "plain", "db.sqlite"), temp); err == nil {
		t.Error("마커도 없고 임시 디렉터리도 아닌 경로가 허용됨")
	}
}

// 심볼릭 링크로 임시 디렉터리 안에서 운영 디렉터리를 가리켜도 실제 위치로 판정한다.
func TestSymlinkIntoProductionIsRejected(t *testing.T) {
	temp := t.TempDir()
	prod := t.TempDir() // 임시 기준이 아닌 다른 루트
	mustTouch(t, filepath.Join(prod, "data", "erp.db"))
	link := filepath.Join(temp, "link")
	if err := os.Symlink(filepath.Join(prod, "data"), link); err != nil {
		t.Skip("심볼릭 링크를 만들 수 없음")
	}
	if err := allowed(false, filepath.Join(link, "erp.db"), temp); err == nil {
		t.Error("임시 디렉터리 안의 링크가 운영 디렉터리를 가리키는데 허용됨")
	}
}

// 공개 함수는 운영체제 임시 디렉터리를 쓴다.
func TestAllowedUsesOSTempDir(t *testing.T) {
	if err := Allowed(false, filepath.Join(t.TempDir(), "db.sqlite")); err != nil {
		t.Errorf("t.TempDir() 아래가 거부됨: %v", err)
	}
	if err := Allowed(true, filepath.Join(t.TempDir(), "db.sqlite")); err == nil {
		t.Error("PG 가 허용됨")
	}
}
