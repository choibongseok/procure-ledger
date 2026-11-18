package privreport

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
	"github.com/choibongseok/procure-ledger/internal/repository"
)

const datasetLabel = "월별 점검 보고서"

// Register 는 보고서 내려받기 라우트를 등록한다. 선언 권한은 sod:read 이고 핸들러가 privacy:log_read 를 더 요구한다(두 영역을 한 파일에서 보므로 둘 다 필요).
func (s *Service) Register(reg contract.Registrar) {
	reg.HandleFunc("GET", "/api/audit/privacy/report", contract.PermSoDRead, s.handle)
}

func (s *Service) denied(ctx context.Context, p contract.Principal, month, why string) {
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		return s.audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "export.run", TargetType: audit.TargetExport, TargetID: "privacy_report", Result: audit.ResultDenied,
			Diff:   []audit.DiffItem{{Key: "dataset", New: datasetLabel}, {Key: "format", New: "xlsx"}, {Key: "filters", New: "대상 월 " + month}},
			Detail: why})
	})
	if err != nil {
		log.Printf("privreport: 거부 감사 기록 실패: %v", err)
	}
}

func (s *Service) handle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := contract.PrincipalFrom(ctx)
	if !ok {
		http.Error(w, contract.ErrUnauthenticated.Error(), http.StatusUnauthorized)
		return
	}
	month := strings.TrimSpace(r.URL.Query().Get("month"))
	if month == "" {
		month = LastMonth(s.now())
	}
	if f := r.URL.Query().Get("format"); f != "" && f != "xlsx" {
		http.Error(w, "보고서는 엑셀(XLSX) 로만 내려받을 수 있습니다.", http.StatusBadRequest)
		return
	}
	if _, _, err := monthRange(month); err != nil {
		http.Error(w, "월 형식은 YYYY-MM 이어야 합니다.", http.StatusBadRequest)
		return
	}
	if !p.Can(contract.PermPrivacyLogRead) { // 위반·예외와 조회 기록을 한 파일에서 보므로 두 권한이 모두 필요하다
		s.denied(ctx, p, month, "조회 기록 열람 권한 없음")
		http.Error(w, "이 보고서를 내려받을 권한이 없습니다. 권한 분리 조회와 개인정보 조회 기록 열람 권한이 모두 필요합니다.", http.StatusForbidden)
		return
	}
	release, err := s.limiter.Acquire(p.UserID)
	if err != nil {
		s.denied(ctx, p, month, "속도 제한")
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	defer release()
	res, err := s.Build(ctx, p, month)
	if err != nil {
		if errors.Is(err, contract.ErrValidation) {
			http.Error(w, strings.TrimPrefix(err.Error(), contract.ErrValidation.Error()+": "), http.StatusBadRequest)
			return
		}
		log.Printf("privreport: 보고서 생성 실패: %v", err)
		http.Error(w, "보고서를 만들지 못했습니다. 잠시 뒤 다시 시도하고, 계속되면 관리자에게 알려 주세요.", http.StatusInternalServerError)
		return
	}
	// 파일을 보내기 전에 감사 두 건(내보내기 실행, 조회 기록 읽기)을 한 트랜잭션에 남긴다. 실패하면 파일을 보내지 않는다.
	err = s.txs.WithTx(ctx, func(tx contract.Tx) error {
		if err := s.audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "export.run", TargetType: audit.TargetExport, TargetID: "privacy_report", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "dataset", New: datasetLabel}, {Key: "format", New: "xlsx"}, {Key: "exported_rows", New: strconv.Itoa(res.Rows)}, {Key: "filters", New: "대상 월 " + month}, {Key: "raw_sensitive", New: "아니오"}}}); err != nil {
			return err
		}
		return s.audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "privacy.log_read", TargetType: audit.TargetPrivacyLog, TargetID: "report", Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "filters", New: "월별 보고서 " + month}, {Key: "rows", New: strconv.Itoa(res.Rows)}}})
	})
	if err != nil {
		log.Printf("privreport: 감사 기록 실패: %v", err)
		http.Error(w, "파일을 만들지 못했습니다. 잠시 뒤 다시 시도하세요.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="report.xlsx"; filename*=UTF-8''%s`, url.PathEscape(res.FileName)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(res.Body)))
	_, _ = w.Write(res.Body)
}

// ---- 월 알림 ----

func ymd(month string) int64 {
	n, _ := strconv.ParseInt(strings.ReplaceAll(month, "-", ""), 10, 64)
	return n
}

// firstBusinessDay 는 그 달의 첫 영업일("YYYY-MM-DD", KST)이다.
func (s *Service) firstBusinessDay(ctx context.Context, tx contract.Tx, month string) (string, error) {
	d, err := time.ParseInLocation("2006-01", month, kst)
	if err != nil {
		return "", err
	}
	for i := 0; i < 31; i++ {
		day := d.AddDate(0, 0, i).Format("2006-01-02")
		ok := d.AddDate(0, 0, i).Weekday() != time.Saturday && d.AddDate(0, 0, i).Weekday() != time.Sunday
		if s.businessDay != nil {
			var err error
			if ok, err = s.businessDay(ctx, tx, day); err != nil {
				return "", err
			}
		}
		if ok {
			return day, nil
		}
	}
	return d.Format("2006-01-02"), nil
}

// Tick 은 이번 달 첫 영업일이 지났고 지난달 보고서 알림을 아직 보내지 않았으면 알림을 보낸다. 받는 사람은 두 권한(sod:read·privacy:log_read)을 모두 가진 사용자다.
// 같은 달 보고서는 사람마다 한 번만 보낸다. 건수를 돌려준다.
func (s *Service) Tick(ctx context.Context) (int, error) {
	now := s.now().In(kst)
	today := now.Format("2006-01-02")
	report := LastMonth(now)
	sent := 0
	err := s.txs.WithTx(ctx, func(tx contract.Tx) error {
		first, err := s.firstBusinessDay(ctx, tx, now.Format("2006-01"))
		if err != nil {
			return err
		}
		if today < first {
			return nil
		}
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT a.user_id FROM user_effective_permissions a JOIN user_effective_permissions b ON b.user_id = a.user_id AND b.permission = ?
			JOIN users u ON u.id = a.user_id WHERE a.permission = ? AND u.active = 1 ORDER BY a.user_id`, string(contract.PermPrivacyLogRead), string(contract.PermSoDRead))
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		at := s.now()
		for _, uid := range ids {
			var c int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications WHERE user_id = ? AND kind = 'privacy_monthly_report' AND ref_type = 'privacy_report' AND ref_id = ?`, uid, ymd(report)).Scan(&c); err != nil {
				return err
			}
			if c > 0 {
				continue
			}
			nid, err := repository.SQLNotifications{}.Insert(ctx, tx, repository.NotificationInsert{UserID: uid, Kind: "privacy_monthly_report", Title: "월별 점검 보고서를 내려받을 수 있습니다",
				Body: report + " 권한 분리 위반과 개인정보 조회 기록 점검 보고서입니다. 권한 분리 위반 화면에서 내려받으세요.", Link: "/admin/sod/violations", RefType: "privacy_report", RefID: ymd(report), Now: at})
			if err != nil {
				return err
			}
			sent++
			if s.deliver != nil {
				if derr := s.deliver(ctx, tx, nid, uid, "privacy_monthly_report", at); derr != nil {
					log.Printf("privreport: 발송 건 생성 실패(앱 안 알림은 만들어짐, 알림 %d): %v", nid, derr)
				}
			}
		}
		return nil
	})
	return sent, err
}

// Run 은 서버 주기 작업이다(한 시간마다 Tick). 복구 리허설 모드에서는 서버가 돌리지 않는다.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			log.Printf("privreport: 월 알림 실패: %v", err)
		} else if n > 0 {
			log.Printf("privreport: 월별 점검 보고서 알림 %d건", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
