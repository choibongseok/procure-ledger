// 개인정보 조회 기록 보관 설정(B41 명세 5.8): 보관 기간 한 칸. privacy:log_manage(관리자).
// 보관 기간을 줄이면 바로 지우지 않고 다음 실행(하루 한 번) 때 지우며, 저장 전에 지워질 달을 먼저 보여 준다.
import { useState, type FormEvent } from "react";
import { api } from "../api/client";
import { ConfirmDialog, Notice } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

interface Retention {
  retentionDays: number;
  min: number;
  max: number;
  defaultDays: number;
  pending: { month: string; rows: number }[];
}

export function PrivacySettingsPage() {
  const res = useLoad(() => api.get<Retention>("/api/admin/privacy/retention"), []);
  const [edited, setEdited] = useState<number | null>(null); // 사용자가 고친 값. 없으면 조회한 값을 그대로 보인다(0 이 먼저 보이지 않게)
  const [confirm, setConfirm] = useState(false);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const d = res.data;
  const days = edited ?? d?.retentionDays ?? 0;

  async function save() {
    setConfirm(false);
    setBusy(true);
    setNote(null);
    try {
      await api.put("/api/admin/privacy/retention", { days });
      setNote({ tone: "ok", text: "보관 기간을 저장했습니다. 지워질 기록은 다음 정기 실행 때 지워집니다." });
      await res.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    if (!d) return;
    if (days < d.retentionDays) setConfirm(true);
    else void save();
  }

  if (res.error) return <Notice tone="error">{res.error}</Notice>;
  if (!d) return <p>불러오는 중…</p>;
  return (
    <section>
      <h1>조회 기록 보관 설정</h1>
      <p>개인정보 조회 기록을 몇 일 동안 보관할지 정합니다. 기본은 {d.defaultDays}일(2년)입니다. 법정 보관 기간은 회사 규모·정보 종류에 따라 달라 법무 확인이 필요합니다.</p>
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      <form onSubmit={submit}>
        <div className="field">
          <label htmlFor="privacy-days">보관 기간(일)</label>
          <input id="privacy-days" type="number" min={d.min} max={d.max} value={days} onChange={(e) => setEdited(Number(e.target.value))} required />
          <span className="hint">
            {d.min}일 이상 {d.max}일 이하
          </span>
        </div>
        <button type="submit" disabled={busy || days === d.retentionDays}>
          저장
        </button>
      </form>
      {d.pending.length > 0 && (
        <Notice tone="warn">
          현재 설정으로 다음 정기 실행 때 지워질 기록: {d.pending.map((p) => `${p.month}(${p.rows}건)`).join(", ")}
        </Notice>
      )}
      {confirm && (
        <ConfirmDialog title="보관 기간 줄이기" confirmLabel="저장" onConfirm={() => void save()} onCancel={() => setConfirm(false)}>
          <p>
            보관 기간을 {d.retentionDays}일에서 {days}일로 줄이면 기간이 지난 달의 조회 기록이 다음 정기 실행 때 지워지고 되돌릴 수 없습니다(요약만 남습니다).
          </p>
        </ConfirmDialog>
      )}
    </section>
  );
}
