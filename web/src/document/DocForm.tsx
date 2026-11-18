// 양식 문서 작성·수정 화면: 양식 필드 폼, 결재선 편집기, 임시저장, 저장 후 상신.
import { useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { ApiError, api } from "../api/client";
import { Breadcrumbs, Notice } from "../layout/ui";
import type { PurchaseRequest } from "../purchase/types";
import { businessMessage, errorText } from "../purchase/useLoad";
import { FieldInput, type FieldValues } from "./FieldInputs";
import { LineEditor, type LineReady } from "./LineEditor";
import type { FormDetail, LineEdit } from "./types";

/** 서버가 400 으로 돌려준 필드 오류({"error","fieldErrors"})를 읽는다. 아니면 일반 문구. */
function parseFieldErrors(e: unknown): { message: string; byKey: Record<string, string> } {
  if (e instanceof ApiError) {
    try {
      const j = JSON.parse(e.message) as { error?: string; fieldErrors?: Record<string, string> };
      if (j.fieldErrors) return { message: businessMessage(Object.values(j.fieldErrors)[0] ?? j.error ?? ""), byKey: j.fieldErrors };
    } catch {
      /* 일반 문구 */
    }
  }
  return { message: errorText(e), byKey: {} };
}

function amountFrom(def: FormDetail["definition"], fields: FieldValues): number {
  const key = def.settings.amount_field;
  const v = key ? fields[key] : 0;
  return typeof v === "number" ? v : 0;
}

export function DocForm() {
  const { code, id } = useParams();
  const nav = useNavigate();
  const [form, setForm] = useState<FormDetail | null>(null);
  const [loadError, setLoadError] = useState("");
  const [title, setTitle] = useState("");
  const [fields, setFields] = useState<FieldValues>({});
  const [urgent, setUrgent] = useState(false);
  const [edits, setEdits] = useState<LineEdit[]>([]);
  const [names, setNames] = useState<Record<number, string>>({});
  const [ready, setReady] = useState<LineReady>(null);
  const [editNotes, setEditNotes] = useState<string[]>([]);
  const [lineProblems, setLineProblems] = useState<string[]>([]);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [docId, setDocId] = useState<number | null>(id ? Number(id) : null);
  const [formCode, setFormCode] = useState(code ?? "");

  useEffect(() => {
    let stale = false;
    (async () => {
      try {
        if (id) {
          const d = await api.get<PurchaseRequest>(`/api/approval-docs/${id}`);
          if (stale) return;
          setFormCode(d.formCode ?? "");
          setTitle(d.title);
          setFields((d.fields as FieldValues) ?? {});
          setUrgent(!!d.urgent);
          setNames(Object.fromEntries((d.attachments ?? []).map((a) => [a.id, a.filename])));
          setEdits((d.draftEdits ?? []).map((e) => ({ role: e.role, targetType: e.targetType, targetId: e.targetId, group: e.group, name: e.name }) as LineEdit));
          setEditNotes(d.editNotes ?? []);
          const f = await api.get<FormDetail>(`/api/forms/${d.formCode}`);
          if (!stale) setForm({ ...f, definition: { ...f.definition, fields: d.formFields && d.formFields.length ? d.formFields : f.definition.fields } });
        } else {
          const f = await api.get<FormDetail>(`/api/forms/${code}`);
          if (!stale) setForm(f);
        }
      } catch (e) {
        if (!stale) setLoadError(errorText(e));
      }
    })();
    return () => {
      stale = true;
    };
  }, [code, id]);

  const amount = useMemo(() => (form ? amountFrom(form.definition, fields) : 0), [form, fields]);

  // 표의 합계를 금액 필드에 채운다(지출 결의서처럼 내역 합계와 금액이 같아야 하는 양식).
  useEffect(() => {
    if (!form) return;
    const key = form.definition.settings.amount_field;
    if (!key) return;
    const table = form.definition.fields.find((f) => f.type === "table" && f.columns?.some((c) => c.type === "amount"));
    if (!table) return;
    const col = table.columns!.find((c) => c.type === "amount")!;
    const rows = Array.isArray(fields[table.key]) ? (fields[table.key] as Record<string, unknown>[]) : [];
    const sum = rows.reduce((a, r) => a + (typeof r[col.key] === "number" ? (r[col.key] as number) : 0), 0);
    if (sum > 0 && fields[key] !== sum) setFields((f) => ({ ...f, [key]: sum }));
  }, [form, fields]);

  if (loadError) return <Notice tone="error">{loadError}</Notice>;
  if (!form) return <p>불러오는 중…</p>;
  const def = form.definition;

  const body = () => ({
    formCode: form.code,
    title: title.trim(),
    fields,
    urgent,
    edits: edits.map((e) => ({ role: e.role, targetType: e.targetType, targetId: e.targetId, group: e.group })),
  });

  async function save(): Promise<number | null> {
    setError("");
    setErrors({});
    try {
      const doc = docId ? await api.put<PurchaseRequest>(`/api/approval-docs/${docId}`, body()) : await api.post<PurchaseRequest>("/api/approval-docs", body());
      setDocId(doc.id);
      return doc.id;
    } catch (e) {
      const p = parseFieldErrors(e);
      setError(p.message);
      setErrors(p.byKey);
      return null;
    }
  }

  async function saveOnly() {
    setBusy(true);
    const did = await save();
    setBusy(false);
    if (did) nav(`/docs/${did}`);
  }

  async function saveAndSubmit() {
    setBusy(true);
    const did = await save();
    if (did) {
      try {
        await api.post(`/api/approval-docs/${did}/submit`);
        nav(`/docs/${did}`);
        return;
      } catch (e) {
        const p = parseFieldErrors(e);
        setError(p.message);
        setErrors(p.byKey);
        setDocId(did); // 저장은 됐으므로 이어서 고친다
      }
    }
    setBusy(false);
  }

  return (
    <section>
      <Breadcrumbs items={[{ label: "전자결재", to: "/docs" }, { label: `${form.name} 작성` }]} />
      <h1>{form.name} 작성</h1>
      <div className="form-grid">
        <div>
          <div className="field">
            <label htmlFor="doc-title">제목 *</label>
            <input id="doc-title" value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} />
          </div>
          {def.fields.map((f) => (
            <FieldInput
              key={f.key}
              def={f}
              value={fields[f.key]}
              error={errors[f.key] ?? Object.entries(errors).find(([k]) => k.startsWith(`${f.key}[`))?.[1]}
              onChange={(v) => setFields((cur) => ({ ...cur, [f.key]: v }))}
              attachNames={names}
              onAttachName={(n, name) => setNames((cur) => ({ ...cur, [n]: name }))}
            />
          ))}
          {def.settings.urgent_allowed && (
            <div className="field">
              <label>
                <input type="checkbox" checked={urgent} onChange={(e) => setUrgent(e.target.checked)} /> 긴급 문서
              </label>
            </div>
          )}
          {editNotes.length > 0 && <Notice tone="warn">{editNotes.join(" ")}</Notice>}
          {error && !lineProblems.some((p) => error.includes(p)) && <Notice tone="error">{error}</Notice>}
          <div className="form-row">
            <button type="button" className="btn-secondary" disabled={busy || title.trim() === ""} onClick={() => void saveOnly()}>
              임시저장
            </button>
            <button type="button" disabled={busy || title.trim() === "" || ready === false} onClick={() => void saveAndSubmit()}>
              저장 후 상신
            </button>
          </div>
        </div>
        <LineEditor formCode={form.code} def={def} fields={fields} amount={amount} edits={edits} onEdits={setEdits} onReady={(r, probs) => { setReady(r); setLineProblems(probs ?? []); }} />
      </div>
      <input type="hidden" value={formCode} readOnly />
    </section>
  );
}
