// 결재선 편집기: 양식 기본 결재선(자동)을 보이고, 양식이 허용하는 범위에서 합의·참조·수신을 더한다. 개인 결재선 저장·불러오기 포함.
// 만들 수 있는지(상신 가능 여부)는 서버의 같은 결재선 만들기 함수가 판정한다. 화면은 서버가 돌려준 결과와 문구를 그대로 보인다.
import { AbsenceNote } from "../purchase/LinePreviewCard";
import { AuthorityLine } from "./AuthorityLine";
import { useEffect, useState } from "react";
import { api } from "../api/client";
import { Badge, Notice, TableWrap } from "../layout/ui";
import { PersonPicker } from "../org/PersonPicker";
import type { PersonCard } from "../org/types";
import { errorText } from "../purchase/useLoad";
import { UnitSelect } from "./UnitSelect";
import type { DocLinePreview, FormDefinition, LineEdit, PersonalLine } from "./types";
import { ROLE_LABEL } from "./types";

const DEBOUNCE_MS = 400;

export type LineReady = boolean | null;

function move(list: LineEdit[], i: number, d: number): LineEdit[] {
  const next = [...list];
  [next[i], next[i + d]] = [next[i + d], next[i]];
  return next;
}

interface Props {
  formCode: string;
  def: FormDefinition;
  fields: Record<string, unknown>;
  amount: number;
  edits: LineEdit[];
  onEdits: (e: LineEdit[]) => void;
  onReady: (r: LineReady, problems?: string[]) => void;
}

export function LineEditor({ formCode, def, fields, amount, edits, onEdits, onReady }: Props) {
  const [preview, setPreview] = useState<DocLinePreview | null>(null);
  const [failed, setFailed] = useState(false);
  const mode = def.settings.line_edit;

  const fieldsKey = JSON.stringify(fields);
  const editsKey = JSON.stringify(edits.map((e) => ({ role: e.role, targetType: e.targetType, targetId: e.targetId, group: e.group })));
  useEffect(() => {
    let stale = false;
    const timer = setTimeout(() => {
      api
        .post<DocLinePreview>("/api/approval-docs/line-preview", {
          formCode,
          amount,
          fields,
          edits: edits.map((e) => ({ role: e.role, targetType: e.targetType, targetId: e.targetId, group: e.group })),
        })
        .then((p) => {
          if (stale) return;
          setPreview(p);
          setFailed(false);
          onReady(p.complete, p.problems);
        })
        .catch(() => {
          if (stale) return;
          setPreview(null);
          setFailed(true);
          onReady(null);
        });
    }, DEBOUNCE_MS);
    return () => {
      stale = true;
      clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [formCode, amount, fieldsKey, editsKey]);

  return (
    <aside className="card" aria-label="결재선">
      <h2>결재선</h2>
      {failed && <Notice tone="warn">결재선을 불러오지 못했습니다. 잠시 뒤 다시 시도해 주세요. 상신 때 서버가 다시 확인합니다.</Notice>}
      {preview && (
        <>
          <p className="hint">기안자 소속: {preview.unitPath || "—"}. 상신하는 시점의 조직 기준으로 다시 만들어집니다.</p>
          {preview.complete && preview.steps.length > 0 && (
            <StepTable
              preview={preview}
              canRemove={mode === "free"}
              onRemove={(defIndex) => onEdits([...edits, { role: "remove", targetType: "default", targetId: defIndex, group: 0 }])}
            />
          )}
          {preview.complete && <AuthorityLine note={preview.authority} plain hideWarning />}
          {preview.complete && preview.recipients.length > 0 && (
            <ul className="hint">
              {preview.recipients
                .filter((r) => !(r.kind === "cc" && r.targetType === "user" && r.note))
                .map((r) => (
                  <li key={`${r.kind}-${r.targetType}-${r.targetId}`}>
                    {ROLE_LABEL[r.kind]}: {r.name}
                    {r.path ? ` (${r.path})` : ""}
                  </li>
                ))}
            </ul>
          )}
          {preview.complete ? (
            <ul className="hint">
              {preview.notes.map((n) => (
                <li key={n}>{n}</li>
              ))}
            </ul>
          ) : (
            <Notice tone="error">
              <strong>이 상태로는 상신할 수 없습니다. 임시저장은 할 수 있습니다.</strong>
              <br />
              {preview.problems.map((p) => (
                <span key={p}>
                  {p}
                  <br />
                </span>
              ))}
            </Notice>
          )}
        </>
      )}
      {mode === "none" ? (
        <p className="hint">이 양식은 결재선을 바꿀 수 없습니다.</p>
      ) : (
        <EditPanel formCode={formCode} mode={mode} edits={edits} onEdits={onEdits} />
      )}
    </aside>
  );
}

function StepTable({ preview, canRemove, onRemove }: { preview: DocLinePreview; canRemove: boolean; onRemove: (defIndex: number) => void }) {
  const steps = preview.steps.filter((s) => !s.omitted);
  const parallel = new Map<number, number>();
  for (const s of steps) if (s.role === "agree") parallel.set(s.stage, (parallel.get(s.stage) ?? 0) + 1);
  return (
    <TableWrap>
      <table>
        <thead>
          <tr>
            <th className="nowrap">단계</th>
            <th className="nowrap">역할</th>
            <th className="nowrap">직책</th>
            <th>결재자</th>
            <th>소속 조직</th>
            <th>비고</th>
            {canRemove && <th />}
          </tr>
        </thead>
        <tbody>
          {steps.map((s) => (
            <tr key={s.order}>
              <td className="nowrap">{s.order}</td>
              <td className="nowrap">
                {ROLE_LABEL[s.role] ?? "결재"}
                {s.role === "agree" && (parallel.get(s.stage) ?? 0) > 1 && <> (병렬)</>}
              </td>
              <td className="nowrap">{s.title}</td>
              <td>
                {s.missing ? "—" : s.kind === "finance" ? <>재무 담당({s.poolSize}명)</> : <>{s.approverName} {s.acting && <Badge tone="info">직무대행</Badge>}{s.absentUntil && <AbsenceNote until={s.absentUntil} delegate={s.absenceDelegate} />}</>}
              </td>
              <td>{s.unitPath || s.unitName || ""}</td>
              <td>{[s.raisedNote, s.layerNote, s.required ? "필수" : "", s.skipped ? "(전결)" : preview.authority?.finalOrder === s.order ? "전결" : ""].filter(Boolean).join(" ")}</td>
              {canRemove && (
                <td className="nowrap">
                  {!s.required && s.defIndex ? (
                    <button type="button" className="btn-secondary btn-sm" onClick={() => onRemove(s.defIndex!)}>
                      {s.order}단계 빼기
                    </button>
                  ) : null}
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </TableWrap>
  );
}

/** 결재선 편집 칸. mode: free(결재·합의·참조·수신), add_only(합의·참조·수신), cc_receive(구매 품의서: 참조·수신만). */
export function EditPanel({ formCode, mode, edits, onEdits }: { formCode: string; mode: string; edits: LineEdit[]; onEdits: (e: LineEdit[]) => void }) {
  const [role, setRole] = useState<LineEdit["role"]>(mode === "cc_receive" ? "cc" : "agree");
  const [person, setPerson] = useState<PersonCard | null>(null);
  const [unit, setUnit] = useState(0);
  const [unitName, setUnitName] = useState("");
  const [group, setGroup] = useState("");
  const [saved, setSaved] = useState<PersonalLine[]>([]);
  const [pick, setPick] = useState("");
  const [name, setName] = useState("");
  const [msg, setMsg] = useState("");
  const unitOnly = role === "receive";
  const roles: LineEdit["role"][] = mode === "free" ? ["agree", "cc", "receive", "approve"] : mode === "cc_receive" ? ["cc", "receive"] : ["agree", "cc", "receive"];

  const reload = () => api.get<PersonalLine[]>("/api/personal-lines").then((r) => setSaved(Array.isArray(r) ? r : [])).catch(() => setSaved([]));
  useEffect(() => {
    void reload();
  }, []);

  function add() {
    const g = role === "agree" ? Number(group) || 0 : 0;
    if (unitOnly) {
      if (!unit) return;
      onEdits([...edits, { role, targetType: "unit", targetId: unit, group: g, name: unitName }]);
      setUnit(0);
    } else if (person) {
      onEdits([...edits, { role, targetType: "user", targetId: person.id, group: g, name: person.name }]);
      setPerson(null);
    }
  }

  async function savePersonal() {
    setMsg("");
    // 같은 이름이 이미 있으면 덮어쓰기 전에 확인한다(서버는 양식 지정을 보존한다).
    if (saved.some((l) => l.name === name.trim()) && !window.confirm(`「${name.trim()}」 개인 결재선이 이미 있습니다. 덮어쓸까요?`)) return;
    try {
      await api.post("/api/personal-lines", { name, formCode, edits: edits.map((e) => ({ role: e.role, targetType: e.targetType, targetId: e.targetId, group: e.group })) });
      setName("");
      setMsg("개인 결재선을 저장했습니다.");
      await reload();
    } catch (e) {
      setMsg(errorText(e));
    }
  }

  async function loadPersonal() {
    setMsg("");
    try {
      const r = await api.get<{ line: PersonalLine; notes: string[] }>(`/api/personal-lines/${pick}`);
      onEdits(r.line.edits);
      setMsg(r.notes.length ? r.notes.join(" ") : "개인 결재선을 불러왔습니다.");
    } catch (e) {
      setMsg(errorText(e));
    }
  }

  async function removePersonal() {
    try {
      await api.del(`/api/personal-lines/${pick}`);
      setPick("");
      await reload();
    } catch (e) {
      setMsg(errorText(e));
    }
  }

  return (
    <div>
      <h3>결재선에 더하기</h3>
      <div className="field">
        <label htmlFor="edit-role">역할</label>
        <select id="edit-role" value={role} onChange={(e) => setRole(e.target.value as LineEdit["role"])}>
          {roles.map((r) => (
            <option key={r} value={r}>
              {ROLE_LABEL[r]}
            </option>
          ))}
        </select>
      </div>
      {unitOnly ? (
        <div className="field">
          <label htmlFor="edit-unit">수신처 조직</label>
          <UnitSelect id="edit-unit" value={unit} onChange={(n, name) => { setUnit(n); setUnitName(name); }} />
        </div>
      ) : (
        <PersonPicker label="사람" value={person} onChange={setPerson} />
      )}
      {role === "agree" && (
        <div className="field">
          <label htmlFor="edit-group">병렬 묶음(같은 번호의 합의는 동시에 처리, 비우면 순서대로)</label>
          <input id="edit-group" value={group} inputMode="numeric" onChange={(e) => setGroup(e.target.value.replace(/\D/g, "").slice(0, 1))} />
        </div>
      )}
      <button type="button" className="btn-secondary" disabled={unitOnly ? !unit : !person} onClick={add}>
        결재선에 더하기
      </button>
      {edits.length > 0 && (
        <ul>
          {edits.map((e, i) => (
            <li key={`${e.role}-${e.targetType}-${e.targetId}-${i}`}>
              {e.role === "remove" ? (
                <>기본 {e.targetId}번째 단계를 뺍니다</>
              ) : (
                <>
                  {ROLE_LABEL[e.role]}: {e.name ?? `${e.targetType === "unit" ? "조직" : "사용자"} ${e.targetId}`}
                  {e.group ? ` (병렬 ${e.group})` : ""}
                </>
              )}{" "}
              {e.role !== "remove" && mode === "free" && (
                <>
                  <button type="button" className="btn-secondary btn-sm" disabled={i === 0} onClick={() => onEdits(move(edits, i, -1))}>
                    위로
                  </button>{" "}
                  <button type="button" className="btn-secondary btn-sm" disabled={i === edits.length - 1} onClick={() => onEdits(move(edits, i, 1))}>
                    아래로
                  </button>{" "}
                </>
              )}
              <button type="button" className="btn-secondary btn-sm" onClick={() => onEdits(edits.filter((_, j) => j !== i))}>
                {e.role === "remove" ? "되돌리기" : "빼기"}
              </button>
            </li>
          ))}
        </ul>
      )}
      <h3>개인 결재선</h3>
      <div className="form-row">
        <select aria-label="개인 결재선 선택" value={pick} onChange={(e) => setPick(e.target.value)}>
          <option value="">저장한 결재선 선택</option>
          {saved.map((l) => (
            <option key={l.id} value={l.id}>
              {l.name}
            </option>
          ))}
        </select>
        <button type="button" className="btn-secondary btn-sm" disabled={!pick} onClick={() => void loadPersonal()}>
          불러오기
        </button>
        <button type="button" className="btn-secondary btn-sm" disabled={!pick} onClick={() => void removePersonal()}>
          삭제
        </button>
      </div>
      {saved.length === 0 && <span className="hint">저장한 결재선이 없습니다.</span>}
      <div className="form-row">
        <input aria-label="개인 결재선 이름" value={name} maxLength={40} placeholder="저장할 이름" onChange={(e) => setName(e.target.value)} />
        <button type="button" className="btn-secondary btn-sm" disabled={name.trim() === "" || edits.length === 0} onClick={() => void savePersonal()}>
          지금 편집을 저장
        </button>
      </div>
      {msg && <span className="hint">{msg}</span>}
    </div>
  );
}
