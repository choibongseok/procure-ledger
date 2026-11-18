package webhooks

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/dbutil"
	"github.com/choibongseok/procure-ledger/internal/netsafe"
)

// 관리 화면이 쓰는 나머지 동작(B42 2단계 platform 몫): 구독 수정, 구독별 실패 일괄 재전송, 구독 전체 발송 기록 조회, 주소 검사 설정(허용 호스트·추가 포트).

// UpdateInput 은 구독 수정 입력이다. nil 인 칸은 바꾸지 않는다. URL 을 주면 새 주소로 바꾸고 서명 비밀은 그대로 둔다(주소만 옮기는 경우).
type UpdateInput struct {
	Name       *string
	URL        *string
	Events     []string // nil 이면 바꾸지 않는다
	IncludeAPI *bool
}

// Update 는 구독을 수정한다(webhook:manage). 감사 webhook.sub_save 에는 바뀐 항목 이름만 남고 주소는 어떤 형태로도 없다(url_changed 표시만).
func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (View, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return View{}, err
	}
	var name string
	var events []string
	if in.Name != nil {
		if name, err = validName(*in.Name); err != nil {
			return View{}, err
		}
	}
	if in.Events != nil {
		if events, err = normalizeEvents(in.Events); err != nil {
			return View{}, err
		}
	}
	var newURL string
	if in.URL != nil {
		if s.o.Sealer == nil {
			return View{}, stateErr("구독 정보를 안전하게 저장할 키(PL_ACCOUNT_SECRET_KEY)가 서버에 없어 주소를 바꿀 수 없습니다.")
		}
		newURL = strings.TrimSpace(*in.URL)
		u, err := netsafe.ValidateURL(newURL, s.urlOptions(ctx, s.o.DB))
		if err != nil {
			return View{}, valErr("%s", err.Error())
		}
		if !(s.o.AllowLocal && isLocalHost(u.Hostname())) {
			if err := s.resolveCheck(ctx, u.Hostname()); err != nil {
				return View{}, err
			}
		}
	}
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		sub, err := s.loadSub(ctx, tx, id)
		if err != nil {
			return err
		}
		var diff []audit.DiffItem
		set, args := []string{}, []any{}
		if in.Name != nil && name != sub.name {
			set, args = append(set, "name = ?"), append(args, name)
			diff = append(diff, audit.DiffItem{Key: "name", Old: sub.name, New: name})
		}
		if in.Events != nil {
			evJSON, _ := json.Marshal(events)
			set, args = append(set, "events_json = ?"), append(args, string(evJSON))
			diff = append(diff, audit.DiffItem{Key: "events", New: strconv.Itoa(len(events)) + "종"})
		}
		if in.IncludeAPI != nil && *in.IncludeAPI != sub.includeAPI {
			set, args = append(set, "include_api = ?"), append(args, boolInt(*in.IncludeAPI))
			diff = append(diff, audit.DiffItem{Key: "include_api", Old: yesNo(sub.includeAPI), New: yesNo(*in.IncludeAPI)})
		}
		if in.URL != nil {
			b, code := s.open(sub.sealed)
			if code != "" {
				return stateErr("%s", ReasonText(code))
			}
			b.URL = newURL
			sealed, err := s.seal(b)
			if err != nil {
				return err
			}
			set, args = append(set, "secret_sealed = ?", "hint = ?"), append(args, sealed, hint(newURL))
			diff = append(diff, audit.DiffItem{Key: "url_changed", New: "예"})
		}
		if len(set) == 0 {
			return nil
		}
		set, args = append(set, "updated_at = ?"), append(args, ts(s.now()))
		if _, err := tx.ExecContext(ctx, `UPDATE webhook_subscriptions SET `+strings.Join(set, ", ")+` WHERE id = ?`, append(args, id)...); err != nil {
			if dbutil.IsUnique(err) {
				return valErr("같은 이름의 구독이 이미 있습니다.")
			}
			return err
		}
		return s.record(ctx, tx, p, "webhook.sub_save", id, append([]audit.DiffItem{{Key: "name", New: sub.name}}, diff...)...)
	})
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, id)
}

// ResendFailed 는 구독의 실패(포기)한 건을 모두 대기로 되돌린다(구독 일괄 재전송). 건마다 5번까지라 그 한도를 넘은 건은 건너뛴다. 되돌린 건수를 돌려준다. 감사 webhook.delivery_resend.
func (s *Service) ResendFailed(ctx context.Context, subID int64) (int, error) {
	p, err := s.authorize(ctx)
	if err != nil {
		return 0, err
	}
	var n int
	err = s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		sub, err := s.loadSub(ctx, tx, subID)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE webhook_deliveries SET status = 'queued', attempts = 0, resends = resends + 1, next_try_at = ?, claimed_at = '', last_code = ''
			WHERE subscription_id = ? AND status = 'failed' AND resends < ?`, ts(s.now()), subID, maxResends)
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		n = int(c)
		if n == 0 {
			return nil
		}
		return s.record(ctx, tx, p, "webhook.delivery_resend", subID, audit.DiffItem{Key: "name", New: sub.name}, audit.DiffItem{Key: "rows", New: strconv.Itoa(n)})
	})
	return n, err
}

// DeliveryFilter 는 전체 발송 기록의 조건이다.
type DeliveryFilter struct {
	SubID     int64
	Status    string
	EventType string // 자원.동작
	From, To  time.Time
	Limit     int
}

// DeliveryRow 는 전체 발송 기록 한 줄이다(구독 이름 포함, 본문·주소·응답 없음).
type DeliveryRow struct {
	DeliveryView
	SubID   int64  `json:"subscriptionId"`
	SubName string `json:"subscriptionName"`
}

// AllDeliveries 는 모든 구독의 발송 기록이다(webhook:manage 또는 audit:read). 기간·구독·상태·이벤트 종류로 거를 수 있다.
func (s *Service) AllDeliveries(ctx context.Context, f DeliveryFilter) ([]DeliveryRow, error) {
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		return nil, contract.ErrUnauthenticated
	}
	if s.o.ManagePerm == "" || !(p.Can(s.o.ManagePerm) || p.Can(contract.PermAuditRead)) {
		return nil, contract.ErrForbidden
	}
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 100
	}
	q := `SELECT d.id, d.subscription_id, COALESCE(s.name, ''), d.event_seq, d.event_type, d.object_id, d.status, d.attempts, d.last_code, d.next_try_at, d.sent_at, d.created_at
		FROM webhook_deliveries d LEFT JOIN webhook_subscriptions s ON s.id = d.subscription_id WHERE 1 = 1`
	var args []any
	if f.SubID > 0 {
		q, args = q+` AND d.subscription_id = ?`, append(args, f.SubID)
	}
	if f.Status != "" {
		q, args = q+` AND d.status = ?`, append(args, f.Status)
	}
	if f.EventType != "" {
		q, args = q+` AND d.event_type = ?`, append(args, f.EventType)
	}
	if !f.From.IsZero() {
		q, args = q+` AND d.created_at >= ?`, append(args, ts(f.From))
	}
	if !f.To.IsZero() {
		q, args = q+` AND d.created_at < ?`, append(args, ts(f.To))
	}
	rows, err := s.o.DB.QueryContext(ctx, q+` ORDER BY d.id DESC LIMIT ?`, append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeliveryRow{}
	for rows.Next() {
		var r DeliveryRow
		var code string
		if err := rows.Scan(&r.ID, &r.SubID, &r.SubName, &r.EventSeq, &r.EventType, &r.ObjectID, &r.Status, &r.Attempts, &code, &r.NextTryAt, &r.SentAt, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.State, r.Reason, r.Title = statusText[r.Status], ReasonText(code), eventTitle(r.EventType)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Hosts 는 주소 검사 설정이다(허용 호스트 목록은 비면 공인 IP 인 모든 호스트, 추가 포트는 443 밖 허용 포트).
type Hosts struct {
	AllowHosts []string `json:"allowHosts"`
	ExtraPorts []string `json:"extraPorts"`
}

// GetHosts 는 주소 검사 설정을 읽는다(webhook:manage).
func (s *Service) GetHosts(ctx context.Context) (Hosts, error) {
	if _, err := s.authorize(ctx); err != nil {
		return Hosts{}, err
	}
	return Hosts{AllowHosts: nonNil(splitList(s.setting(ctx, s.o.DB, "webhook.allow_hosts"))), ExtraPorts: nonNil(splitList(s.setting(ctx, s.o.DB, "webhook.extra_ports")))}, nil
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// SetHosts 는 주소 검사 설정을 바꾼다(webhook:manage). 목록 개수·형식을 검사하고 감사 webhook.hosts_set 에는 건수만 남긴다. 이미 등록된 구독은 발송 직전 검사에 새 설정이 적용된다.
func (s *Service) SetHosts(ctx context.Context, h Hosts) error {
	p, err := s.authorize(ctx)
	if err != nil {
		return err
	}
	clean := func(in []string, what string, port bool) ([]string, error) {
		var out []string
		for _, v := range in {
			v = strings.ToLower(strings.TrimSpace(v))
			if v == "" {
				continue
			}
			if port {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 || n > 65535 {
					return nil, valErr("%s 형식이 올바르지 않습니다: %s", what, v)
				}
			} else if strings.ContainsAny(v, " /:@") || len(v) > 253 {
				return nil, valErr("%s 형식이 올바르지 않습니다: %s", what, v)
			}
			out = append(out, v)
		}
		if len(out) > 50 {
			return nil, valErr("%s 는 50개까지입니다.", what)
		}
		return out, nil
	}
	hosts, err := clean(h.AllowHosts, "허용 호스트", false)
	if err != nil {
		return err
	}
	ports, err := clean(h.ExtraPorts, "추가 포트", true)
	if err != nil {
		return err
	}
	return s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		for _, kv := range [][2]string{{"webhook.allow_hosts", strings.Join(hosts, ",")}, {"webhook.extra_ports", strings.Join(ports, ",")}} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_by, updated_at) VALUES (?,?,?,?)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by, updated_at = excluded.updated_at`, kv[0], kv[1], p.UserID, s.now().Format(time.RFC3339Nano)); err != nil {
				return err
			}
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "webhook.hosts_set", TargetType: audit.TargetWebhook, TargetID: "hosts", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "allow_hosts", New: strconv.Itoa(len(hosts)) + "개"}, {Key: "extra_ports", New: strconv.Itoa(len(ports)) + "개"}}})
	})
}
