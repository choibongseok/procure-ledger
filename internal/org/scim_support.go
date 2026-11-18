package org

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 외부 계정 동기화(SCIM, B40)가 쓰는 조직 보조 함수. 발령이 아닌 값(표시 이름)과 부서 값 해석만 둔다. 입사·퇴사·전보는 Command 경로(RegisterBatch)를 그대로 쓴다.

// ErrUnitNotFound 는 부서 값에 맞는 열린 조직이 없다는 뜻이다(자동 신설은 하지 않는다, 명세 40 D5).
var ErrUnitNotFound = userError{"해당하는 조직을 찾을 수 없습니다."}

// ErrUnitAmbiguous 는 이름이 같은 열린 조직이 둘 이상이라 하나로 정할 수 없다는 뜻이다.
var ErrUnitAmbiguous = userError{"같은 이름의 조직이 둘 이상이라 하나로 정할 수 없습니다. 조직 코드로 지정하세요."}

// ResolveUnitRef 는 외부 시스템이 보낸 부서 값(조직 코드 또는 이름)을 오늘 열려 있는 조직 번호로 바꾼다.
// 코드를 먼저 보고(코드는 유일하다), 코드가 아니면 이름이 정확히 같은 조직이 하나일 때만 받는다. 폐지된 조직은 후보가 아니다.
// 없으면 ErrUnitNotFound, 이름이 겹치면 ErrUnitAmbiguous 다(둘 다 contract.ErrValidation 으로도 판정된다).
func (s *Service) ResolveUnitRef(ctx context.Context, tx contract.Tx, ref string) (int64, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, ErrUnitNotFound
	}
	res, err := s.foldAll(ctx, tx)
	if err != nil {
		return 0, err
	}
	sn := NewSnap(res, s.Today())
	var byName []int64
	for id, u := range sn.Units {
		if u.Closed {
			continue
		}
		if u.Code == ref {
			return id, nil
		}
		if u.Name == ref {
			byName = append(byName, id)
		}
	}
	switch len(byName) {
	case 0:
		return 0, ErrUnitNotFound
	case 1:
		return byName[0], nil
	}
	return 0, ErrUnitAmbiguous
}

// UpdateProfile 는 사용자의 표시 이름을 바꾼다(발령이 아니라 이력은 감사 user.update 로 남는다). 이메일·사번은 바꾸지 않는다(명세 40 D13).
// 같은 이름이면 아무것도 쓰지 않는다. 권한 판단은 호출자가 한다(actor 는 감사 행위자다).
func (s *Service) UpdateProfile(ctx context.Context, actor contract.Principal, userID int64, displayName string) error {
	name := strings.TrimSpace(displayName)
	if name == "" {
		return verr("이름을 입력하세요.")
	}
	if p := cellProblem(name); p != "" {
		return verr("이름: " + p)
	}
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM users WHERE id = ?`, userID).Scan(&old); err != nil {
			return contract.ErrNotFound
		}
		if old == name {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET name = ? WHERE id = ?`, name, userID); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: "user.update", TargetType: audit.TargetUser,
			TargetID: strconv.FormatInt(userID, 10), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "name", Old: old, New: name}}})
	})
}

// UpdateContact 는 사용자의 휴대전화(org_people.phone)만 바꾼다(발령이 아니라 이력은 감사 user.update 로 남는다). 내선·사번·이메일은 건드리지 않는다.
// 빈 값은 지움이다(SCIM 의 phoneNumbers remove). 같은 값이면 아무것도 쓰지 않는다. 형식은 조직 가져오기와 같은 규칙(phoneRe)이다.
// 감사 diff 의 값은 마스킹해서 남기고 원문은 싣지 않는다. 조직에 등록되지 않은 사용자는 ErrNotInOrg 다. 권한 판단은 호출자가 한다.
func (s *Service) UpdateContact(ctx context.Context, actor contract.Principal, userID int64, phone string) error {
	phone = strings.TrimSpace(phone)
	if phone != "" {
		if p := cellProblem(phone); p != "" {
			return verr("전화번호: " + p)
		}
		if !phoneRe.MatchString(phone) {
			return verr("전화번호 형식이 올바르지 않습니다.")
		}
	}
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return contract.ErrNotFound
		}
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT phone FROM org_people WHERE user_id = ?`, userID).Scan(&old); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return contract.ErrNotInOrg
			}
			return err
		}
		if old == phone {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE org_people SET phone = ? WHERE user_id = ?`, phone, userID); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{Actor: actor, Action: "user.update", TargetType: audit.TargetUser,
			TargetID: strconv.FormatInt(userID, 10), Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "contact_phone", Old: s.maskPhone(old), New: s.maskPhone(phone)}}})
	})
}

// maskPhone 은 감사에 남길 전화번호를 가린다(마스커가 없으면 숫자를 모두 가린다). 빈 값은 그대로 빈 값이다.
func (s *Service) maskPhone(v string) string {
	if v == "" {
		return ""
	}
	if s.masker == nil {
		return strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return '*'
			}
			return r
		}, v)
	}
	return s.masker.MaskField(contract.Principal{Role: contract.RoleStaff}, contract.FieldPhone, v)
}
