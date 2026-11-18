package backup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 판정 항목 상태. 한국어로 출력한다.
const (
	Pass = "통과"
	Warn = "경고"
	Fail = "실패"
)

// Check 는 판정표의 한 줄이다.
type Check struct {
	Item   string
	Status string
	Detail string
}

// VerifyOptions 는 세트 검증 입력이다.
type VerifyOptions struct {
	Key       []byte // 암호화 세트의 복호화 키
	AnchorKey []byte // 앵커 서명 키(있으면 앵커 대조까지 한다)
	TmpDir    string // 복호화 사본을 둘 곳(기본: 세트 옆의 임시 디렉터리)
}

// VerifyReport 는 세트 검증 결과다.
type VerifyReport struct {
	Checks   []Check
	OK       bool // 실패 항목이 없다
	Manifest Manifest
	Report   *Report // SQLite 세트의 DB 점검 결과
}

// FirstFailure 는 첫 실패 줄의 설명이다.
func (r VerifyReport) FirstFailure() string {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return c.Item + ": " + c.Detail
		}
	}
	return ""
}

func (r *VerifyReport) add(item, status, detail string) {
	r.Checks = append(r.Checks, Check{item, status, detail})
	if status == Fail {
		r.OK = false
	}
}

// parseSums 는 SHA256SUMS 를 {파일: 해시} 로 읽는다.
func parseSums(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "  ", 2)
		if len(parts) == 2 {
			out[parts[1]] = parts[0]
		}
	}
	return out, sc.Err()
}

// Materialize 는 세트의 DB(와 앵커 사본)를 work 디렉터리에 평문으로 꺼낸다. 암호화 세트는 복호화한다.
func Materialize(setDir string, m Manifest, key []byte, work string) (dbPath, anchorPath string, err error) {
	place := func(stored, plain string) (string, error) {
		src := filepath.Join(setDir, stored)
		dst := filepath.Join(work, plain)
		if m.Encrypted {
			return dst, DecryptFile(src, dst, key)
		}
		in, err := os.ReadFile(src)
		if err != nil {
			return dst, err
		}
		return dst, os.WriteFile(dst, in, 0o600)
	}
	plainDB := strings.TrimSuffix(m.DBFile, ".enc")
	if dbPath, err = place(m.DBFile, plainDB); err != nil {
		return "", "", err
	}
	if m.AnchorFile != "" {
		plainA := strings.TrimSuffix(m.AnchorFile, ".enc")
		if anchorPath, err = place(m.AnchorFile, plainA); err != nil {
			return "", "", err
		}
	}
	return dbPath, anchorPath, nil
}

// VerifySet 은 세트를 점검해 판정표를 돌려준다(명세 6.1 verify-backup, 7.1-2, 7.4). 오류는 점검을 못 한 경우이고, 항목 실패는 판정표에 담긴다.
func VerifySet(ctx context.Context, setDir string, o VerifyOptions) (VerifyReport, error) {
	rep := VerifyReport{OK: true}
	m, err := ReadManifest(setDir)
	if err != nil {
		rep.add("세트 무결성", Fail, err.Error())
		return rep, nil
	}
	rep.Manifest = m
	if m.Format < 1 || m.Format > FormatVersion {
		rep.add("형식 버전", Fail, fmt.Sprintf("이 도구가 읽을 수 없는 형식 버전입니다(세트 %d, 지원 %d)", m.Format, FormatVersion))
		return rep, nil
	}
	rep.add("형식 버전", Pass, fmt.Sprintf("%d", m.Format))

	// 해시: SHA256SUMS 와 매니페스트 둘 다 실제 파일과 맞아야 한다.
	sums, serr := parseSums(filepath.Join(setDir, SumsName))
	if serr != nil {
		rep.add("해시", Fail, "SHA256SUMS 를 읽을 수 없습니다")
		return rep, nil
	}
	bad := ""
	for name, fi := range m.Files {
		h, size, err := FileSHA256(filepath.Join(setDir, name))
		switch {
		case err != nil:
			bad = name + " 파일을 읽을 수 없습니다"
		case h != fi.SHA256 || size != fi.Size:
			bad = name + " 의 내용이 매니페스트와 다릅니다(훼손 또는 변조)"
		case sums[name] != h:
			bad = name + " 의 해시가 SHA256SUMS 와 다릅니다(훼손 또는 변조)"
		}
		if bad != "" {
			break
		}
	}
	if len(m.Files) == 0 {
		bad = "세트에 파일이 없습니다"
	}
	if bad != "" {
		rep.add("해시", Fail, bad)
		return rep, nil
	}
	rep.add("해시", Pass, fmt.Sprintf("파일 %d개 일치", len(m.Files)))

	if m.Encrypted {
		if len(o.Key) == 0 {
			rep.add("복호화", Fail, "암호화된 세트입니다. PL_BACKUP_KEY 가 필요합니다")
			return rep, nil
		}
		if Fingerprint(o.Key) != m.KeyFingerprint {
			rep.add("복호화", Fail, "키 지문이 세트와 다릅니다(다른 키입니다)")
			return rep, nil
		}
	}
	if m.DBKind == "postgres" {
		rep.add("DB 스냅샷", Pass, "PostgreSQL 덤프는 복구할 때 pg_restore 로 읽습니다(여기서는 해시만 확인)")
		return rep, nil
	}

	work, err := os.MkdirTemp(o.TmpDir, ".verify-")
	if err != nil {
		return rep, err
	}
	defer os.RemoveAll(work)
	dbPath, anchorPath, err := Materialize(setDir, m, o.Key, work)
	if err != nil {
		if errors.Is(err, ErrWrongKey) {
			rep.add("복호화", Fail, err.Error())
			return rep, nil
		}
		return rep, err
	}
	if m.Encrypted {
		rep.add("복호화", Pass, "")
	}
	r, err := Inspect(ctx, dbPath, anchorPath, o.AnchorKey)
	if err != nil {
		return rep, err
	}
	rep.Report = &r
	rep.addDBChecks(m, r, o.AnchorKey)
	return rep, nil
}

// addDBChecks 는 DB 점검 결과를 판정표 줄로 옮긴다(백업 검증과 복구 판정이 같은 줄을 쓴다).
func (rep *VerifyReport) addDBChecks(m Manifest, r Report, anchorKey []byte) {
	if r.Integrity == "ok" {
		rep.add("무결성 검사", Pass, "integrity_check ok")
	} else {
		rep.add("무결성 검사", Fail, r.Integrity)
		return
	}
	if r.FKViolations == 0 {
		rep.add("외래 키", Pass, "위반 없음")
	} else {
		rep.add("외래 키", Fail, fmt.Sprintf("위반 %d건", r.FKViolations))
	}
	switch {
	case r.Migration == "":
		rep.add("마이그레이션 번호", Fail, "적용된 마이그레이션을 읽을 수 없습니다")
	case r.Migration != m.Migration:
		rep.add("마이그레이션 번호", Fail, fmt.Sprintf("매니페스트 %s, DB %s", m.Migration, r.Migration))
	default:
		rep.add("마이그레이션 번호", Pass, r.Migration)
	}
	if r.ChainOK {
		rep.add("감사 체인", Pass, fmt.Sprintf("기록 %d건 검증", r.ChainChecked))
	} else {
		rep.add("감사 체인", Fail, fmt.Sprintf("%s(기록 %d번)", r.ChainWhy, r.ChainSeq))
	}
	if r.ChainOK && (r.ChainSeq != m.ChainSeq || r.ChainHash != m.ChainHash) {
		rep.add("체인 머리 일치", Fail, fmt.Sprintf("매니페스트 %d번, DB %d번", m.ChainSeq, r.ChainSeq))
	} else if r.ChainOK {
		rep.add("체인 머리 일치", Pass, fmt.Sprintf("%d번", r.ChainSeq))
	}
	switch {
	case len(anchorKey) == 0:
		rep.add("앵커 대조", Warn, "앵커 검증 안 됨: 앵커 키(PL_AUDIT_ANCHOR_KEY)가 없습니다")
	case !r.AnchorKnown:
		rep.add("앵커 대조", Warn, "앵커 검증 안 됨: 세트에 앵커 사본이 없습니다")
	case r.Anchor.State == "ok":
		rep.add("앵커 대조", Pass, r.Anchor.Message)
	case r.Anchor.State == "empty":
		rep.add("앵커 대조", Warn, "앵커 검증 안 됨: "+r.Anchor.Message)
	default:
		rep.add("앵커 대조", Warn, "앵커 이상: "+r.Anchor.Message)
	}
	if len(r.Invariants) == 0 {
		rep.add("불변식", Pass, "전표 차대변·재고 합계 대 원장 합·번호 카운터 일치")
	} else {
		rep.add("불변식", Warn, "업무 불변식 위반(복구는 진행할 수 있습니다): "+strings.Join(r.Invariants, "; "))
	}
}
