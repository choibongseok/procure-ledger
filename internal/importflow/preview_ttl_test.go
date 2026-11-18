package importflow

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/choibongseok/procure-ledger/internal/contract/domain"
)

// 명세 6절: 미리보기 유효 시간은 app_settings 의 import.preview_ttl_hours 를 매번 읽는다(없거나 잘못되면 24시간).
func TestPreviewTTLFollowsSetting(t *testing.T) {
	e := newEnv(t)
	n := 0
	expired := func(setting string, after time.Duration) bool {
		t.Helper()
		n++
		file := makeFile(t, [][]string{{fmt.Sprintf("WH-%02d", n), "창고", "이천"}}) // 반영된 파일과 겹치지 않게 매번 다른 창고
		if setting != "" {
			if _, err := e.db.Exec(`INSERT INTO app_settings(key, value) VALUES ('import.preview_ttl_hours', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, setting); err != nil {
				t.Fatal(err)
			}
		}
		start := *e.clock
		defer func() { *e.clock = start }()
		p, err := e.svc.Preview(as(e.admin), domain.ImportWarehouse, "", file)
		if err != nil {
			t.Fatal(err)
		}
		*e.clock = start.Add(after)
		_, err = e.svc.Apply(as(e.admin), domain.ImportWarehouse, p.BatchNo, p.Token, file)
		return err != nil && strings.Contains(err.Error(), "만료")
	}
	if !expired("1", 2*time.Hour) {
		t.Error("1시간 설정인데 2시간 뒤에도 유효함")
	}
	if expired("48", 30*time.Hour) {
		t.Error("48시간 설정인데 30시간 뒤에 만료됨")
	}
	if !expired("abc", 25*time.Hour) {
		t.Error("잘못된 값이면 24시간이어야 함")
	}
	if !expired("0", 25*time.Hour) {
		t.Error("0 이면 24시간이어야 함")
	}
}
