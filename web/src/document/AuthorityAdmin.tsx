// 전결 규정 관리 화면(B23 단계 3, admin): 양식·금액 구간별 최종 결재자 직책과 근거 조항을 표로 편집한다.
import { useState } from "react";
import { api } from "../api/client";
import { EmptyState, Notice, TableWrap } from "../layout/ui";
import { won } from "../purchase/labels";
import { errorText, useLoad } from "../purchase/useLoad";

export interface AuthorityRule {
  id: number;
  formCode: string;
  formName: string;
  minAmount: number;
  maxAmount: number;
  finalLevel: number;
  policySlug: string;
  policyTitle?: string;
  clauseNo: string;
  active: boolean;
}

const LEVELS: Record<number, string> = { 1: "팀장", 2: "부서장", 3: "본부장", 4: "대표" };

const blank = { id: 0, formCode: "", minAmount: "0", maxAmount: "", finalLevel: "2", policySlug: "", clauseNo: "", active: true };

export function AuthorityAdmin() {
  const rules = useLoad(() => api.get<AuthorityRule[]>("/api/authority-rules"), []);
  const policies = useLoad(() => api.get<{ slug: string; title: string }[]>("/api/authority-policies"), []);
  const forms = useLoad(() => api.get<{ code: string; name: string }[]>("/api/admin/forms"), []);
  const [form, setForm] = useState({ ...blank });
  const [err, setErr] = useState("");
  const [done, setDone] = useState("");
  const [busy, setBusy] = useState(false);

  function edit(r: AuthorityRule) {
    setDone("");
    setForm({
      id: r.id, formCode: r.formCode, minAmount: String(r.minAmount), maxAmount: r.maxAmount ? String(r.maxAmount) : "", finalLevel: String(r.finalLevel),
      policySlug: r.policySlug, clauseNo: r.clauseNo, active: r.active,
    });
  }
  async function save() {
    setErr("");
    setDone("");
    setBusy(true);
    const body = {
      formCode: form.formCode, minAmount: Number(form.minAmount) || 0, maxAmount: form.maxAmount === "" ? 0 : Number(form.maxAmount), finalLevel: Number(form.finalLevel),
      policySlug: form.policySlug.trim(), clauseNo: form.clauseNo.trim(), active: form.active,
    };
    try {
      if (form.id) await api.put(`/api/authority-rules/${form.id}`, body);
      else await api.post("/api/authority-rules", body);
      setForm({ ...blank });
      setDone("전결 규정을 저장했습니다. 이미 상신한 문서는 상신 때의 규정대로 진행합니다.");
      await rules.reload();
    } catch (e) {
      setErr(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  async function remove(r: AuthorityRule) {
    if (!window.confirm("이 전결 규정을 삭제할까요? 이미 상신한 문서에는 영향이 없습니다.")) return;
    setErr("");
    setDone("");
    try {
      await api.del(`/api/authority-rules/${r.id}`);
      await rules.reload();
    } catch (e) {
      setErr(errorText(e));
    }
  }
  const data = rules.data ?? [];
  return (
    <section>
      <h1>전결 규정</h1>
      <p className="hint">양식과 금액 구간마다 최종 결재자를 정합니다. 최종 결재자가 승인하면 그 뒤의 결재·합의 단계는 건너뛰고 결재가 끝납니다. 필수 합의 단계가 뒤에 있으면 전결은 적용되지 않습니다. 위임(부재 중 결재를 맡기는 것)과는 다릅니다.</p>
      {done && <Notice tone="ok">{done}</Notice>}
      {(err || rules.error) && <Notice tone="error">{err || rules.error}</Notice>}
      {!rules.loading && data.length === 0 && <EmptyState>등록된 전결 규정이 없습니다.</EmptyState>}
      {data.length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th>양식</th>
                <th className="nowrap">금액 구간</th>
                <th className="nowrap">최종 결재자</th>
                <th>근거 조항</th>
                <th className="nowrap">사용</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {data.map((r) => (
                <tr key={r.id}>
                  <td>{r.formCode ? r.formName : "모든 양식"}</td>
                  <td className="nowrap">
                    {won(r.minAmount)} 이상 {r.maxAmount ? `${won(r.maxAmount)} 미만` : "상한 없음"}
                  </td>
                  <td className="nowrap">{LEVELS[r.finalLevel]}</td>
                  <td>{r.policySlug ? `${r.policyTitle || "규정 문서(제목 없음)"} ${r.clauseNo}` : "—"}</td>
                  <td className="nowrap">{r.active ? "사용" : "사용 안 함"}</td>
                  <td className="nowrap">
                    <button type="button" className="btn-secondary btn-sm" onClick={() => edit(r)}>
                      수정
                    </button>{" "}
                    <button type="button" className="btn-danger btn-sm" onClick={() => void remove(r)}>
                      삭제
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      <h2>{form.id ? "규정 수정" : "규정 추가"}</h2>
      <div className="form-row">
        <label>
          양식
          <select value={form.formCode} onChange={(e) => setForm({ ...form, formCode: e.target.value })}>
            <option value="">모든 양식</option>
            {(forms.data ?? []).map((f) => (
              <option key={f.code} value={f.code}>
                {f.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          최소 금액(원, 이상)
          <input inputMode="numeric" value={form.minAmount} onChange={(e) => setForm({ ...form, minAmount: e.target.value })} />
        </label>
        <label>
          최대 금액(원, 미만 · 비우면 상한 없음)
          <input inputMode="numeric" value={form.maxAmount} onChange={(e) => setForm({ ...form, maxAmount: e.target.value })} />
        </label>
        <label>
          최종 결재자
          <select value={form.finalLevel} onChange={(e) => setForm({ ...form, finalLevel: e.target.value })}>
            {Object.entries(LEVELS).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </label>
      </div>
      <div className="form-row">
        <label>
          근거 규정 문서
          <select value={form.policySlug} onChange={(e) => setForm({ ...form, policySlug: e.target.value })}>
            <option value="">선택 안 함</option>
            {form.policySlug && !(policies.data ?? []).some((p) => p.slug === form.policySlug) && <option value={form.policySlug}>(제목을 찾을 수 없는 규정 문서)</option>}
            {(policies.data ?? []).map((p) => (
              <option key={p.slug} value={p.slug}>
                {p.title}
              </option>
            ))}
          </select>
        </label>
        <label>
          근거 조항 번호(예: 제2조)
          <input value={form.clauseNo} onChange={(e) => setForm({ ...form, clauseNo: e.target.value })} />
        </label>
        <label>
          <input type="checkbox" checked={form.active} onChange={(e) => setForm({ ...form, active: e.target.checked })} /> 사용
        </label>
        <button type="button" disabled={busy} onClick={() => void save()}>
          {form.id ? "수정 저장" : "규정 추가"}
        </button>
        {form.id !== 0 && (
          <button type="button" className="btn-secondary" onClick={() => setForm({ ...blank })}>
            취소
          </button>
        )}
      </div>
    </section>
  );
}
