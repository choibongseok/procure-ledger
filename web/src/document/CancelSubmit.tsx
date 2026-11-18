// 상신 취소(D9): 아무도 처리하지 않은 문서를 기안자가 임시저장으로 되돌린다. 구매 품의서와 양식 문서가 같이 쓴다.
import { useState } from "react";
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import { errorText } from "../purchase/useLoad";

export function CancelSubmit({ path, onDone }: { path: string; onDone: () => void }) {
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  async function run() {
    setErr("");
    setBusy(true);
    try {
      await api.post(path, { reason });
      setOpen(false);
      setReason("");
      onDone();
    } catch (e) {
      setErr(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  if (!open) {
    return (
      <p>
        <button type="button" className="btn-secondary" onClick={() => setOpen(true)}>
          상신 취소
        </button>{" "}
        <span className="hint">아직 아무도 결재하지 않았다면 임시저장으로 되돌려 고칠 수 있습니다.</span>
      </p>
    );
  }
  return (
    <div className="form-row">
      <label>
        취소 사유(필수)
        <input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} />
      </label>
      <button type="button" className="btn-danger" disabled={busy || !reason.trim()} onClick={() => void run()}>
        상신 취소하기
      </button>
      <button type="button" className="btn-secondary" onClick={() => setOpen(false)}>
        닫기
      </button>
      {err && <Notice tone="error">{err}</Notice>}
    </div>
  );
}
