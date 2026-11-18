// 양식 관리(admin, form:manage): 양식 목록, 새 버전 만들기, 초안 편집기(필드를 폼으로 편집하고 JSON 을 직접 치지 않는다), 게시, 사용 중지.
import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api } from "../api/client";
import { Badge, Breadcrumbs, EmptyState, Notice, TableWrap, useToast } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";
import { FormLineAdmin } from "./FormLineAdmin";
import type { FieldDef, FieldType, FormAdminRow, FormDefinition, FormVersion } from "./types";
import { CATEGORY_LABEL, FIELD_TYPE_LABEL, LINE_EDIT_LABEL, OBJECTION_LABEL, VERSION_STATUS_LABEL } from "./types";

export function FormAdminList() {
  const nav = useNavigate();
  const { data, error, loading, reload } = useLoad(() => api.get<FormAdminRow[]>("/api/admin/forms"), []);
  const [msg, setMsg] = useState("");
  async function newVersion(code: string) {
    setMsg("");
    try {
      const v = await api.post<FormVersion>(`/api/admin/forms/${code}/versions`);
      nav(`/forms/versions/${v.id}`);
    } catch (e) {
      setMsg(errorText(e));
    }
  }
  async function setActive(code: string, active: boolean) {
    setMsg("");
    try {
      await api.post(`/api/admin/forms/${code}/active`, { active });
      await reload();
    } catch (e) {
      setMsg(errorText(e));
    }
  }
  return (
    <section>
      <h1>결재 양식 관리</h1>
      {error && <Notice tone="error">{error}</Notice>}
      {msg && <Notice tone="error">{msg}</Notice>}
      {loading && <p>불러오는 중…</p>}
      {data && data.length === 0 && <EmptyState>등록된 양식이 없습니다.</EmptyState>}
      {data && data.length > 0 && (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th>양식</th>
                <th className="nowrap">분류</th>
                <th className="nowrap">최신 버전</th>
                <th className="nowrap">사용</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {data.map((f) => (
                <tr key={f.code}>
                  <td>{f.name}</td>
                  <td className="nowrap">{CATEGORY_LABEL[f.category] ?? f.category}</td>
                  <td className="nowrap">
                    {f.latestVersion}판 <Badge tone={f.latestStatus === "draft" ? "warn" : "ok"}>{VERSION_STATUS_LABEL[f.latestStatus] ?? f.latestStatus}</Badge>
                  </td>
                  <td className="nowrap">{f.active ? "사용 중" : "사용 중지"}</td>
                  <td className="nowrap">
                    {f.hasDraftVersion ? (
                      <Link to={`/forms/versions/by-form/${f.code}`}>작성 중인 버전 이어서 고치기</Link>
                    ) : (
                      <button type="button" className="btn-secondary btn-sm" onClick={() => void newVersion(f.code)}>
                        {f.name} 새 버전 만들기
                      </button>
                    )}{" "}
                    <button type="button" className="btn-secondary btn-sm" onClick={() => void setActive(f.code, !f.active)}>
                      {f.active ? `${f.name} 사용 중지` : `${f.name} 사용 재개`}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}

const TYPES = Object.keys(FIELD_TYPE_LABEL) as FieldType[];

function slug(n: number) {
  return `field_${n}`;
}

function FieldRow({ f, i, n, system, onChange, onMove, onRemove }: { f: FieldDef; i: number; n: number; system: boolean; onChange: (f: FieldDef) => void; onMove: (d: number) => void; onRemove: () => void }) {
  const set = (p: Partial<FieldDef>) => onChange({ ...f, ...p });
  return (
    <div className="card" aria-label={`필드 ${i + 1}`}>
      <div className="form-row">
        <div className="field">
          <label htmlFor={`fl-${i}`}>이름</label>
          <input id={`fl-${i}`} value={f.label} disabled={system} onChange={(e) => set({ label: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor={`fk-${i}`}>키(영문 소문자·숫자·밑줄)</label>
          <input id={`fk-${i}`} value={f.key} disabled={system} onChange={(e) => set({ key: e.target.value })} />
        </div>
        <div className="field">
          <label htmlFor={`ft-${i}`}>종류</label>
          <select id={`ft-${i}`} value={f.type} disabled={system} onChange={(e) => set({ type: e.target.value as FieldType })}>
            {TYPES.map((t) => (
              <option key={t} value={t}>
                {FIELD_TYPE_LABEL[t]}
              </option>
            ))}
          </select>
        </div>
      </div>
      <label>
        <input type="checkbox" checked={f.required} disabled={system} onChange={(e) => set({ required: e.target.checked })} /> 필수
      </label>{" "}
      <label>
        <input type="checkbox" checked={!!f.sensitive} disabled={system} onChange={(e) => set({ sensitive: e.target.checked })} /> 민감 정보(권한 없는 열람자에게 가림)
      </label>
      {f.type === "select" && (
        <div className="field">
          <label htmlFor={`fo-${i}`}>선택지(한 줄에 하나)</label>
          <textarea id={`fo-${i}`} rows={3} value={(f.options ?? []).join("\n")} disabled={system} onChange={(e) => set({ options: e.target.value.split("\n").map((s) => s.trim()).filter(Boolean) })} />
        </div>
      )}
      {(f.type === "text" || f.type === "textarea") && (
        <div className="field">
          <label htmlFor={`fm-${i}`}>최대 글자 수</label>
          <input id={`fm-${i}`} inputMode="numeric" value={f.maxLength ?? ""} disabled={system} onChange={(e) => set({ maxLength: Number(e.target.value.replace(/\D/g, "")) || undefined })} />
        </div>
      )}
      {f.type === "table" && (
        <div>
          <h4>표의 열</h4>
          {(f.columns ?? []).map((c, j) => (
            <div className="form-row" key={j}>
              <input aria-label={`열 ${j + 1} 이름`} value={c.label} disabled={system} onChange={(e) => set({ columns: (f.columns ?? []).map((x, k) => (k === j ? { ...x, label: e.target.value } : x)) })} />
              <input aria-label={`열 ${j + 1} 키`} value={c.key} disabled={system} onChange={(e) => set({ columns: (f.columns ?? []).map((x, k) => (k === j ? { ...x, key: e.target.value } : x)) })} />
              <select aria-label={`열 ${j + 1} 종류`} value={c.type} disabled={system} onChange={(e) => set({ columns: (f.columns ?? []).map((x, k) => (k === j ? { ...x, type: e.target.value as FieldType } : x)) })}>
                {(["text", "number", "amount", "date", "select"] as FieldType[]).map((t) => (
                  <option key={t} value={t}>
                    {FIELD_TYPE_LABEL[t]}
                  </option>
                ))}
              </select>
              <button type="button" className="btn-secondary btn-sm" disabled={system} onClick={() => set({ columns: (f.columns ?? []).filter((_, k) => k !== j) })}>
                열 {j + 1} 삭제
              </button>
            </div>
          ))}
          <button type="button" className="btn-secondary btn-sm" disabled={system} onClick={() => set({ columns: [...(f.columns ?? []), { key: `col_${(f.columns ?? []).length + 1}`, label: "열", type: "text", required: false }] })}>
            열 추가
          </button>
        </div>
      )}
      <div className="form-row">
        <button type="button" className="btn-secondary btn-sm" disabled={system || i === 0} onClick={() => onMove(-1)}>
          {f.label} 위로
        </button>
        <button type="button" className="btn-secondary btn-sm" disabled={system || i === n - 1} onClick={() => onMove(1)}>
          {f.label} 아래로
        </button>
        <button type="button" className="btn-danger btn-sm" disabled={system} onClick={onRemove}>
          {f.label} 삭제
        </button>
      </div>
    </div>
  );
}

/** 본문 서식의 {{키}} 를 샘플 값으로 채운 미리보기. */
export function bodyPreview(body: string, fields: FieldDef[]): string {
  return body.replace(/\{\{\s*([a-z][a-z0-9_]*)\s*\}\}/g, (_, k: string) => {
    const f = fields.find((x) => x.key === k);
    if (!f) return `［없는 필드: ${k}］`;
    return `［${f.label} 값］`;
  });
}

export function FormVersionEditor() {
  const { id, code } = useParams();
  const nav = useNavigate();
  const [problems, setProblems] = useState("");
  const toast = useToast();
  const [preview, setPreview] = useState("");
  const [defOverride, setDef] = useState<FormDefinition | null>(null);
  const verId = id ? Number(id) : null;
  const ver = useLoad(
    async () => {
      if (verId) return api.get<FormVersion>(`/api/admin/forms/versions/${verId}`);
      return null;
    },
    [verId],
  );
  // /forms/versions/by-form/:code 는 그 양식의 작성 중 버전으로 이동한다(없으면 게시 버전을 복사해 새 초안을 만든다. 서버가 이미 있는 초안이면 그것을 돌려준다).
  useEffect(() => {
    if (!code || verId) return;
    let stale = false;
    api
      .post<FormVersion>(`/api/admin/forms/${code}/versions`)
      .then((nv) => !stale && nav(`/forms/versions/${nv.id}`, { replace: true }))
      .catch(() => !stale && setProblems("작성 중인 버전을 열지 못했습니다. 양식 관리 목록에서 다시 시도하세요."));
    return () => {
      stale = true;
    };
  }, [code, verId, nav]);
  const v = ver.data;
  if (ver.error) return <Notice tone="error">{ver.error}</Notice>;
  if (!v) return <p>{problems || "불러오는 중…"}</p>;
  const def = defOverride ?? v.definition;
  const editable = v.status === "draft";
  const system = def.system;
  const set = (p: Partial<FormDefinition>) => setDef({ ...def, ...p });
  const setSettings = (p: Partial<FormDefinition["settings"]>) => set({ settings: { ...def.settings, ...p } });
  const setField = (i: number, f: FieldDef) => set({ fields: def.fields.map((x, j) => (j === i ? f : x)) });
  const move = (i: number, d: number) => {
    const next = [...def.fields];
    [next[i], next[i + d]] = [next[i + d], next[i]];
    set({ fields: next });
  };

  async function save(): Promise<boolean> {
    setProblems("");
    try {
      await api.put(`/api/admin/forms/versions/${v!.id}`, { definition: def });
      toast("양식 초안을 저장했습니다.");
      return true;
    } catch (e) {
      setProblems(errorText(e));
      return false;
    }
  }
  async function publish() {
    if (!(await save())) return;
    try {
      await api.post(`/api/admin/forms/versions/${v!.id}/publish`);
      nav("/forms");
    } catch (e) {
      setProblems(errorText(e));
    }
  }
  async function showNumber() {
    if (!(await save())) return;
    try {
      const r = await api.get<{ number: string }>(`/api/admin/forms/versions/${v!.id}/number-preview`);
      setPreview(r.number);
    } catch (e) {
      setProblems(errorText(e));
    }
  }

  return (
    <section>
      <Breadcrumbs items={[{ label: "양식 관리", to: "/forms" }, { label: `${v.formCode} ${v.version}판` }]} />
      <h1>
        {v.formCode} {v.version}판 <Badge tone={editable ? "warn" : "ok"}>{VERSION_STATUS_LABEL[v.status]}</Badge>
      </h1>
      {!editable && <Notice tone="info">게시된 버전은 고칠 수 없습니다. 양식 관리에서 새 버전을 만들어 고치세요.</Notice>}
      {system && <Notice tone="info">시스템 양식의 필드와 본문은 고칠 수 없습니다. 번호 규칙·보존 연한·설정은 고칠 수 있습니다.</Notice>}
      <h2>필드</h2>
      {def.fields.length === 0 && <EmptyState>필드가 없습니다.</EmptyState>}
      {def.fields.map((f, i) => (
        <FieldRow key={i} f={f} i={i} n={def.fields.length} system={system || !editable} onChange={(nf) => setField(i, nf)} onMove={(d) => move(i, d)} onRemove={() => set({ fields: def.fields.filter((_, j) => j !== i) })} />
      ))}
      <button type="button" className="btn-secondary" disabled={system || !editable} onClick={() => set({ fields: [...def.fields, { key: slug(def.fields.length + 1), label: "새 필드", type: "text", required: false }] })}>
        필드 추가
      </button>

      <h2>본문 서식</h2>
      <div className="field">
        <label htmlFor="body">본문 서식 ({"{{필드키}}"} 자리에 값이 들어갑니다. 비우면 필드를 순서대로 보입니다)</label>
        <textarea id="body" rows={5} value={def.body} disabled={system || !editable} onChange={(e) => set({ body: e.target.value })} />
        <pre aria-label="본문 서식 미리보기" className="hint">
          {bodyPreview(def.body, def.fields) || "(서식 없음)"}
        </pre>
      </div>

      <FormLineAdmin def={def} editable={editable} onChange={set} />

      <h2>번호·보존·설정</h2>
      <div className="field">
        <label htmlFor="num">문서 번호 규칙 ({"{form} {unit} {unitcode} {yyyy} {yy} {seq:4} {id:6}"})</label>
        <input id="num" value={def.numbering.pattern} disabled={!editable} onChange={(e) => set({ numbering: { ...def.numbering, pattern: e.target.value } })} />
        <button type="button" className="btn-secondary btn-sm" onClick={() => void showNumber()}>
          다음 번호 미리보기
        </button>
        {preview && <span className="hint">예: {preview}</span>}
      </div>
      <div className="field">
        <label htmlFor="ret">보존 연한</label>
        <select id="ret" value={def.retention_years} disabled={!editable} onChange={(e) => set({ retention_years: Number(e.target.value) })}>
          {[1, 3, 5, 10, 30, 0].map((y) => (
            <option key={y} value={y}>
              {y === 0 ? "영구" : `${y}년`}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label>
          <input
            type="checkbox"
            checked={!!def.legal_hold || ["PUR", "EXP", "ADJ"].includes(v.formCode)}
            disabled={!editable || ["PUR", "EXP", "ADJ"].includes(v.formCode)}
            onChange={(e) => set({ legal_hold: e.target.checked })}
          />{" "}
          법정 보존(폐기 불가)
        </label>
        <span className="hint">
          {["PUR", "EXP", "ADJ"].includes(v.formCode)
            ? "재무·회계 증빙과 얽힌 양식이라 항상 폐기할 수 없습니다."
            : "체크하면 보존 연한이 지나도 폐기 대상에 나타나지 않습니다. 새 버전을 게시한 뒤 결재가 끝나는 문서부터 적용됩니다."}
        </span>
      </div>
      <div className="field">
        <label htmlFor="le">기안자의 결재선 편집</label>
        <select id="le" value={def.settings.line_edit} disabled={!editable} onChange={(e) => setSettings({ line_edit: e.target.value as FormDefinition["settings"]["line_edit"] })}>
          {Object.entries(LINE_EDIT_LABEL).map(([k, l]) => (
            <option key={k} value={k}>
              {l}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label htmlFor="oa">합의 이견</label>
        <select id="oa" value={def.settings.objection_action} disabled={!editable} onChange={(e) => setSettings({ objection_action: e.target.value as FormDefinition["settings"]["objection_action"] })}>
          {Object.entries(OBJECTION_LABEL).map(([k, l]) => (
            <option key={k} value={k}>
              {l}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label htmlFor="af">결재선 금액으로 쓸 필드</label>
        <select id="af" value={def.settings.amount_field} disabled={!editable} onChange={(e) => setSettings({ amount_field: e.target.value })}>
          <option value="">없음</option>
          {def.fields
            .filter((f) => f.type === "amount")
            .map((f) => (
              <option key={f.key} value={f.key}>
                {f.label}
              </option>
            ))}
        </select>
      </div>
      <label>
        <input type="checkbox" checked={def.settings.dept_visible} disabled={!editable} onChange={(e) => setSettings({ dept_visible: e.target.checked })} /> 같은 부서 동료도 볼 수 있음
      </label>{" "}
      <label>
        <input type="checkbox" checked={def.settings.urgent_allowed} disabled={!editable} onChange={(e) => setSettings({ urgent_allowed: e.target.checked })} /> 긴급 표시 허용
      </label>

      {problems && <Notice tone="error">{problems}</Notice>}
      {editable && (
        <div className="form-row">
          <button type="button" className="btn-secondary" onClick={() => void save()}>
            초안 저장
          </button>
          <button type="button" onClick={() => void publish()}>
            게시
          </button>
        </div>
      )}
    </section>
  );
}
