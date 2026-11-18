// 도입 점검표(B38 명세 5절): 항목별 완료 여부를 서버가 DB 에서 계산해 보여 준다. onboarding:read(관리자).
// 사람이 정하는 것은 "해당 없음"과 사유뿐이다(수동 체크박스는 없다). 비밀값·경로는 보이지 않는다.
import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { Badge, EmptyState, Notice, type Tone } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

export type ItemState = "done" | "in_progress" | "todo" | "not_applicable" | "unknown";

export interface ChecklistItem {
  id: string;
  title: string;
  state: ItemState;
  evidence: string;
  link: string;
  skippable: boolean;
  skip?: { reason: string; by: number; at: string };
}

export interface Checklist {
  items: ChecklistItem[];
  summary: { total: number; done: number; notApplicable: number };
}

export const STATE_LABEL: Record<ItemState, string> = {
  done: "완료",
  in_progress: "진행 중",
  todo: "미착수",
  not_applicable: "해당 없음",
  unknown: "확인하지 못했습니다",
};
const STATE_TONE: Record<ItemState, Tone> = { done: "ok", in_progress: "info", todo: "warn", not_applicable: "muted", unknown: "bad" };

export function OnboardingPage() {
  const list = useLoad(() => api.get<Checklist>("/api/admin/onboarding"), []);
  const [open, setOpen] = useState(""); // "해당 없음" 사유 입력을 연 항목
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const d = list.data;

  async function skip(e: FormEvent, id: string) {
    e.preventDefault();
    setBusy(true);
    setNote(null);
    try {
      await api.post("/api/admin/onboarding/skips", { item: id, reason });
      setOpen("");
      setReason("");
      setNote({ tone: "ok", text: "해당 없음으로 표시했습니다." });
      await list.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  async function unskip(id: string) {
    setBusy(true);
    setNote(null);
    try {
      await api.del(`/api/admin/onboarding/skips/${encodeURIComponent(id)}`);
      setNote({ tone: "ok", text: "해당 없음 표시를 풀었습니다." });
      await list.reload();
    } catch (err) {
      setNote({ tone: "error", text: errorText(err) });
    } finally {
      setBusy(false);
    }
  }

  return (
    <section>
      <h1>도입 점검표</h1>
      <p>회사에서 이 시스템을 쓰기 위해 갖춰야 할 설정의 진행 상황입니다. 상태는 저장된 데이터를 읽어 그때그때 계산합니다. 이 회사에 해당하지 않는 항목만 사유와 함께 "해당 없음"으로 표시할 수 있습니다.</p>
      {list.error && <Notice tone="error">{list.error}</Notice>}
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      {list.loading && !d && <p className="hint">불러오는 중…</p>}
      {d && d.items.length === 0 && <EmptyState hint="잠시 뒤 다시 시도하세요.">점검할 항목을 불러오지 못했습니다.</EmptyState>}
      {d && d.items.length > 0 && (
        <>
          <p className="chip-row" aria-label="진행 요약">
            <Badge tone="ok">
              완료 {d.summary.done} / {d.summary.total}
            </Badge>
            {d.summary.notApplicable > 0 && <Badge tone="muted">해당 없음 {d.summary.notApplicable}</Badge>}
          </p>
          <ol className="onboarding-list">
            {d.items.map((it, i) => (
              <li key={it.id} className="card" aria-labelledby={`ob-${it.id}`}>
                <h2 id={`ob-${it.id}`}>
                  {i + 1}. {it.title} <Badge tone={STATE_TONE[it.state]}>{STATE_LABEL[it.state]}</Badge>
                </h2>
                <p>{it.evidence}</p>
                {it.skip && (
                  <p className="hint">
                    해당 없음 사유: {it.skip.reason} ({new Date(it.skip.at).toLocaleDateString("ko-KR")})
                  </p>
                )}
                <p className="form-actions">
                  {it.link && (
                    <Link className="btn btn-secondary" to={it.link}>
                      바로가기
                    </Link>
                  )}{" "}
                  {it.skippable && !it.skip && open !== it.id && (
                    <button type="button" className="btn-secondary" disabled={busy} onClick={() => setOpen(it.id)}>
                      해당 없음으로 표시
                    </button>
                  )}
                  {it.skip && (
                    <button type="button" className="btn-secondary" disabled={busy} onClick={() => void unskip(it.id)}>
                      해당 없음 풀기
                    </button>
                  )}
                </p>
                {open === it.id && (
                  <form onSubmit={(e) => void skip(e, it.id)} aria-label={`${it.title} 해당 없음 사유`}>
                    <div className="field">
                      <label htmlFor={`ob-reason-${it.id}`}>해당 없음 사유</label>
                      <input id={`ob-reason-${it.id}`} value={reason} maxLength={200} onChange={(e) => setReason(e.target.value)} required />
                      <span className="hint">200자 이내로 한 줄로 적습니다. 감사 기록에 남습니다.</span>
                    </div>
                    <button type="submit" disabled={busy}>
                      표시
                    </button>{" "}
                    <button type="button" className="btn-secondary" onClick={() => setOpen("")}>
                      취소
                    </button>
                  </form>
                )}
              </li>
            ))}
          </ol>
        </>
      )}
    </section>
  );
}
