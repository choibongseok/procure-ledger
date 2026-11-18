package mcptool

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// DefaultRatePerMin·MaxRatePerMin 은 도구별 사용자 분당 호출 한도의 기본값·상한이다(명세 8·4절, D10).
const (
	DefaultRatePerMin = 30
	MaxRatePerMin     = 60
	// DefaultDraftRatePerMin 은 초안(쓰기) 도구의 기본 한도다(명세 8절: 읽기 30, 쓰기 5).
	DefaultDraftRatePerMin = 5
)

// SetEnabled 는 도구를 켜거나 끈다(mcptool:manage). 켜려면 지금 정의가 카탈로그와 맞고, 현재 버전의 시험 실행이 한 번 성공했어야 하며,
// 켜진 도구가 상한(50)을 넘지 않아야 한다. 끄기는 즉시이고 한 명이 한다. 초안 도구는 3단계에서 2인 확인으로 켠다.
func (s *Service) SetEnabled(ctx context.Context, id int64, enabled bool, optList ...EnableOpts) (Detail, error) {
	var opts EnableOpts
	if len(optList) > 0 {
		opts = optList[0]
	}
	p, err := contract.Authorize(ctx, contract.PermMCPToolManage)
	if err != nil {
		return Detail{}, err
	}
	d, err := s.detail(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	if d.Enabled == enabled {
		if enabled {
			return Detail{}, stateErr("이미 켜져 있습니다.")
		}
		return Detail{}, stateErr("이미 꺼져 있습니다.")
	}
	if enabled {
		if d.Kind != "read" {
			return Detail{}, stateErr("초안 도구는 다른 관리자의 확인이 필요합니다. 켜기 요청을 만들어 다른 관리자가 확인하게 하세요.")
		}
		if d.Broken {
			return Detail{}, stateErr("정의가 지금의 자원 목록과 맞지 않아 켤 수 없습니다. 정의를 고쳐 새 버전으로 저장하세요.")
		}
		if !d.Tested {
			return Detail{}, stateErr("현재 버전의 시험 실행이 아직 성공하지 않았습니다. 시험 실행을 한 번 성공시킨 뒤 켜세요.")
		}
		var on int
		if err := s.o.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM mcp_tools WHERE enabled = 1`).Scan(&on); err != nil {
			return Detail{}, err
		}
		if on >= MaxEnabled {
			return Detail{}, stateErr("켠 도구는 " + strconv.Itoa(MaxEnabled) + "개까지입니다. 쓰지 않는 도구를 먼저 끄세요.")
		}
	}
	var ov *EvalResult
	if enabled {
		if ov, err = s.checkEval(ctx, p, d, opts); err != nil {
			return Detail{}, err
		}
	}
	action := "mcptool.disable"
	if enabled {
		action = "mcptool.enable"
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE mcp_tools SET enabled = ?, updated_at = ? WHERE id = ?`, boolInt(enabled), s.now(), id); e != nil {
			return e
		}
		if ov != nil {
			if e := s.o.Audit.Record(ctx, tx, overrideEntry(p, d, *ov, opts.Reason)); e != nil {
				return e
			}
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: action, TargetType: audit.TargetMCPTool, TargetID: d.Name, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "tool_name", New: d.Name}, {Key: "tool_version", New: strconv.Itoa(d.CurrentVersion)}}})
	})
	if err != nil {
		return Detail{}, err
	}
	return s.detail(ctx, id)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SetRate 는 도구별 사용자 분당 호출 한도를 정한다(mcptool:manage, 1~60).
func (s *Service) SetRate(ctx context.Context, id int64, perMin int) (Detail, error) {
	p, err := contract.Authorize(ctx, contract.PermMCPToolManage)
	if err != nil {
		return Detail{}, err
	}
	if perMin < 1 || perMin > MaxRatePerMin {
		return Detail{}, valErr("분당 호출 한도는 1~%d 사이여야 합니다", MaxRatePerMin)
	}
	d, err := s.detail(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	if d.RatePerMin == perMin {
		return Detail{}, stateErr("이미 같은 한도입니다.")
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE mcp_tools SET rate_per_min = ?, updated_at = ? WHERE id = ?`, perMin, s.now(), id); e != nil {
			return e
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "mcptool.limit_set", TargetType: audit.TargetMCPTool, TargetID: d.Name, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "tool_name", New: d.Name}, {Key: "rate_limit", Old: strconv.Itoa(d.RatePerMin), New: strconv.Itoa(perMin)}}})
	})
	if err != nil {
		return Detail{}, err
	}
	return s.detail(ctx, id)
}

// markTested 는 시험 실행을 통과한 정의가 저장된 어느 도구의 버전과 같으면 그 버전에 통과 표시를 남긴다(같은 트랜잭션).
func (s *Service) markTested(ctx context.Context, tx contract.Tx, name, hash string, by int64, count int) error {
	var toolID int64
	var version int
	err := tx.QueryRowContext(ctx, `SELECT t.id, v.version FROM mcp_tools t JOIN mcp_tool_versions v ON v.tool_id = t.id WHERE t.name = ? AND v.def_hash = ? ORDER BY v.version DESC LIMIT 1`, name, hash).Scan(&toolID, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // 아직 저장하지 않은 정의의 시험이다
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mcp_tool_tests (tool_id, version, tested_by, tested_at, result_count) VALUES (?,?,?,?,?)
		ON CONFLICT (tool_id, version) DO UPDATE SET tested_by = ?, tested_at = ?, result_count = ?`, toolID, version, nullID(by), s.now(), count, nullID(by), s.now(), count)
	return err
}

// AllowCall 은 도구별 사용자 분당 호출 한도에 이번 호출을 넣는다(MCP 호출 가드가 부른다). 한도 안이면 true 이고 호출 수가 오른다.
// 한도를 넘은 호출은 호출 수를 올리지 않고(거부가 한도를 다시 소모하지 않는다) 거부 수만 센다. 센 값은 DB 라서 여러 프로세스가 합쳐 적용된다.
func AllowCall(ctx context.Context, txs contract.TxManager, toolID, userID int64, limit int, now time.Time) (bool, error) {
	minute := now.UTC().Format("2006-01-02T15:04")
	allowed := false
	err := txs.WithTx(ctx, func(tx contract.Tx) error {
		res, e := tx.ExecContext(ctx, `INSERT INTO mcp_tool_rate_window (tool_id, user_id, minute, calls, denied) VALUES (?,?,?,1,0)
			ON CONFLICT (tool_id, user_id, minute) DO UPDATE SET calls = mcp_tool_rate_window.calls + 1 WHERE mcp_tool_rate_window.calls < ?`, toolID, userID, minute, limit)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		allowed = n > 0
		if !allowed {
			if _, e = tx.ExecContext(ctx, `UPDATE mcp_tool_rate_window SET denied = denied + 1 WHERE tool_id = ? AND user_id = ? AND minute = ?`, toolID, userID, minute); e != nil {
				return e
			}
		}
		// 10분 지난 창은 정리한다.
		_, e = tx.ExecContext(ctx, `DELETE FROM mcp_tool_rate_window WHERE minute < ?`, now.UTC().Add(-10*time.Minute).Format("2006-01-02T15:04"))
		return e
	})
	return allowed, err
}
