package org

import (
	"context"
	"errors"
	"log"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/privacy"
)

// 개인정보 원문 열람 기록(B41 ①, 명세 41 5.2). 연락처·사번 원문이 응답에 실리는 경로는 응답을 만들기 전에 notePrivacy 를 부르고,
// 기록하지 못하면 원문을 싣지 않고 실패로 응답한다(fail-closed, D2). 조용한 마스킹 강등은 하지 않는다.

// ErrPrivacyNote 는 열람 기록을 남기지 못해 정보를 보여 줄 수 없다는 뜻이다.
var ErrPrivacyNote = errors.New("열람 기록을 남기지 못해 정보를 보여 드릴 수 없습니다. 잠시 뒤 다시 시도하세요")

func (s *Service) notePrivacy(ctx context.Context, p contract.Principal, a privacy.Access) error {
	if s.priv == nil || len(a.Subjects) == 0 {
		return nil
	}
	if err := s.priv.Note(ctx, p, a); err != nil {
		log.Printf("privacy: 열람 기록 실패 화면=%s 범주=%s: %v", a.Screen, a.Category, err)
		return ErrPrivacyNote
	}
	return nil
}

// userSubjects 는 사용자 번호들을 정보주체로 바꾼다(열람자 본인은 뺀다).
func userSubjects(p contract.Principal, ids []int64) []privacy.Subject {
	subs := make([]privacy.Subject, 0, len(ids))
	for _, id := range ids {
		subs = append(subs, privacy.Subject{Type: privacy.SubjectUser, ID: id})
	}
	return privacy.ExcludeSelf(p, subs)
}

// rawContactFields 는 카드에 이메일·휴대전화 원문이 실렸는지(가린 값이 아닌지)로 필드 이름을 돌려준다. 값이 비어 있으면 원문이 아니다.
func rawContactFields(card contract.PersonCard, c contactRow) []string {
	var out []string
	if c.email != "" && card.Email == c.email {
		out = append(out, privacy.FieldEmail)
	}
	if c.phone != "" && card.Phone == c.phone {
		out = append(out, privacy.FieldPhone)
	}
	return out
}

// noteCards 는 viewer 기준으로 만든 카드들 중 연락처 원문이 실린 정보주체를 모아 응답 한 번에 한 번 기록한다.
// 본인이 본인 것을 보는 경우와 가린 응답은 기록하지 않는다.
func (s *Service) noteCards(ctx context.Context, p contract.Principal, screen string, export bool, dataset string, cards []contract.PersonCard, cs map[int64]contactRow) error {
	var ids []int64
	fields := map[string]bool{}
	for _, c := range cards {
		rf := rawContactFields(c, cs[c.UserID])
		if len(rf) == 0 {
			continue
		}
		ids = append(ids, c.UserID)
		for _, f := range rf {
			fields[f] = true
		}
	}
	var fl []string
	for _, f := range []string{privacy.FieldEmail, privacy.FieldPhone} {
		if fields[f] {
			fl = append(fl, f)
		}
	}
	return s.notePrivacy(ctx, p, privacy.Access{Category: privacy.CatContact, Screen: screen, Export: export, Dataset: dataset, Fields: fl, Subjects: userSubjects(p, ids)})
}
