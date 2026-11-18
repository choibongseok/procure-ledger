package admincmd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/choibongseok/procure-ledger/internal/demo"
)

func init() {
	commands = append(commands, command{"demo-seed", "demo-seed",
		"설치 때 데모 데이터를 골랐는데 넣지 못한 환경에서 데모 샘플 세트를 다시 넣고, 첫 관리자를 데모 조직에 배치한다(배치는 여러 번 해도 한 번만 된다).", runDemoSeed})
}

// runDemoSeed 는 설치 마법사에서 데모 데이터를 고르고(app_settings setup.demo_mode=1) 설치는 끝났지만 데모 세트를 넣지 못한 환경의 재시도다.
// 데모 표시가 없는 DB(운영)에서는 거부한다. 일부만 들어간 뒤라면 데모 세트 자체가 한 번만 되도록 막아 두었으므로(demo.Seed) 새 DB 로 다시 설치해야 한다.
func runDemoSeed(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 0 {
		return errors.New("사용법: demo-seed")
	}
	d, closeDeps, err := Build(ctx)
	if err != nil {
		return err
	}
	defer closeDeps()
	var v string
	if err := d.DB.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'setup.demo_mode'`).Scan(&v); err != nil || v != "1" {
		return errors.New("이 환경은 설치 때 데모 데이터를 고르지 않았습니다(운영 데이터에는 데모를 넣지 않습니다)")
	}
	var seeded string
	_ = d.DB.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key = 'demo_seed'`).Scan(&seeded)
	switch seeded {
	case "":
		if err := demo.Seed(ctx, d); err != nil {
			return fmt.Errorf("데모 데이터를 넣지 못했습니다: %w. 일부만 들어갔다면 새 DB 로 다시 설치하세요", err)
		}
		fmt.Fprintln(out, "데모 데이터를 넣었습니다")
	case "done":
		fmt.Fprintln(out, "데모 데이터는 이미 들어 있습니다")
	default:
		return errors.New("데모 데이터가 일부만 들어간 상태입니다. 새 DB 로 다시 설치하세요")
	}
	// 첫 관리자(설치를 끝낸 사람)를 데모 조직에 배치한다. 이미 배치됐으면 아무것도 하지 않는다.
	var adminID int64
	if err := d.DB.QueryRowContext(ctx, `SELECT completed_by FROM setup_state WHERE id = 1`).Scan(&adminID); err != nil || adminID == 0 {
		return errors.New("첫 관리자를 찾을 수 없어 조직에 배치하지 못했습니다")
	}
	placed, err := demo.PlaceAdmin(ctx, d, adminID)
	if err != nil {
		return fmt.Errorf("첫 관리자를 데모 조직에 배치하지 못했습니다: %w", err)
	}
	if placed {
		fmt.Fprintln(out, "첫 관리자를 데모 조직에 배치했습니다")
	} else {
		fmt.Fprintln(out, "첫 관리자는 이미 조직에 배치되어 있습니다")
	}
	return nil
}
