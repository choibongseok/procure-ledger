package org

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// AC-62: 조직 테이블과 users.department_id·users.position 을 쓰는 SQL 은 internal/org 안에만 있어야 한다.
// 비테스트 Go 소스 전체를 훑는다(마이그레이션·테스트 시드는 대상이 아니다).
// legacyUserUpdate 는 옛 경로가 남은 파일의 예외 목록이다. 3단계에서 비웠고 늘리지 않는다.
var legacyUserUpdate = map[string]bool{}

var (
	orgTableWrite  = regexp.MustCompile(`(?i)(INSERT\s+(OR\s+\w+\s+)?INTO|UPDATE|DELETE\s+FROM|REPLACE\s+INTO)\s+org_\w+`)
	userProjection = regexp.MustCompile("(?is)UPDATE\\s+users\\s+SET[^`\"]*\\b(department_id|position)\\b")
	departmentsSQL = regexp.MustCompile(`(?i)(UPDATE|DELETE\s+FROM)\s+departments\b`)
)

func TestOrgWritesOnlyInsideOrgPackage(t *testing.T) {
	root := filepath.Join("..", "..")
	var found []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "web", "bin", "ui-screenshots":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/org/") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		if orgTableWrite.MatchString(src) {
			found = append(found, rel+": 조직 테이블에 쓰는 SQL")
		}
		if departmentsSQL.MatchString(src) {
			found = append(found, rel+": departments 를 고치거나 지우는 SQL")
		}
		if userProjection.MatchString(src) && !legacyUserUpdate[rel] {
			found = append(found, rel+": users.department_id·position 을 바꾸는 SQL")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Fatalf("internal/org 밖에서 조직·소속을 쓰는 코드가 있다:\n  %s", strings.Join(found, "\n  "))
	}
}
