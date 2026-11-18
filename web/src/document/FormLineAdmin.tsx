// 양식 관리: 기본 결재선(직책 자동·사용자·조직장·문서의 조직 선택), 기본 참조처·수신처, 사용 부서 제한을 폼으로 편집한다. JSON 을 직접 입력받지 않는다.
import { useEffect, useState } from "react";
import { api } from "../api/client";
import { PersonPicker } from "../org/PersonPicker";
import type { PersonCard } from "../org/types";
import { UnitSelect } from "./UnitSelect";
import type { FormDefinition, StepSpec } from "./types";
import { ROLE_LABEL } from "./types";

const KIND_LEVEL: Record<string, number> = { team_lead: 1, dept_head: 2, division_head: 3, ceo: 4 };
const KIND_LABEL: Record<string, string> = { team_lead: "팀장", dept_head: "부서장", division_head: "본부장", ceo: "대표" };
const SOURCE_LABEL: Record<string, string> = {
  org_level: "조직도로 자동(직책)",
  fixed_user: "사용자 지정",
  unit_head: "조직의 조직장",
  field_unit: "문서에서 고른 조직의 조직장",
};

interface TreeNode {
  id: number;
  name: string;
  closed?: boolean;
  children?: TreeNode[];
}

/** 조직 번호 → 이름 표(조직 트리). 불러오기 전에는 빈 표다. */
export function useUnitNames(): Record<number, string> {
  const [names, setNames] = useState<Record<number, string>>({});
  useEffect(() => {
    let stale = false;
    api
      .get<{ root: TreeNode }>("/api/org/tree")
      .then((t) => {
        if (stale) return;
        const m: Record<number, string> = {};
        const walk = (n: TreeNode) => {
          m[n.id] = n.name;
          (n.children ?? []).forEach(walk);
        };
        if (t?.root) walk(t.root);
        setNames(m);
      })
      .catch(() => undefined);
    return () => {
      stale = true;
    };
  }, []);
  return names;
}

export function FormLineAdmin({ def, editable, onChange }: { def: FormDefinition; editable: boolean; onChange: (p: Partial<FormDefinition>) => void }) {
  const unitNames = useUnitNames();
  const steps = def.line.steps ?? [];
  const useTiers = def.line.rule === "amount_tiers";
  const unitFields = def.fields.filter((f) => f.type === "unit");
  const setSteps = (next: StepSpec[]) => onChange({ line: { steps: next } });
  const setStep = (i: number, p: Partial<StepSpec>) => setSteps(steps.map((s, j) => (j === i ? { ...s, ...p } : s)));
  const move = (i: number, d: number) => {
    const next = [...steps];
    [next[i], next[i + d]] = [next[i + d], next[i]];
    setSteps(next);
  };
  const [person, setPerson] = useState<PersonCard | null>(null);
  const [pickFor, setPickFor] = useState(-1);
  const [recUnit, setRecUnit] = useState(0);
  const [limitUnit, setLimitUnit] = useState(0);

  const addRecipient = (kind: "cc" | "receive") => {
    if (!recUnit) return;
    onChange({ recipients: { ...def.recipients, [kind]: [...(def.recipients[kind] ?? []), { unit_id: recUnit }] } });
    setRecUnit(0);
  };
  const removeRecipient = (kind: "cc" | "receive", i: number) =>
    onChange({ recipients: { ...def.recipients, [kind]: (def.recipients[kind] ?? []).filter((_, j) => j !== i) } });
  const recName = (r: { unit_id?: number; unit_code?: string }) => (r.unit_id ? unitNames[r.unit_id] ?? `조직 ${r.unit_id}` : `조직 코드 ${r.unit_code}`);

  return (
    <div>
      <h2>기본 결재선</h2>
      <div className="field">
        <label htmlFor="line-mode">결재선 만드는 방식</label>
        <select
          id="line-mode"
          value={useTiers ? "tiers" : "steps"}
          disabled={!editable}
          onChange={(e) => onChange({ line: e.target.value === "tiers" ? { rule: "amount_tiers" } : { steps: steps.length ? steps : [{ role: "approve", kind: "team_lead", source: "org_level", level: 1, required: true }] } })}
        >
          <option value="steps">단계를 직접 정한다</option>
          <option value="tiers">금액 구간 규칙(결재선 금액 필드 필요)</option>
        </select>
      </div>
      {useTiers ? (
        <p className="hint">금액 구간별 결재선은 이 양식의 규칙 표(관리자 설정 데이터)를 따릅니다. 구간 경계와 직책은 규정 문서와 맞춰 관리합니다.</p>
      ) : (
        <>
          {steps.length === 0 && <p className="hint">단계가 없습니다. 결재 단계를 하나 이상 더하세요.</p>}
          {steps.map((s, i) => (
            <div className="card" key={i} aria-label={`기본 단계 ${i + 1}`}>
              <div className="form-row">
                <div className="field">
                  <label htmlFor={`st-role-${i}`}>역할</label>
                  <select
                    id={`st-role-${i}`}
                    value={s.role}
                    disabled={!editable}
                    onChange={(e) => {
                      const role = e.target.value;
                      setStep(i, role === "approve" ? { role, source: "org_level", kind: s.kind || "team_lead", level: KIND_LEVEL[s.kind || "team_lead"] } : { role, source: role === "agree" ? "unit_head" : "unit_head", kind: undefined, level: undefined });
                    }}
                  >
                    {["approve", "agree", "cc", "receive"].map((r) => (
                      <option key={r} value={r}>
                        {ROLE_LABEL[r]}
                      </option>
                    ))}
                  </select>
                </div>
                <div className="field">
                  <label htmlFor={`st-src-${i}`}>대상 정하는 방식</label>
                  <select id={`st-src-${i}`} value={s.source} disabled={!editable || s.role === "approve"} onChange={(e) => setStep(i, { source: e.target.value })}>
                    {(s.role === "approve" ? ["org_level"] : s.role === "agree" ? ["unit_head", "fixed_user", "field_unit"] : ["unit_head", "field_unit"]).map((k) => (
                      <option key={k} value={k}>
                        {SOURCE_LABEL[k]}
                      </option>
                    ))}
                  </select>
                </div>
              </div>
              {s.source === "org_level" && (
                <div className="field">
                  <label htmlFor={`st-kind-${i}`}>직책(조직도에서 위로 올라가며 찾는다)</label>
                  <select id={`st-kind-${i}`} value={s.kind ?? "team_lead"} disabled={!editable} onChange={(e) => setStep(i, { kind: e.target.value, level: KIND_LEVEL[e.target.value] })}>
                    {Object.keys(KIND_LEVEL).map((k) => (
                      <option key={k} value={k}>
                        {KIND_LABEL[k]}
                      </option>
                    ))}
                  </select>
                </div>
              )}
              {s.source === "unit_head" && (
                <div className="field">
                  <label htmlFor={`st-unit-${i}`}>조직 {s.unitId ? `(지금: ${unitNames[s.unitId] ?? s.unitId})` : s.unitCode ? `(코드 ${s.unitCode})` : ""}</label>
                  <UnitSelect id={`st-unit-${i}`} value={s.unitId ?? 0} onChange={(n) => setStep(i, { unitId: n || undefined, unitCode: undefined })} />
                </div>
              )}
              {s.source === "field_unit" && (
                <div className="field">
                  <label htmlFor={`st-field-${i}`}>조직을 고르는 필드</label>
                  <select id={`st-field-${i}`} value={s.field ?? ""} disabled={!editable} onChange={(e) => setStep(i, { field: e.target.value })}>
                    <option value="">선택하세요</option>
                    {unitFields.map((f) => (
                      <option key={f.key} value={f.key}>
                        {f.label}
                      </option>
                    ))}
                  </select>
                  {unitFields.length === 0 && <span className="hint">먼저 필드에 "조직 선택" 종류를 하나 추가하세요.</span>}
                </div>
              )}
              {s.source === "fixed_user" && (
                <div>
                  <p className="hint">지금 지정된 사용자 번호: {s.userId ?? "없음"}</p>
                  <PersonPicker
                    label={`단계 ${i + 1} 사용자`}
                    value={pickFor === i ? person : null}
                    onChange={(p) => {
                      setPerson(p);
                      setPickFor(i);
                      if (p) setStep(i, { userId: p.id });
                    }}
                  />
                </div>
              )}
              {s.role === "agree" && (
                <div className="field">
                  <label htmlFor={`st-stage-${i}`}>병렬 묶음(같은 번호의 합의는 동시에 처리, 비우면 순서대로)</label>
                  <input id={`st-stage-${i}`} value={s.stage ?? ""} inputMode="numeric" disabled={!editable} onChange={(e) => setStep(i, { stage: Number(e.target.value.replace(/\D/g, "").slice(0, 1)) || undefined })} />
                </div>
              )}
              <label>
                <input type="checkbox" checked={s.required} disabled={!editable} onChange={(e) => setStep(i, { required: e.target.checked })} /> 필수 단계(기안자가 뺄 수 없음)
              </label>
              <div className="form-row">
                <button type="button" className="btn-secondary btn-sm" disabled={!editable || i === 0} onClick={() => move(i, -1)}>
                  단계 {i + 1} 위로
                </button>
                <button type="button" className="btn-secondary btn-sm" disabled={!editable || i === steps.length - 1} onClick={() => move(i, 1)}>
                  단계 {i + 1} 아래로
                </button>
                <button type="button" className="btn-danger btn-sm" disabled={!editable} onClick={() => setSteps(steps.filter((_, j) => j !== i))}>
                  단계 {i + 1} 삭제
                </button>
              </div>
            </div>
          ))}
          <button type="button" className="btn-secondary" disabled={!editable} onClick={() => setSteps([...steps, { role: "approve", kind: "team_lead", source: "org_level", level: 1, required: true }])}>
            기본 단계 추가
          </button>
        </>
      )}

      <h2>기본 참조처·수신처</h2>
      {(["cc", "receive"] as const).map((kind) => (
        <div key={kind}>
          <h3>{ROLE_LABEL[kind]}</h3>
          {(def.recipients[kind] ?? []).length === 0 && <p className="hint">{ROLE_LABEL[kind]}처가 없습니다.</p>}
          <ul>
            {(def.recipients[kind] ?? []).map((r, i) => (
              <li key={i}>
                {recName(r)}{" "}
                <button type="button" className="btn-secondary btn-sm" disabled={!editable} onClick={() => removeRecipient(kind, i)}>
                  {recName(r)} {ROLE_LABEL[kind]}처 빼기
                </button>
              </li>
            ))}
          </ul>
        </div>
      ))}
      <div className="field">
        <label htmlFor="rec-unit">더할 조직</label>
        <UnitSelect id="rec-unit" value={recUnit} onChange={setRecUnit} />
      </div>
      <div className="form-row">
        <button type="button" className="btn-secondary btn-sm" disabled={!editable || !recUnit} onClick={() => addRecipient("cc")}>
          참조처로 더하기
        </button>
        <button type="button" className="btn-secondary btn-sm" disabled={!editable || !recUnit} onClick={() => addRecipient("receive")}>
          수신처로 더하기
        </button>
      </div>

      <h2>사용 부서 제한</h2>
      {def.dept_limit.length === 0 ? <p className="hint">제한이 없습니다(모든 부서가 씁니다).</p> : <p className="hint">아래 조직(하위 조직 포함) 소속만 이 양식을 쓸 수 있습니다.</p>}
      <ul>
        {def.dept_limit.map((id) => (
          <li key={id}>
            {unitNames[id] ?? `조직 ${id}`}{" "}
            <button type="button" className="btn-secondary btn-sm" disabled={!editable} onClick={() => onChange({ dept_limit: def.dept_limit.filter((x) => x !== id) })}>
              {unitNames[id] ?? `조직 ${id}`} 제한 빼기
            </button>
          </li>
        ))}
      </ul>
      <div className="field">
        <label htmlFor="limit-unit">제한에 더할 조직</label>
        <UnitSelect id="limit-unit" value={limitUnit} onChange={setLimitUnit} />
      </div>
      <button
        type="button"
        className="btn-secondary btn-sm"
        disabled={!editable || !limitUnit || def.dept_limit.includes(limitUnit)}
        onClick={() => {
          onChange({ dept_limit: [...def.dept_limit, limitUnit] });
          setLimitUnit(0);
        }}
      >
        사용 부서에 더하기
      </button>
    </div>
  );
}
