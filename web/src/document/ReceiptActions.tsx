// 수신 건 처리 버튼(접수·담당자 지정·처리 완료·반송). 문서함 수신함과 문서 상세가 같이 쓴다.
import { useState } from "react";
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import { errorText } from "../purchase/useLoad";
import type { DocReceipt } from "./types";

type Mode = "" | "assign" | "complete" | "return";
interface Member {
  id: number;
  name: string;
  rank: string;
}

export function ReceiptActions({ receiptId, status, onChanged }: { receiptId: number; status: string; onChanged: () => void }) {
  const [mode, setMode] = useState<Mode>("");
  const [text, setText] = useState("");
  const [members, setMembers] = useState<Member[]>([]);
  const [who, setWho] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  if (status === "done" || status === "returned") return null;

  async function run(path: string, body?: unknown) {
    setErr("");
    setBusy(true);
    try {
      await api.post(`/api/receipts/${receiptId}/${path}`, body ?? {});
      setMode("");
      setText("");
      onChanged();
    } catch (e) {
      setErr(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  async function openAssign() {
    setErr("");
    setMode("assign");
    try {
      const mine = await api.get<DocReceipt[]>("/api/receipts");
      const unitId = mine.find((r) => r.id === receiptId)?.unitId;
      if (!unitId) return;
      const r = await api.get<{ members: { id: number; name: string; rank: string; active: boolean }[] }>(`/api/org/units/${unitId}/members?descendants=0`);
      setMembers(r.members.filter((m) => m.active));
    } catch (e) {
      setErr(errorText(e));
    }
  }
  return (
    <div>
      {mode === "" && (
        <div className="form-row">
          {status === "pending" && (
            <button type="button" disabled={busy} onClick={() => void run("accept")}>
              접수
            </button>
          )}
          {(status === "received" || status === "assigned") && (
            <>
              <button type="button" className="btn-secondary" onClick={() => void openAssign()}>
                담당자 지정
              </button>
              <button type="button" onClick={() => setMode("complete")}>
                처리 완료
              </button>
            </>
          )}
          <button type="button" className="btn-secondary" onClick={() => setMode("return")}>
            반송
          </button>
        </div>
      )}
      {mode === "assign" && (
        <div className="form-row">
          <label>
            담당자
            <select value={who} onChange={(e) => setWho(e.target.value)}>
              <option value="">선택하세요</option>
              {members.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name} {m.rank}
                </option>
              ))}
            </select>
          </label>
          <button type="button" disabled={busy || !who} onClick={() => void run("assign", { userId: Number(who) })}>
            지정
          </button>
          <button type="button" className="btn-secondary" onClick={() => setMode("")}>
            취소
          </button>
        </div>
      )}
      {(mode === "complete" || mode === "return") && (
        <div className="form-row">
          <label>
            {mode === "return" ? "반송 사유(필수)" : "처리 의견(선택)"}
            <input value={text} maxLength={500} onChange={(e) => setText(e.target.value)} />
          </label>
          <button type="button" disabled={busy || (mode === "return" && !text.trim())} onClick={() => void run(mode, mode === "return" ? { reason: text } : { comment: text })}>
            {mode === "return" ? "반송하기" : "처리 완료하기"}
          </button>
          <button type="button" className="btn-secondary" onClick={() => setMode("")}>
            취소
          </button>
        </div>
      )}
      {err && <Notice tone="error">{err}</Notice>}
    </div>
  );
}
