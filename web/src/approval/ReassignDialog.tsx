// 결재자 변경 대화상자(admin): 결재 진행 중 건의 아직 끝나지 않은 직책 단계의 결재자를 다른 사람으로 바꾼다(B21 3.8·7.7).
// 구매요청 상세·결재 화면과 결재자 소멸 목록(B22)이 같은 컴포넌트를 연다. 규칙(자격·사유·재직 확인)은 서버가 판정하고 화면은 서버 문구를 그대로 보인다.
import { useEffect, useState } from "react";
import { api } from "../api/client";
import { Badge, Notice } from "../layout/ui";
import { errorText } from "../purchase/useLoad";

export interface ReassignTarget {
  prId: number;
  seq: number;
  /** 직책 이름(팀장·부서장 …). */
  title: string;
  /** 현재 결재자 이름과 조직. */
  currentName: string;
  currentUnit?: string;
  /** 지금 차례 단계이고 결재자가 없는 상태(퇴사 등)면 true. 이때는 재직 확인이 필요 없다. */
  stalled: boolean;
}

interface Candidate {
  userId: number;
  name: string;
  title: string;
  acting: boolean;
  unitName: string;
  unitPath: string;
}

const CAUSES = [
  { value: "leave", label: "퇴사" },
  { value: "transfer", label: "전보" },
  { value: "absence", label: "장기 부재" },
  { value: "other", label: "기타" },
];

export function ReassignDialog({ target, onClose, onDone }: { target: ReassignTarget; onClose: () => void; onDone: (message: string) => void }) {
  const [text, setText] = useState("");
  const [cands, setCands] = useState<Candidate[] | null>(null);
  const [userId, setUserId] = useState("");
  const [cause, setCause] = useState("");
  const [reason, setReason] = useState("");
  const [warn, setWarn] = useState(!target.stalled);
  const [confirm, setConfirm] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let stale = false;
    const t = setTimeout(() => {
      api
        .get<Candidate[]>(`/api/approvals/${target.prId}/steps/${target.seq}/reassign-candidates?text=${encodeURIComponent(text)}`)
        .then((list) => !stale && setCands(list))
        .catch((e) => !stale && setError(errorText(e)));
    }, 250);
    return () => {
      stale = true;
      clearTimeout(t);
    };
  }, [target.prId, target.seq, text]);

  const ready = userId !== "" && cause !== "" && reason.trim() !== "" && (!warn || confirm);

  async function submit() {
    setBusy(true);
    setError("");
    try {
      await api.post(`/api/approvals/${target.prId}/steps/${target.seq}/reassign`, {
        userId: Number(userId),
        cause,
        reason: reason.trim(),
        confirmActive: warn && confirm,
      });
      const to = cands?.find((c) => String(c.userId) === userId)?.name ?? "";
      onDone(`결재자를 ${target.currentName}님에서 ${to}님으로 바꿨습니다. 요청자와 새 결재자에게 알렸습니다.`);
    } catch (e) {
      const msg = errorText(e);
      // 서버가 재직 중 결재자 확인을 요구하면 확인 체크를 보인다.
      if (msg.includes("확인이 필요합니다")) setWarn(true);
      setError(msg);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="card" role="dialog" aria-modal="false" aria-label={`결재자 변경 — ${target.seq}단계 ${target.title}`}>
      <h3>
        {target.title === "합의" ? "합의자" : "결재자"} 변경 — {target.seq}단계 {target.title}
      </h3>
      <p>
        현재 결재자: {target.currentName || "—"}
        {target.currentUnit ? ` (${target.title}, ${target.currentUnit})` : ""}{" "}
        {target.stalled ? <Badge tone="warn">결재할 수 없음</Badge> : <Badge tone="info">재직 중</Badge>}
      </p>
      {warn && (
        <Notice tone="warn">
          현재 결재자 {target.currentName}님은 재직 중이며 결재할 수 있습니다. 바꾸면 {target.currentName}님은 이 단계를 결재할 수 없습니다.
          <br />
          <label>
            <input type="checkbox" checked={confirm} onChange={(e) => setConfirm(e.target.checked)} /> 그래도 바꿉니다
          </label>
        </Notice>
      )}
      <div className="field">
        <label htmlFor="reassign-search">새 결재자 검색</label>
        <input id="reassign-search" value={text} onChange={(e) => setText(e.target.value)} placeholder="이름 또는 조직" />
      </div>
      <div className="field">
        <label htmlFor="reassign-user">새 결재자</label>
        <select id="reassign-user" value={userId} onChange={(e) => setUserId(e.target.value)}>
          <option value="">선택하세요</option>
          {(cands ?? []).map((c) => (
            <option key={c.userId} value={c.userId}>
              {[c.name, c.title].filter(Boolean).join(" · ")}
              {c.acting ? "(직무대행)" : ""}
              {c.unitPath || c.unitName ? ` · ${c.unitPath || c.unitName}` : ""}
            </option>
          ))}
        </select>
        {cands && cands.length === 0 && (
          <span className="hint">이 단계를 맡을 수 있는 조직장이 없습니다. 조직도에서 조직장 보임 발령을 먼저 등록하세요.</span>
        )}
      </div>
      <div className="field">
        <label htmlFor="reassign-cause">구분</label>
        <select id="reassign-cause" value={cause} onChange={(e) => setCause(e.target.value)}>
          <option value="">선택하세요</option>
          {CAUSES.map((c) => (
            <option key={c.value} value={c.value}>
              {c.label}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label htmlFor="reassign-reason">사유</label>
        <textarea id="reassign-reason" value={reason} maxLength={500} rows={3} onChange={(e) => setReason(e.target.value)} />
        <span className="hint">결재 이력과 감사 로그에 남습니다.</span>
      </div>
      {error && <Notice tone="error">{error}</Notice>}
      <div className="form-row">
        <button type="button" disabled={busy || !ready} onClick={() => void submit()}>
          결재자 변경
        </button>
        <button type="button" className="btn-secondary" onClick={onClose}>
          취소
        </button>
      </div>
    </div>
  );
}
