package apiv1

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
)

// 쓰기 요청 본문의 상한(가져오기 5,000행 기준, 명세 10.2).
const maxWriteBody = 5 << 20

// writeOp 는 쓰기 라우트 하나의 본문 처리다: 본문을 읽어 서비스를 부르고 응답 데이터와 전송 정보를 돌려준다.
type writeOp func(ctx context.Context, c Caller, r *http.Request, body []byte, key, hash string) (any, WriteMeta, error)

// writeRoute 는 범위 검사·본문 읽기(상한)·Idempotency-Key 형식 검사·오류 매핑을 한 곳에서 한다.
func (s *Service) writeRoute(scope string, op writeOp) http.HandlerFunc {
	return scoped(scope, func(w http.ResponseWriter, r *http.Request, rid string, c Caller) {
		if s.write == nil {
			writeError(w, rid, http.StatusServiceUnavailable, "unavailable", "쓰기 API 가 준비되지 않았습니다.")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWriteBody))
		if err != nil {
			writeError(w, rid, http.StatusRequestEntityTooLarge, "invalid_argument", "요청 본문이 너무 큽니다(5MB 이하).")
			return
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key != "" && !validIdemKey(key) {
			writeError(w, rid, http.StatusBadRequest, "invalid_argument", "Idempotency-Key 는 1~64자의 영문·숫자·_ . : - 여야 합니다.")
			return
		}
		sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), body...))
		data, meta, err := op(r.Context(), c, r, body, key, hex.EncodeToString(sum[:]))
		if err != nil {
			failWrite(w, rid, err)
			return
		}
		if meta.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		if meta.Status == 0 {
			meta.Status = http.StatusOK
		}
		writeJSON(w, meta.Status, map[string]any{"data": data})
	})
}

func validIdemKey(k string) bool {
	if len(k) < 1 || len(k) > 64 {
		return false
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == ':' || r == '-') {
			return false
		}
	}
	return true
}

// decodeBody 는 본문 JSON 을 알 수 없는 필드 없이 읽는다.
func decodeBody(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.Join(contract.ErrValidation, errors.New("요청 본문이 올바른 JSON 이 아니거나 알 수 없는 필드가 있습니다."))
	}
	if dec.More() {
		return errors.Join(contract.ErrValidation, errors.New("요청 본문 뒤에 다른 내용이 있습니다."))
	}
	return nil
}

func (s *Service) importPreviewHandler(scope, target string) http.HandlerFunc {
	return s.writeRoute(scope, func(ctx context.Context, c Caller, _ *http.Request, body []byte, _, _ string) (any, WriteMeta, error) {
		var req ImportRequest
		if err := decodeBody(body, &req); err != nil {
			return nil, WriteMeta{}, err
		}
		out, meta, err := s.write.PreviewImport(ctx, c, target, req.Rows)
		return out, meta, err
	})
}

func (s *Service) importApplyHandler(scope, target string) http.HandlerFunc {
	return s.writeRoute(scope, func(ctx context.Context, c Caller, r *http.Request, body []byte, key, hash string) (any, WriteMeta, error) {
		var req ImportApplyRequest
		if err := decodeBody(body, &req); err != nil {
			return nil, WriteMeta{}, err
		}
		out, meta, err := s.write.ApplyImport(ctx, c, key, hash, target, r.PathValue("batch"), req)
		return out, meta, err
	})
}

func (s *Service) journalDraftHandler(scope string) http.HandlerFunc {
	return s.writeRoute(scope, func(ctx context.Context, c Caller, _ *http.Request, body []byte, key, hash string) (any, WriteMeta, error) {
		var req JournalDraftRequest
		if err := decodeBody(body, &req); err != nil {
			return nil, WriteMeta{}, err
		}
		out, meta, err := s.write.DraftJournal(ctx, c, key, hash, req)
		return out, meta, err
	})
}

// failWrite 는 쓰기 오류를 고정 코드로 옮긴다. 검증·충돌의 한국어 사유는 호출자가 고칠 수 있게 그대로 낸다(서비스가 값·내부 식별자를 담지 않는 문장).
func failWrite(w http.ResponseWriter, rid string, err error) {
	var rl ErrRateLimited
	switch {
	case errors.As(err, &rl):
		if rl.RetryAfterSeconds > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(rl.RetryAfterSeconds))
		}
		writeError(w, rid, http.StatusTooManyRequests, "rate_limited", "쓰기 호출 한도를 넘었습니다. 잠시 뒤 다시 시도하세요.")
	case errors.Is(err, contract.ErrForbidden):
		writeError(w, rid, http.StatusForbidden, "forbidden_scope", "이 토큰의 범위로는 할 수 없습니다.")
	case errors.Is(err, contract.ErrNotFound):
		writeError(w, rid, http.StatusNotFound, "not_found", "대상을 찾을 수 없습니다.")
	case errors.Is(err, contract.ErrInvalidState):
		writeError(w, rid, http.StatusConflict, "conflict", reasonOf(err, contract.ErrInvalidState, "현재 상태에서는 할 수 없습니다."))
	case errors.Is(err, contract.ErrValidation):
		writeError(w, rid, http.StatusBadRequest, "invalid_argument", reasonOf(err, contract.ErrValidation, "요청 값이 올바르지 않습니다."))
	default:
		writeError(w, rid, http.StatusInternalServerError, "unavailable", "서버 오류가 발생했습니다.")
	}
}

// reasonOf 는 "센티널: 사유" 형태의 오류에서 사유 문장만 꺼낸다(없으면 기본 문구).
func reasonOf(err, sentinel error, def string) string {
	msg := err.Error()
	if i := strings.Index(msg, sentinel.Error()+": "); i >= 0 {
		msg = msg[i+len(sentinel.Error())+2:]
	} else if joined := strings.TrimSpace(strings.ReplaceAll(msg, sentinel.Error(), "")); joined != "" && !strings.Contains(joined, "\n") {
		msg = strings.TrimSpace(joined)
	} else {
		return def
	}
	if msg == "" || len(msg) > 400 {
		return def
	}
	return msg
}
