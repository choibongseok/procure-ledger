// Package onboarding 는 도입 점검표(B38 3단계, 명세 38 5절)와 백업 상태 화면의 서버다.
//
// 점검표 상태는 요청 때 DB 를 읽어 계산하고 캐시하지 않는다(수동 체크박스가 아니다). 사람이 정하는 것은 "해당 없음"과 사유뿐이고
// 표시·해제는 같은 트랜잭션의 감사(onboarding.skip·onboarding.unskip)에 남는다. 항목 판정은 비밀값을 읽지 않는다.
package onboarding

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// Skip 는 "해당 없음" 표시 한 건이다.
type Skip struct {
	Reason string `json:"reason"`
	By     int64  `json:"by"`
	At     string `json:"at"`
}

// Item 은 점검표 한 항목의 계산 결과다.
type Item struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	State     State  `json:"state"`
	Evidence  string `json:"evidence"`
	Link      string `json:"link"`
	Skippable bool   `json:"skippable"`
	Skip      *Skip  `json:"skip,omitempty"`
}

// Summary 는 요약이다. 완료와 해당 없음을 합쳐 "끝난" 것으로 센다.
type Summary struct {
	Total int `json:"total"`
	Done  int `json:"done"`
	NA    int `json:"notApplicable"`
}

// Service 는 점검표 서비스다.
type Service struct {
	db    *sql.DB
	txs   contract.TxManager
	audit audit.Recorder
	env   Env
	now   func() time.Time
}

// Options 는 조립 입력이다.
type Options struct {
	DB    *sql.DB
	Txs   contract.TxManager
	Audit audit.Recorder
	Env   Env
	Now   func() time.Time
}

// New 는 서비스를 만든다.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{db: o.DB, txs: o.Txs, audit: o.Audit, env: o.Env, now: o.Now}
}

// Count 는 점검표 항목 수다(요약 카드가 쓴다).
func Count() int { return len(checks) }

// List 는 항목을 계산한다. 개별 질의가 실패하면 그 항목만 확인하지 못함(unknown)으로 두고 나머지는 계산한다.
func (s *Service) List(ctx context.Context) ([]Item, Summary, error) {
	if _, err := contract.Authorize(ctx, contract.PermOnboardingRead); err != nil {
		return nil, Summary{}, err
	}
	skips, err := s.skips(ctx)
	if err != nil {
		return nil, Summary{}, err
	}
	now := s.now()
	items := make([]Item, 0, len(checks))
	sum := Summary{Total: len(checks)}
	for _, c := range checks {
		it := Item{ID: c.id, Title: c.title, Link: c.link, Skippable: c.skippable}
		r, qerr := c.fn(ctx, s.db, now, s.env)
		if qerr != nil {
			it.State, it.Evidence = StateUnknown, "확인하지 못했습니다"
		} else {
			it.State, it.Evidence = r.state, r.evidence
		}
		if sk, ok := skips[c.id]; ok && c.skippable {
			it.Skip = &sk
			if it.State != StateDone { // 이미 완료된 항목은 완료로 보인다(표시는 남아 있다)
				it.State = StateNA
			}
		}
		switch it.State {
		case StateDone:
			sum.Done++
		case StateNA:
			sum.NA++
		}
		items = append(items, it)
	}
	return items, sum, nil
}

func (s *Service) skips(ctx context.Context) (map[string]Skip, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT item_id, reason, by_user, at FROM onboarding_skips`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Skip{}
	for rows.Next() {
		var id string
		var sk Skip
		if err := rows.Scan(&id, &sk.Reason, &sk.By, &sk.At); err != nil {
			return nil, err
		}
		out[id] = sk
	}
	return out, rows.Err()
}

func find(id string) (check, bool) {
	for _, c := range checks {
		if c.id == id {
			return c, true
		}
	}
	return check{}, false
}

func validation(msg string) error { return fmt.Errorf("%w: %s", contract.ErrValidation, msg) }

// MarkNotApplicable 은 항목을 "해당 없음"으로 표시한다. 사유가 필요하고(1~200자, 제어문자 없음), 표시할 수 없는 항목은 거부한다. 감사 onboarding.skip 이 같은 트랜잭션에 남는다.
func (s *Service) MarkNotApplicable(ctx context.Context, itemID, reason string) error {
	p, err := contract.Authorize(ctx, contract.PermOnboardingRead)
	if err != nil {
		return err
	}
	c, ok := find(itemID)
	if !ok {
		return fmt.Errorf("%w: 알 수 없는 점검 항목입니다", contract.ErrNotFound)
	}
	if !c.skippable {
		return validation("이 항목은 해당 없음으로 표시할 수 없습니다.")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return validation("해당 없음으로 표시하는 사유를 입력하세요.")
	}
	if utf8.RuneCountInString(reason) > 200 {
		return validation("사유는 200자 이내로 입력하세요.")
	}
	for _, r := range reason {
		if r < 0x20 || r == 0x7f {
			return validation("사유에 줄바꿈·제어문자를 넣을 수 없습니다.")
		}
	}
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO onboarding_skips (item_id, reason, by_user, at) VALUES (?, ?, ?, ?)
			ON CONFLICT(item_id) DO UPDATE SET reason = excluded.reason, by_user = excluded.by_user, at = excluded.at`,
			itemID, reason, p.UserID, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "onboarding.skip", TargetType: audit.TargetSetup, TargetID: itemID, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "onboarding_item", New: itemID}, {Key: "reason", New: reason}}})
	})
}

// ClearNotApplicable 은 "해당 없음" 표시를 해제한다. 표시가 없으면 아무것도 하지 않는다(감사도 남기지 않는다).
func (s *Service) ClearNotApplicable(ctx context.Context, itemID string) error {
	p, err := contract.Authorize(ctx, contract.PermOnboardingRead)
	if err != nil {
		return err
	}
	if _, ok := find(itemID); !ok {
		return fmt.Errorf("%w: 알 수 없는 점검 항목입니다", contract.ErrNotFound)
	}
	return s.txs.WithTx(ctx, func(tx contract.Tx) error {
		var old string
		err := tx.QueryRowContext(ctx, `SELECT reason FROM onboarding_skips WHERE item_id = ?`, itemID).Scan(&old)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM onboarding_skips WHERE item_id = ?`, itemID); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "onboarding.unskip", TargetType: audit.TargetSetup, TargetID: itemID, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "onboarding_item", New: itemID}, {Key: "reason", Old: old}}})
	})
}
