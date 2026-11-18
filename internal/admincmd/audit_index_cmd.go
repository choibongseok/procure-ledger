package admincmd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/choibongseok/procure-ledger/internal/audit"
)

func init() {
	commands = append(commands,
		command{"audit-entity-index", "audit-entity-index", "감사 로그의 법인 색인(entity_id, seq)을 만든다(B44). PG 는 쓰기를 막지 않는 방식(CONCURRENTLY)이라 서버를 멈출 필요 없다. 이미 있으면 아무 일도 하지 않는다.", runAuditEntityIndex},
	)
}

func runAuditEntityIndex(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 0 {
		return errors.New("사용법: audit-entity-index")
	}
	d, closeDeps, err := Build(ctx)
	if err != nil {
		return err
	}
	defer closeDeps()
	if err := audit.EnsureEntityIndex(ctx, d.DB); err != nil {
		return fmt.Errorf("감사 로그 법인 색인을 만들지 못했습니다: %w", err)
	}
	fmt.Fprintln(out, "감사 로그 법인 색인: 준비됨")
	return nil
}
