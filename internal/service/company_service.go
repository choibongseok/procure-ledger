package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // 이미지 헤더(크기) 확인용 디코더
	_ "image/png"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

// 회사 설정(B34 단계 1,). 회사 정보·로고·직인. 설정은 admin 만 바꾸고 모든 변경은 같은 트랜잭션의 감사에 이전·새 값이 남는다.
// 설정이 한 번도 저장되지 않았으면 회사명은 비어 있고(출력은 조직도 최상위 조직 이름을 쓴다), 동작은 현행과 같다.

const (
	companyAssetMaxBytes = 512 * 1024
	companyAssetMaxDim   = 2000
)

var companyBusinessNoRe = regexp.MustCompile(`^\d{3}-\d{2}-\d{5}$`)

type CompanyService struct {
	Base
	Repo repository.SQLCompany
}

var _ domain.CompanyService = (*CompanyService)(nil)

func NewCompanyService(b Base) *CompanyService { return &CompanyService{Base: b} }

// companyInfo 는 설정 읽기 한 곳이다. 출력(발주서·결재 문서)이 회사 정보를 얻는 유일한 함수다.
func (b Base) companyInfo(ctx context.Context, q contract.Tx) (domain.CompanyInfo, error) {
	return repository.SQLCompany{}.Get(ctx, q)
}

func (s *CompanyService) Info(ctx context.Context) (domain.CompanyInfo, error) {
	if _, err := contract.Authorize(ctx, contract.PermCompanyRead); err != nil {
		return domain.CompanyInfo{}, err
	}
	return s.companyInfo(ctx, s.DB)
}

func tooLong(label, v string, max int) error {
	if utf8.RuneCountInString(v) > max {
		return fmt.Errorf("%w: %s 은(는) %d자 이내로 입력하세요.", contract.ErrValidation, label, max)
	}
	return nil
}

func validateCompany(in *domain.CompanyInput) error {
	in.Name, in.BusinessNo, in.Representative = strings.TrimSpace(in.Name), strings.TrimSpace(in.BusinessNo), strings.TrimSpace(in.Representative)
	in.Address, in.Phone = strings.TrimSpace(in.Address), strings.TrimSpace(in.Phone)
	if in.Name == "" {
		return fmt.Errorf("%w: 회사명을 입력하세요.", contract.ErrValidation)
	}
	for _, c := range []struct {
		label, v string
		max      int
	}{{"회사명", in.Name, 100}, {"대표자", in.Representative, 50}, {"주소", in.Address, 200}, {"대표 전화", in.Phone, 30}} {
		if err := tooLong(c.label, c.v, c.max); err != nil {
			return err
		}
	}
	if in.BusinessNo != "" && !companyBusinessNoRe.MatchString(in.BusinessNo) {
		return fmt.Errorf("%w: 사업자등록번호는 000-00-00000 형식으로 입력하세요.", contract.ErrValidation)
	}
	return nil
}

// Update 는 회사 정보를 저장한다. version 은 읽은 시점의 값(처음 저장은 0)이고 다르면 먼저 바꾼 사람이 있다는 안내로 거부한다.
func (s *CompanyService) Update(ctx context.Context, in domain.CompanyInput, version int64) (domain.CompanyInfo, error) {
	p, err := s.authorize(ctx, contract.PermCompanyManage, "company.update", audit.TargetSetting)
	if err != nil {
		return domain.CompanyInfo{}, err
	}
	if err := validateCompany(&in); err != nil {
		return domain.CompanyInfo{}, err
	}
	if err := requireDefaultEntityForCompanyEdit(ctx); err != nil {
		return domain.CompanyInfo{}, err
	}
	var out domain.CompanyInfo
	err = s.write(ctx, p, "company.update", audit.TargetSetting, 0, func(tx contract.Tx) error {
		cur, err := s.Repo.Get(ctx, tx)
		if err != nil {
			return err
		}
		ok, err := s.Repo.Save(ctx, tx, in, version, p.UserID, s.now())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: 다른 관리자가 먼저 바꿨습니다. 새로 고친 뒤 다시 저장하세요.", contract.ErrInvalidState)
		}
		if out, err = s.Repo.Get(ctx, tx); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "company.update", audit.TargetSetting, 0,
			audit.DiffItem{Key: "company_name", Old: cur.Name, New: in.Name}, audit.DiffItem{Key: "business_no", Old: cur.BusinessNo, New: in.BusinessNo},
			audit.DiffItem{Key: "representative", Old: cur.Representative, New: in.Representative}, audit.DiffItem{Key: "address", Old: cur.Address, New: in.Address},
			audit.DiffItem{Key: "phone", Old: cur.Phone, New: in.Phone})
	})
	return out, err
}

// SetAsset 은 로고·직인 이미지를 교체한다(PNG·JPEG, 512KB·2,000px 이하). 이전 이미지는 이력으로 남는다.
func (s *CompanyService) SetAsset(ctx context.Context, kind string, data []byte) (domain.CompanyInfo, error) {
	action := "company.logo_set"
	if kind == domain.CompanyAssetSeal {
		action = "company.seal_set"
	} else if kind != domain.CompanyAssetLogo {
		return domain.CompanyInfo{}, fmt.Errorf("%w: 이미지 종류는 로고 또는 직인이어야 합니다.", contract.ErrValidation)
	}
	p, err := s.authorize(ctx, contract.PermCompanyManage, action, audit.TargetSetting)
	if err != nil {
		return domain.CompanyInfo{}, err
	}
	a, err := checkCompanyImage(kind, data)
	if err != nil {
		return domain.CompanyInfo{}, err
	}
	var out domain.CompanyInfo
	err = s.write(ctx, p, action, audit.TargetSetting, 0, func(tx contract.Tx) error {
		old, oerr := s.Repo.Asset(ctx, tx, kind)
		if oerr != nil && !errors.Is(oerr, contract.ErrNotFound) {
			return oerr
		}
		if err := s.Repo.SetAsset(ctx, tx, a, p.UserID, s.now()); err != nil {
			return err
		}
		if out, err = s.Repo.Get(ctx, tx); err != nil {
			return err
		}
		oldHash := ""
		if oerr == nil {
			oldHash = old.SHA256[:16]
		}
		return s.record(ctx, tx, p, action, audit.TargetSetting, 0,
			audit.DiffItem{Key: "asset_hash", Old: oldHash, New: a.SHA256[:16]}, audit.DiffItem{Key: "asset_size", New: fmt.Sprint(a.Size)})
	})
	return out, err
}

// sniffImage 는 내용 서명(매직 바이트)으로 PNG·JPEG 를 가린다. 그 밖(SVG·GIF·확장자만 맞춘 파일)은 빈 값이다.
func sniffImage(b []byte) string {
	switch {
	case len(b) >= 8 && bytes.Equal(b[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	}
	return ""
}

// checkCompanyImage 는 내용 서명으로 PNG·JPEG 만 받고(SVG·GIF·확장자만 맞춘 파일 거부) 크기·치수 상한을 본다.
func checkCompanyImage(kind string, data []byte) (domain.CompanyAsset, error) {
	if len(data) == 0 {
		return domain.CompanyAsset{}, fmt.Errorf("%w: 이미지 파일을 선택하세요.", contract.ErrValidation)
	}
	if len(data) > companyAssetMaxBytes {
		return domain.CompanyAsset{}, fmt.Errorf("%w: 이미지는 512KB 이하로 올려 주세요.", contract.ErrValidation)
	}
	ct := sniffImage(data)
	if ct == "" {
		return domain.CompanyAsset{}, fmt.Errorf("%w: PNG 또는 JPG 이미지만 올릴 수 있습니다.", contract.ErrValidation)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return domain.CompanyAsset{}, fmt.Errorf("%w: 이미지를 읽을 수 없습니다. PNG 또는 JPG 파일인지 확인하세요.", contract.ErrValidation)
	}
	if cfg.Width > companyAssetMaxDim || cfg.Height > companyAssetMaxDim {
		return domain.CompanyAsset{}, fmt.Errorf("%w: 이미지는 가로·세로 2,000픽셀 이하로 올려 주세요.", contract.ErrValidation)
	}
	sum := sha256.Sum256(data)
	return domain.CompanyAsset{Kind: kind, ContentType: ct, Data: data, Size: len(data), Width: cfg.Width, Height: cfg.Height, SHA256: hex.EncodeToString(sum[:])}, nil
}

// Asset 은 현재 이미지다(로그인한 사용자는 출력에 필요해 누구나 받는다). 없으면 contract.ErrNotFound.
func (s *CompanyService) Asset(ctx context.Context, kind string) (domain.CompanyAsset, error) {
	if _, err := contract.Authorize(ctx, contract.PermCompanyRead); err != nil {
		return domain.CompanyAsset{}, err
	}
	if kind != domain.CompanyAssetLogo && kind != domain.CompanyAssetSeal {
		return domain.CompanyAsset{}, contract.ErrNotFound
	}
	return s.Repo.Asset(ctx, s.DB, kind)
}

// requireDefaultEntityForCompanyEdit 는 회사 설정 화면이 기본 법인의 정보만 고치게 한다. 다른 법인의 회사 정보는 관리 > 법인 관리에서 고친다(B44 20절).
func requireDefaultEntityForCompanyEdit(ctx context.Context) error {
	if contract.WriteEntityID(ctx) != contract.DefaultEntityID {
		return fmt.Errorf("%w: 이 법인의 회사 정보는 관리 > 법인 관리에서 고칩니다", contract.ErrInvalidState)
	}
	return nil
}
