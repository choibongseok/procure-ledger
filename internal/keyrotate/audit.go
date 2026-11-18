package keyrotate

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/choibongseok/procure-ledger/internal/audit"
	"github.com/choibongseok/procure-ledger/internal/backup"
	caudit "github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// RecordAudit 는 키 교체 결과를 감사 system.key_rotated 로 남긴다(키 이름과 건수만. 키 값·암호문·경로는 없다).
func RecordAudit(ctx context.Context, db *sql.DB, keyName string, total int, ok bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, detail := caudit.ResultSuccess, fmt.Sprintf("키 %s 교체, 다시 봉한 값 %d건", keyName, total)
	if !ok {
		res, detail = caudit.ResultError, fmt.Sprintf("키 %s 교체 실패(아무것도 바뀌지 않았거나 점검에 실패했습니다)", keyName)
	}
	if err := audit.New(db).Record(ctx, tx, caudit.Entry{Actor: backup.Actor, Action: "system.key_rotated", TargetType: caudit.TargetSystem, TargetID: keyName, Result: res,
		Detail: detail, Diff: []caudit.DiffItem{{Key: "keyName", New: keyName}, {Key: "resealed", New: fmt.Sprint(total)}}}); err != nil {
		return err
	}
	return tx.Commit()
}
