package privacy_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// B41 AC-4105 소스 검사: 마스킹 해제 값(원문)을 만들 수 있는 호출(MaskField·MaskContact·ContactRawAllowed·RevealAccount·RevealPayee)이 있는
// 모든 파일은 이 목록에 있어야 한다. 새 원문 경로는 privacy.Note 를 부르고 noted 에 올려야 통과한다(기록 없는 원문 경로가 조용히 생기지 못하게).

var rawCall = regexp.MustCompile(`\.(MaskField|MaskContact)\(|ContactRawAllowed\(|\.Reveal(Account|Payee)\(`)
var noteUse = regexp.MustCompile(`privacy\.Access\b|\.NoteTx?\(`)

// allowed: 호출해도 응답에 새 원문이 실리지 않는 파일(정의·가짜 구현·가린 값만 쓰는 곳·호출만 하는 핸들러).
var allowed = map[string]string{
	"internal/contract/org.go":             "정의(MaskContact·ContactRawAllowed)",
	"internal/contract/fake/org.go":        "가짜 구현(시험용)",
	"internal/org/scim_support.go":         "가린 값만(감사 diff 마스킹)",
	"internal/service/apiread.go":          "연동 API 세금계산서: 사업자번호를 권한 없는 주체로 가려서만 낸다(API 채널은 원문을 낼 수 없다, B43)",
	"internal/handler/payment_register.go": "핸들러는 서비스 호출만(FundService 가 같은 트랜잭션에서 기록)",
}

// noted: 마스킹 해제 값(원문)을 판정하거나 싣는 파일. 값을 판정하는 헬퍼(maskVendor·cardFor·present)와 그 호출처가 같은 패키지에 있고,
// 호출처가 응답을 만들기 전에 privacy.Note 를 부른다(응답 한 번에 한 기록). 같은 패키지 안에 Note 호출이 있어야 통과한다.
var noted = map[string]string{
	"internal/org/directory.go":              "조직도 사람 카드·목록 연락처: cardFor 는 값만 판정하고 noteCards 가 기록(org/privacy_note.go)",
	"internal/docverify/service.go":          "문서 검증 사업자번호: present·presentSource 의 호출처가 기록",
	"internal/service/inventory_service.go":  "거래처 목록·상세: maskVendor 의 호출처가 기록(service/privacy_note.go)",
	"internal/service/po_print_service.go":   "발주서 출력: maskVendor 의 호출처가 기록",
	"internal/service/taxinvoice_service.go": "세금계산서 사업자번호 원문(D14): out 의 호출처 present·presentList 가 기록(service/taxinvoice_privacy.go)",
}

// pendingNote: 원문 경로이지만 아직 Note 를 넣지 않은 파일. 지금은 없다(새 경로를 추가하다 임시로 올릴 때만 쓴다). Note 를 이미 부르면 실패해 noted 로 옮기게 한다.
var pendingNote = map[string]string{}

// mustNote: 원문 경로가 아니라 기록 호출 지점으로 이미 연결된 파일(호출이 사라지면 실패한다).
var mustNote = []string{"internal/service/fund_service.go", "internal/export/handler.go", "internal/handler/admin_register.go", "internal/auth/http_password.go"}

func repoRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..")
}

func goFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := repoRoot(t)
	for _, dir := range []string{"internal", "cmd"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, _ := os.ReadFile(path)
			rel, _ := filepath.Rel(root, path)
			out[filepath.ToSlash(rel)] = string(b)
			return nil
		})
	}
	return out
}

// packageUsesNote 는 path 와 같은 디렉터리(패키지)의 파일 중 privacy.Note 를 부르는 곳이 있는지다.
func packageUsesNote(files map[string]string, path string) bool {
	dir := filepath.ToSlash(filepath.Dir(path))
	for other, src := range files {
		if filepath.ToSlash(filepath.Dir(other)) == dir && noteUse.MatchString(src) {
			return true
		}
	}
	return false
}

func TestRawPathsAreRecordedOrListed(t *testing.T) {
	files := goFiles(t)
	for path, src := range files {
		if !rawCall.MatchString(src) {
			continue
		}
		_, isAllowed := allowed[path]
		_, isNoted := noted[path]
		_, isPending := pendingNote[path]
		switch {
		case isAllowed:
		case isNoted:
			if !packageUsesNote(files, path) {
				t.Errorf("%s: 원문 경로인데 같은 패키지에 privacy.Note 호출이 없음", path)
			}
		case isPending:
			if noteUse.MatchString(src) {
				t.Errorf("%s: 이미 privacy.Note 를 부르니 pendingNote 에서 noted 로 옮기세요", path)
			}
		default:
			t.Errorf("%s: 마스킹 해제 값을 만들 수 있는 호출이 있는데 허용 목록에 없음. 응답에 원문을 싣는다면 privacy.Note 를 부르고 noted 에 올리세요(명세 41 5.2)", path)
		}
	}
	for _, m := range []map[string]string{noted, pendingNote} {
		for path := range m {
			src, ok := files[path]
			if !ok || !rawCall.MatchString(src) {
				t.Errorf("%s: 목록에 있지만 원문 호출이 없거나 파일이 없음(목록을 정리하세요)", path)
			}
		}
	}
	for _, path := range mustNote {
		if !noteUse.MatchString(files[path]) {
			t.Errorf("%s: privacy.Note 호출이 사라짐", path)
		}
	}
}

// 민감 열(Sensitive: true)이 있는 데이터 세트를 선언하는 파일은 PrivacySpec 도 선언해야 한다(내보내기 핸들러가 원문 포함 요청을 기록할 수 있게).
func TestSensitiveDatasetsDeclarePrivacy(t *testing.T) {
	for path, src := range goFiles(t) {
		if strings.HasPrefix(path, "internal/export/") || strings.HasPrefix(path, "internal/mcp/") {
			continue
		}
		if strings.Contains(src, "Sensitive: true") && !strings.Contains(src, "PrivacySpec") {
			t.Errorf("%s: 민감 열이 있는 데이터 세트는 export.PrivacySpec 을 선언해야 함", path)
		}
	}
}
