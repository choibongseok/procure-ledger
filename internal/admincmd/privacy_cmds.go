package admincmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/choibongseok/procure-ledger/internal/privacy"
)

func init() {
	commands = append(commands,
		command{"privacy-verify", "privacy-verify", "개인정보 조회 기록의 해시 체인과 감사 체인의 하루 요약을 검증한다(읽기 전용, 서버를 멈출 필요 없음). 어긋나면 종료 코드 1.", runPrivacyVerify},
		command{"privacy-purge", "privacy-purge [--dry-run]", "보관 기간이 지난 달의 조회 기록을 파기한다(요약을 남기고 감사 privacy.log_purge). --dry-run 은 대상만 보여 준다.", runPrivacyPurge},
	)
}

func privacyService(ctx context.Context) (*privacy.Recorder, func(), error) {
	d, closeDeps, err := Build(ctx)
	if err != nil {
		return nil, nil, err
	}
	rec, ok := d.Privacy.(*privacy.Recorder)
	if !ok {
		closeDeps()
		return nil, nil, errors.New("조회 기록 서비스를 만들지 못했습니다")
	}
	return rec, closeDeps, nil
}

func runPrivacyVerify(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 0 {
		return errors.New("사용법: privacy-verify")
	}
	rec, closeDeps, err := privacyService(ctx)
	if err != nil {
		return err
	}
	defer closeDeps()
	res, err := rec.Verify(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "조회 기록 %d건 검증", res.Checked)
	if res.Purged {
		fmt.Fprint(out, "(파기한 달이 있어 남은 구간만)")
	}
	fmt.Fprintln(out)
	if !res.ChainOK {
		fmt.Fprintf(out, "실패: 기록 %d번: %s\n", res.BrokenID, res.Why)
		return errors.New("조회 기록 체인이 맞지 않습니다")
	}
	fmt.Fprintf(out, "체인: 통과, 감사 체인의 하루 요약 %d건 대조: ", res.DigestsChecked)
	if !res.DigestOK {
		fmt.Fprintf(out, "실패(%s)\n", res.DigestWhy)
		return errors.New("조회 기록이 감사 체인의 하루 요약과 맞지 않습니다")
	}
	fmt.Fprintln(out, "통과")
	return nil
}

func runPrivacyPurge(ctx context.Context, args []string, out io.Writer) error {
	dry := false
	for _, a := range args {
		if a != "--dry-run" {
			return errors.New("사용법: privacy-purge [--dry-run]")
		}
		dry = true
	}
	rec, closeDeps, err := privacyService(ctx)
	if err != nil {
		return err
	}
	defer closeDeps()
	days, err := rec.RetentionDays(ctx)
	if err != nil {
		return err
	}
	items, err := rec.Purge(ctx, time.Now(), dry)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Fprintf(out, "파기할 달이 없습니다(보관 기간 %d일)\n", days)
		return nil
	}
	verb := "파기했습니다"
	if dry {
		verb = "파기 대상입니다"
	}
	for _, it := range items {
		fmt.Fprintf(out, "%s: %d건 %s\n", it.Month, it.Rows, verb)
	}
	return nil
}
