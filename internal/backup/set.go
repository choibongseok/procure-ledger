// Package backup 은 erp-admin 의 백업·복구 구현이다(B38 2단계, 6~7절).
//
// 백업 세트는 디렉터리 하나다: manifest.json, DB 스냅샷(db.sqlite 또는 db.pgdump), 앵커 사본(anchor.log), SHA256SUMS.
// 암호화하면 각 파일이 .enc 로 저장된다. 비밀(키·경로·호스트 이름)은 매니페스트에 적지 않는다.
package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FormatVersion 은 백업 세트 형식 번호다. 이 도구가 아는 것보다 큰 세트는 읽지 않는다.
const FormatVersion = 1

const (
	ManifestName = "manifest.json"
	SumsName     = "SHA256SUMS"
	DBFileSQLite = "db.sqlite"
	DBFilePG     = "db.pgdump"
	AnchorName   = "anchor.log"
	setPrefix    = "pl-"
	partialDir   = ".partial-"
	setTimeFmt   = "20060102T150405Z"
)

// FileInfo 는 세트 안 파일 하나의 해시와 크기(저장된 모습 그대로: 암호화했다면 암호문)다.
type FileInfo struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest 는 세트의 설명서다. 비밀·경로·호스트 이름이 없다.
type Manifest struct {
	Format         int                 `json:"format"`
	AppVersion     string              `json:"appVersion"`
	ToolVersion    string              `json:"toolVersion"`
	DBKind         string              `json:"dbKind"` // sqlite | postgres
	Migration      string              `json:"migration"`
	CreatedAt      string              `json:"createdAt"`
	DBFile         string              `json:"dbFile"`
	AnchorFile     string              `json:"anchorFile,omitempty"`
	Files          map[string]FileInfo `json:"files"`
	ChainSeq       int64               `json:"chainSeq"`
	ChainHash      string              `json:"chainHash"`
	AnchorLines    int                 `json:"anchorLines"`
	AnchorStamps   int                 `json:"anchorStamps"`
	Encrypted      bool                `json:"encrypted"`
	KeyFingerprint string              `json:"keyFingerprint,omitempty"`
	Invariants     []string            `json:"invariants,omitempty"` // 백업 시점의 업무 불변식 위반(항목·건수). 백업을 막지는 않는다
	Warnings       []string            `json:"warnings,omitempty"`
}

// SetName 은 세트 디렉터리 이름이다: pl-<UTC 시각>[-<라벨>].
func SetName(at time.Time, label string) string {
	n := setPrefix + at.UTC().Format(setTimeFmt)
	if label != "" {
		n += "-" + label
	}
	return n
}

// ValidLabel 은 세트 이름에 넣을 수 있는 라벨(영문·숫자·밑줄·하이픈, 32자까지)인지 본다. 경로 문자를 막는다.
func ValidLabel(s string) bool {
	if len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// FileSHA256 은 파일의 SHA-256 과 크기다.
func FileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// WriteSums 는 SHA256SUMS(sha256sum 형식)와 manifest.json 을 쓴다.
func WriteSums(dir string, m Manifest) error {
	names := make([]string, 0, len(m.Files))
	for n := range m.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%s  %s\n", m.Files[n].SHA256, n)
	}
	if err := os.WriteFile(filepath.Join(dir, SumsName), []byte(b.String()), 0o600); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ManifestName), append(raw, '\n'), 0o600)
}

// ReadManifest 는 세트의 매니페스트를 읽는다.
func ReadManifest(dir string) (Manifest, error) {
	var m Manifest
	raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return m, fmt.Errorf("매니페스트를 읽을 수 없습니다: %w", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("매니페스트가 올바르지 않습니다: %w", err)
	}
	return m, nil
}

// SetInfo 는 목록에 보이는 세트 한 개다.
type SetInfo struct {
	Name      string
	Path      string
	CreatedAt string
	Size      int64
	Manifest  Manifest
	Complete  bool // 매니페스트가 있고 읽힌다
}

// List 는 dir 아래의 세트들을 이름(=시각) 오름차순으로 돌려준다. .partial 접두 디렉터리는 제외한다.
func List(dir string) ([]SetInfo, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []SetInfo
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), setPrefix) {
			continue
		}
		si := SetInfo{Name: e.Name(), Path: filepath.Join(dir, e.Name())}
		if m, err := ReadManifest(si.Path); err == nil {
			si.Manifest, si.CreatedAt, si.Complete = m, m.CreatedAt, true
			for _, fi := range m.Files {
				si.Size += fi.Size
			}
		}
		out = append(out, si)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
