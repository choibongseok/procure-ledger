package privacy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// VerifyResult 는 조회 기록 체인 검증 결과다.
type VerifyResult struct {
	OK             bool   `json:"ok"`
	ChainOK        bool   `json:"chainOk"`
	Checked        int64  `json:"checked"`
	BrokenID       int64  `json:"brokenId,omitempty"`
	Why            string `json:"why,omitempty"`
	DigestsChecked int    `json:"digestsChecked"`
	DigestOK       bool   `json:"digestOk"`
	DigestWhy      string `json:"digestWhy,omitempty"`
	Purged         bool   `json:"purged"` // 파기한 달이 있어 남은 구간만 검증했다
}

const verifyBatch = 1000

type logRow struct {
	id                                           int64
	at, role, channel, category, screen, dataset string
	actor                                        int64
	token                                        sql.NullInt64
	export, count                                int
	fields, subjHash, exposure, prev, hash       string
}

// Verify 는 남은 행의 체인(이전 해시 연결·행 해시·정보주체 해시·건수)을 처음부터 끝까지 확인하고, 감사 체인의 하루 요약이 가리키는
// 행이 그대로 있는지 확인한다(말단 삭제 감지). 읽기 전용이다.
func (r *Recorder) Verify(ctx context.Context) (VerifyResult, error) {
	res := VerifyResult{ChainOK: true, DigestOK: true}
	// 파기한 달의 머리 해시: 남은 첫 행의 이전 해시가 이어 가야 할 기준이다.
	segs, err := r.segments(ctx)
	if err != nil {
		return res, err
	}
	res.Purged = len(segs) > 0
	var after int64
	expectedPrev := ""
	first := true
	for {
		rows, err := r.db.QueryContext(ctx, `SELECT id, at, actor_id, actor_role, channel, token_id, category, screen, dataset, is_export, fields, subject_count, subjects_hash, exposure, prev_hash, hash
			FROM privacy_access_log WHERE id > ? ORDER BY id LIMIT ?`, after, verifyBatch)
		if err != nil {
			return res, err
		}
		var batch []logRow
		for rows.Next() {
			var x logRow
			if err := rows.Scan(&x.id, &x.at, &x.actor, &x.role, &x.channel, &x.token, &x.category, &x.screen, &x.dataset, &x.export, &x.fields, &x.count, &x.subjHash, &x.exposure, &x.prev, &x.hash); err != nil {
				rows.Close()
				return res, err
			}
			batch = append(batch, x)
		}
		rows.Close()
		if len(batch) == 0 {
			break
		}
		subs, err := r.subjectsIn(ctx, batch[0].id, batch[len(batch)-1].id)
		if err != nil {
			return res, err
		}
		for _, x := range batch {
			if first {
				expectedPrev = ""
				for _, sg := range segs { // x 앞에서 끝난 가장 마지막 파기 구간의 머리 해시
					if sg.lastID < x.id && sg.lastID >= 0 {
						expectedPrev = sg.head
					}
				}
				first = false
			}
			fail := func(why string) (VerifyResult, error) {
				res.ChainOK, res.OK, res.BrokenID, res.Why = false, false, x.id, why
				res.DigestOK = false
				res.DigestWhy = "체인이 깨져 요약 대조를 하지 않았습니다"
				return res, nil
			}
			if x.prev != expectedPrev {
				return fail("직전 행과 이어지지 않습니다(행이 지워지거나 바뀜)")
			}
			list := subs[x.id]
			var tok int64
			if x.token.Valid {
				tok = x.token.Int64
			}
			if want := rowHash(x.prev, x.at, x.actor, x.role, x.channel, tok, x.category, x.screen, x.dataset, x.export, x.fields, x.count, subjectsHash(list), x.exposure); want != x.hash {
				return fail("행 내용이 해시와 맞지 않습니다(행 또는 정보주체 목록이 바뀜)")
			}
			if x.subjHash != subjectsHash(list) {
				return fail("정보주체 목록이 기록 당시와 다릅니다")
			}
			if len(list) > x.count {
				return fail("정보주체 수가 기록된 건수보다 많습니다")
			}
			expectedPrev = x.hash
			after = x.id
			res.Checked++
		}
	}
	dres, err := r.verifyDigests(ctx, segs)
	if err != nil {
		return res, err
	}
	res.DigestsChecked, res.DigestOK, res.DigestWhy = dres.n, dres.ok, dres.why
	res.OK = res.ChainOK && res.DigestOK
	return res, nil
}

type segment struct {
	month           string
	firstID, lastID int64
	head            string
}

func (r *Recorder) segments(ctx context.Context) ([]segment, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT month, first_id, last_id, head_hash FROM privacy_log_segments ORDER BY last_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []segment
	for rows.Next() {
		var s segment
		if err := rows.Scan(&s.month, &s.firstID, &s.lastID, &s.head); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Recorder) subjectsIn(ctx context.Context, from, to int64) (map[int64][]Subject, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT log_id, subject_type, subject_id FROM privacy_access_subjects WHERE log_id BETWEEN ? AND ? ORDER BY log_id, subject_type, subject_id`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Subject{}
	for rows.Next() {
		var id int64
		var s Subject
		if err := rows.Scan(&id, &s.Type, &s.ID); err != nil {
			return nil, err
		}
		out[id] = append(out[id], s)
	}
	return out, rows.Err()
}

type digestResult struct {
	n   int
	ok  bool
	why string
}

// verifyDigests 는 감사 체인의 privacy.log_digest 기록(head_id·head)이 가리키는 행이 그대로 있는지 확인한다.
func (r *Recorder) verifyDigests(ctx context.Context, segs []segment) (digestResult, error) {
	res := digestResult{ok: true}
	rows, err := r.db.QueryContext(ctx, `SELECT diff FROM audit_logs WHERE action = 'privacy.log_digest' ORDER BY seq`)
	if err != nil {
		return res, err
	}
	var diffs []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			return res, err
		}
		diffs = append(diffs, d)
	}
	rows.Close()
	for _, d := range diffs {
		var items []struct{ Key, New string }
		if err := json.Unmarshal([]byte(d), &items); err != nil {
			continue
		}
		var headID int64
		var head, day string
		for _, it := range items {
			switch it.Key {
			case "head_id":
				headID, _ = strconv.ParseInt(it.New, 10, 64)
			case "chain_head":
				head = it.New
			case "day":
				day = it.New
			}
		}
		if headID == 0 || head == "" {
			continue
		}
		res.n++
		var got string
		err := r.db.QueryRowContext(ctx, `SELECT hash FROM privacy_access_log WHERE id = ?`, headID).Scan(&got)
		switch {
		case err == nil:
			if got != head {
				return digestResult{n: res.n, why: fmt.Sprintf("%s 요약의 머리 행(%d)의 해시가 다릅니다", day, headID)}, nil
			}
		case errors.Is(err, sql.ErrNoRows):
			covered := false
			for _, s := range segs {
				if headID >= s.firstID && headID <= s.lastID {
					covered = true
					if headID == s.lastID && s.head != head {
						return digestResult{n: res.n, why: fmt.Sprintf("%s 요약이 파기 구간 요약과 다릅니다", day)}, nil
					}
				}
			}
			if !covered {
				return digestResult{n: res.n, why: fmt.Sprintf("%s 요약이 가리키는 행(%d)이 없습니다(끝부분 삭제 의심)", day, headID)}, nil
			}
		default:
			return res, err
		}
	}
	return res, nil
}
