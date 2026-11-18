package service

import (
	"context"
	"errors"
	"log"
	"sort"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/domain"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// 개인정보 원문 열람 기록(B41 ①, 명세 41 5.2). 원문이 응답에 실리는 서비스 경로는 응답을 만들기 전에 notePrivacy 를 부른다.
// 기록하지 못하면 원문을 싣지 않고 실패로 응답한다(fail-closed, D2): 조용한 마스킹 강등은 하지 않는다.

// ErrPrivacyNote 는 열람 기록을 남기지 못해 원문을 보여 줄 수 없다는 뜻이다(핸들러는 500 으로 옮긴다).
var ErrPrivacyNote = errors.New("열람 기록을 남기지 못해 정보를 보여 드릴 수 없습니다. 잠시 뒤 다시 시도하세요")

// notePrivacy 는 원문 열람 한 번을 기록한다. 정보주체가 없으면 아무것도 하지 않는다.
// Base.Privacy 가 nil 이면(기록기를 꽂지 않은 시험·도구 조립) 기록 없이 통과한다 — 서버 조립(bootstrap)이 항상 꽂는다.
func (b Base) notePrivacy(ctx context.Context, p contract.Principal, a privacy.Access) error {
	if b.Privacy == nil || len(a.Subjects) == 0 {
		return nil
	}
	if err := b.Privacy.Note(ctx, p, a); err != nil {
		log.Printf("privacy: 열람 기록 실패 화면=%s 범주=%s: %v", a.Screen, a.Category, err)
		return ErrPrivacyNote
	}
	return nil
}

// vendorRawFields 는 가린 결과(masked)가 원본(orig)과 같아 원문이 그대로 실린 민감 필드 이름이다(계좌번호·사업자번호).
// 마스킹 정책(mask:bypass·채널)을 다시 판정하지 않고 결과로 판정하므로, 정책이 바뀌어도 기록이 어긋나지 않는다.
func vendorRawFields(orig, masked domain.Vendor) []string {
	var out []string
	if orig.AccountNo != "" && masked.AccountNo == orig.AccountNo {
		out = append(out, privacy.FieldAccountNo)
	}
	if orig.BusinessNo != "" && masked.BusinessNo == orig.BusinessNo {
		out = append(out, privacy.FieldBusinessNo)
	}
	if orig.Phone != "" && masked.Phone == orig.Phone {
		out = append(out, privacy.FieldPhone)
	}
	if orig.Email != "" && masked.Email == orig.Email {
		out = append(out, privacy.FieldEmail)
	}
	return out
}

// vendorAccesses 는 거래처별 원문 필드(vendorRawFields)를 범주별 기록(Access)으로 묶는다: 계좌번호·사업자번호는 account,
// 전화·이메일은 contact 범주다. 응답 한 번에 범주마다 한 건이고 정보주체는 그 범주의 원문이 실린 거래처다.
func vendorAccesses(screen string, perVendor map[int64][]string) []privacy.Access {
	type bucket struct {
		fields map[string]bool
		subs   []privacy.Subject
	}
	acct, contact := &bucket{fields: map[string]bool{}}, &bucket{fields: map[string]bool{}}
	ids := make([]int64, 0, len(perVendor))
	for id := range perVendor {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		inAcct, inContact := false, false
		for _, f := range perVendor[id] {
			if f == privacy.FieldPhone || f == privacy.FieldEmail {
				contact.fields[f], inContact = true, true
			} else {
				acct.fields[f], inAcct = true, true
			}
		}
		if inAcct {
			acct.subs = append(acct.subs, privacy.Subject{Type: privacy.SubjectVendor, ID: id})
		}
		if inContact {
			contact.subs = append(contact.subs, privacy.Subject{Type: privacy.SubjectVendor, ID: id})
		}
	}
	var out []privacy.Access
	if len(acct.subs) > 0 {
		out = append(out, privacy.Access{Category: privacy.CatAccount, Screen: screen, Fields: sortedKeys(acct.fields), Subjects: acct.subs})
	}
	if len(contact.subs) > 0 {
		out = append(out, privacy.Access{Category: privacy.CatContact, Screen: screen, Fields: sortedKeys(contact.fields), Subjects: contact.subs})
	}
	return out
}

// noteAll 은 Access 여러 건을 차례로 기록한다. 하나라도 실패하면 오류(fail-closed).
func (b Base) noteAll(ctx context.Context, p contract.Principal, list []privacy.Access) error {
	for _, a := range list {
		if err := b.notePrivacy(ctx, p, a); err != nil {
			return err
		}
	}
	return nil
}
