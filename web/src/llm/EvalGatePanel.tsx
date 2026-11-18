//(B46 3단계 화면). 세트 상세의 "저장 전 평가" 칸: 용도 패턴에 이 세트를 지정하고 허용 하락을 정한다(eval:write).
// 라우팅 규칙을 저장할 때 이 세트를 후보 경로로 돌려 기준선과 견준다.
import { useState } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { serverMessage } from "../api/serverError";
import { Badge, Notice, TableWrap } from "../layout/ui";
import { useLoad } from "../purchase/useLoad";

interface GateRow {
  id: number;
  purposePattern: string;
  setCode: string;
  setName: string;
  enabled: boolean;
  gateDropPct: number;
}

interface Props {
  code: string;
  name: string;
  description: string;
  gateDropPct: number;
  hasBaseline: boolean;
  onChanged: () => Promise<void>;
}

export function EvalGatePanel({ code, name, description, gateDropPct, hasBaseline, onChanged }: Props) {
  const { can } = useAuth();
  const write = can("eval:write");
  const gates = useLoad(() => api.get<{ gates: GateRow[] }>("/api/admin/eval/gates"), []);
  const mine = (gates.data?.gates ?? []).filter((g) => g.setCode === code);
  const [pattern, setPattern] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [drop, setDrop] = useState(String(gateDropPct));
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<{ tone: "ok" | "error"; text: string } | null>(null);

  async function save() {
    setBusy(true);
    setNote(null);
    try {
      const pct = Number(drop);
      if (!Number.isInteger(pct) || pct < 0 || pct > 100) {
        setNote({ tone: "error", text: "허용 하락은 0~100 사이 정수여야 합니다." });
        return;
      }
      if (pct !== gateDropPct) await api.put(`/api/admin/eval/sets/${code}`, { name, description, gateDropPct: pct });
      await api.put("/api/admin/eval/gates", { purposePattern: pattern, setCode: code, enabled });
      setNote({ tone: "ok", text: "저장 전 평가 지정을 저장했습니다." });
      setPattern("");
      await gates.reload();
      await onChanged();
    } catch (err) {
      setNote({ tone: "error", text: serverMessage(err) });
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="card" aria-labelledby="eval-gate">
      <h2 id="eval-gate">저장 전 평가</h2>
      <p className="hint">용도를 지정하면 그 용도의 LLM 라우팅 규칙을 저장하기 전에 이 세트의 앞 20문항을 후보 공급자로 돌려 기준선과 견줍니다. 기준선보다 허용 하락 넘게 낮거나 치명 문항이 틀리면 저장 전에 확인을 받습니다.</p>
      {!hasBaseline && <Notice tone="warn">기준선이 없어 지금은 평가를 건너뜁니다. 실행 결과에서 기준선을 지정하세요.</Notice>}
      {note && <Notice tone={note.tone}>{note.text}</Notice>}
      <TableWrap busy={gates.loading}>
        <table className="stack-table" aria-label="저장 전 평가 지정">
          <thead>
            <tr>
              <th>용도</th>
              <th>허용 하락</th>
              <th>상태</th>
            </tr>
          </thead>
          <tbody>
            {mine.length === 0 && !gates.loading && (
              <tr>
                <td colSpan={3}>이 세트로 지정된 용도가 없습니다.</td>
              </tr>
            )}
            {mine.map((g) => (
              <tr key={g.id}>
                <td data-label="용도">{g.purposePattern === "*" ? "모든 용도" : g.purposePattern}</td>
                <td data-label="허용 하락" className="num">
                  {g.gateDropPct}%p
                </td>
                <td data-label="상태">
                  <Badge tone={g.enabled ? "ok" : "muted"}>{g.enabled ? "켜짐" : "꺼짐"}</Badge>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      {write && (
        <form
          aria-label="저장 전 평가 양식"
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
          <div className="form-row">
            <div className="field">
              <label htmlFor="eg-pattern">용도 패턴</label>
              <input id="eg-pattern" value={pattern} placeholder="docverify.* 또는 *" required onChange={(e) => setPattern(e.target.value.toLowerCase())} />
            </div>
            <div className="field">
              <label htmlFor="eg-drop">허용 하락(%p)</label>
              <input id="eg-drop" type="number" min={0} max={100} value={drop} onChange={(e) => setDrop(e.target.value)} />
            </div>
            <label htmlFor="eg-on">
              <input id="eg-on" type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} /> 켜기
            </label>
            <button type="submit" disabled={busy}>
              지정 저장
            </button>
          </div>
        </form>
      )}
    </section>
  );
}
