package admincmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

func init() {
	commands = append(commands,
		command{"knowledge-reindex", "knowledge-reindex [--entity <법인 코드>] [--kind approval_doc|upload] [--dry-run]",
			"사내 지식 색인(B47)을 다시 만든다. 내용이 같은 문서는 건너뛰므로 몇 번을 돌려도 같다. 서버가 떠 있어도 안전하다(문서마다 트랜잭션). --dry-run 은 볼 문서 수만 센다(감사 knowledge.reindex).",
			runKnowledgeReindex},
	)
}

type knowledgeReindexer interface {
	ReindexSystem(ctx context.Context, actor contract.Principal, entityCode, kind string) (done, skipped int, err error)
	CountTargets(ctx context.Context, entityCode string) (int, error)
}

func runKnowledgeReindex(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("knowledge-reindex", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	entity := fs.String("entity", "", "법인 코드(비우면 전체)")
	kind := fs.String("kind", "", "지식 문서 종류(approval_doc 또는 upload, 비우면 둘 다)")
	dry := fs.Bool("dry-run", false, "볼 문서 수만 센다")
	if err := fs.Parse(args); err != nil {
		return errors.New("사용법: knowledge-reindex [--entity <법인 코드>] [--kind approval_doc|upload] [--dry-run]")
	}
	if fs.NArg() != 0 {
		return errors.New("사용법: knowledge-reindex [--entity <법인 코드>] [--kind approval_doc|upload] [--dry-run]")
	}
	d, closeDeps, err := Build(ctx)
	if err != nil {
		return err
	}
	defer closeDeps()
	r, ok := d.KnowledgeAdmin.(knowledgeReindexer)
	if !ok {
		return errors.New("사내 지식 색인이 설정되지 않았습니다")
	}
	code := strings.ToUpper(strings.TrimSpace(*entity))
	if *dry {
		n, err := r.CountTargets(ctx, code)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "다시 색인 대상: %d건(실제로는 바꾸지 않았습니다)\n", n)
		return nil
	}
	done, skipped, err := r.ReindexSystem(ctx, Actor, code, strings.TrimSpace(*kind))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "다시 색인: 처리 %d건, 내용이 같아 건너뜀 %d건\n", done, skipped)
	return nil
}
