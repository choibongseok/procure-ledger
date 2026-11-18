// 양식 필드 정의대로 입력 칸을 그린다(텍스트·숫자·금액·날짜·선택·표·첨부·조직). 값은 JSON 으로 풀 수 있는 형태로 다룬다.
import { useRef, useState } from "react";
import { apiFetch } from "../api/client";
import { UnitSelect } from "./UnitSelect";
import { errorText } from "../purchase/useLoad";
import type { FieldDef } from "./types";

export type FieldValues = Record<string, unknown>;

interface Props {
  def: FieldDef;
  value: unknown;
  error?: string;
  onChange: (v: unknown) => void;
  /** 표 안 칸이면 id 충돌을 막는 접두. */
  idPrefix?: string;
  /** 첨부 파일 이름 표시용(번호 → 이름). */
  attachNames: Record<number, string>;
  onAttachName: (id: number, name: string) => void;
}

const won = (n: unknown) => (typeof n === "number" ? n.toLocaleString("ko-KR") : "");

export function FieldInput({ def, value, error, onChange, idPrefix = "f", attachNames, onAttachName }: Props) {
  const id = `${idPrefix}-${def.key}`;
  const label = (
    <label htmlFor={id}>
      {def.label}
      {def.required ? " *" : ""}
    </label>
  );
  const hint = def.help ? <span className="hint">{def.help}</span> : null;
  const err = error ? (
    <span className="hint" role="alert" style={{ color: "var(--bad, #b00020)" }}>
      {error}
    </span>
  ) : null;

  switch (def.type) {
    case "textarea":
      return (
        <div className="field">
          {label}
          <textarea id={id} rows={5} value={String(value ?? "")} maxLength={def.maxLength || 5000} onChange={(e) => onChange(e.target.value)} />
          {hint}
          {err}
        </div>
      );
    case "number":
    case "amount":
      return (
        <div className="field">
          {label}
          <input
            id={id}
            inputMode="numeric"
            value={typeof value === "number" ? String(value) : String(value ?? "")}
            onChange={(e) => {
              const t = e.target.value.replace(/[,\s]/g, "");
              if (t === "") return onChange("");
              const n = Number(t);
              onChange(Number.isNaN(n) ? t : n);
            }}
          />
          {def.type === "amount" && typeof value === "number" && <span className="hint">{won(value)}원</span>}
          {hint}
          {err}
        </div>
      );
    case "date":
      return (
        <div className="field">
          {label}
          <input id={id} type="date" lang="ko-KR" title="연-월-일 (예: 2026-11-11)" value={String(value ?? "")} onChange={(e) => onChange(e.target.value)} />
          <small className="muted">날짜 형식: 연-월-일</small>
          {hint}
          {err}
        </div>
      );
    case "select":
      return (
        <div className="field">
          {label}
          <select id={id} value={String(value ?? "")} onChange={(e) => onChange(e.target.value)}>
            <option value="">선택하세요</option>
            {(def.options ?? []).map((o) => (
              <option key={o} value={o}>
                {o}
              </option>
            ))}
          </select>
          {hint}
          {err}
        </div>
      );
    case "unit":
      return (
        <div className="field">
          {label}
          <UnitSelect id={id} value={typeof value === "number" ? value : 0} onChange={(n) => onChange(n || "")} />
          {hint}
          {err}
        </div>
      );
    case "table":
      return <TableInput def={def} value={value} error={error} onChange={onChange} idPrefix={idPrefix} attachNames={attachNames} onAttachName={onAttachName} />;
    case "attachment":
      return <AttachmentInput def={def} id={id} value={value} error={error} onChange={onChange} names={attachNames} onName={onAttachName} />;
    default:
      return (
        <div className="field">
          {label}
          <input id={id} value={String(value ?? "")} maxLength={def.maxLength || 200} onChange={(e) => onChange(e.target.value)} />
          {hint}
          {err}
        </div>
      );
  }
}

function TableInput({ def, value, error, onChange, idPrefix, attachNames, onAttachName }: Props) {
  const rows = Array.isArray(value) ? (value as Record<string, unknown>[]) : [];
  const max = def.maxRows || 100;
  const setRow = (i: number, key: string, v: unknown) => onChange(rows.map((r, j) => (j === i ? { ...r, [key]: v } : r)));
  const sum = (key: string) => rows.reduce((a, r) => a + (typeof r[key] === "number" ? (r[key] as number) : 0), 0);
  const amountCols = (def.columns ?? []).filter((c) => c.type === "amount");
  return (
    <div className="field">
      <label>
        {def.label}
        {def.required ? " *" : ""}
      </label>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th className="nowrap">번호</th>
              {(def.columns ?? []).map((c) => (
                <th key={c.key}>{c.label}</th>
              ))}
              <th />
            </tr>
          </thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={i}>
                <td className="nowrap">{i + 1}</td>
                {(def.columns ?? []).map((c) => (
                  <td key={c.key}>
                    <FieldInput
                      def={{ ...c, label: `${def.label} ${i + 1}행 ${c.label}` }}
                      value={r[c.key]}
                      onChange={(v) => setRow(i, c.key, v)}
                      idPrefix={`${idPrefix}-${def.key}-${i}`}
                      attachNames={attachNames}
                      onAttachName={onAttachName}
                    />
                  </td>
                ))}
                <td>
                  <button type="button" className="btn-secondary btn-sm" onClick={() => onChange(rows.filter((_, j) => j !== i))}>
                    {i + 1}행 삭제
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
          {amountCols.length > 0 && rows.length > 0 && (
            <tfoot>
              <tr>
                <td>합계</td>
                {(def.columns ?? []).map((c) => (
                  <td key={c.key} className="num">
                    {c.type === "amount" ? won(sum(c.key)) : ""}
                  </td>
                ))}
                <td />
              </tr>
            </tfoot>
          )}
        </table>
      </div>
      <button type="button" className="btn-secondary btn-sm" disabled={rows.length >= max} onClick={() => onChange([...rows, {}])}>
        행 추가
      </button>
      {error && (
        <span className="hint" role="alert">
          {error}
        </span>
      )}
    </div>
  );
}

function AttachmentInput({ def, id, value, error, onChange, names, onName }: { def: FieldDef; id: string; value: unknown; error?: string; onChange: (v: unknown) => void; names: Record<number, string>; onName: (id: number, name: string) => void }) {
  const ids = Array.isArray(value) ? (value as number[]) : [];
  const fileInput = useRef<HTMLInputElement>(null);
  const [msg, setMsg] = useState("");
  const [busy, setBusy] = useState(false);
  async function upload(files: FileList | null) {
    if (!files || files.length === 0) return;
    setBusy(true);
    setMsg("");
    try {
      const next = [...ids];
      for (const f of Array.from(files)) {
        const fd = new FormData();
        fd.append("file", f);
        const r = await apiFetch<{ id: number; filename: string }>("/api/approval-docs/attachments", { method: "POST", body: fd });
        onName(r.id, r.filename);
        next.push(r.id);
      }
      onChange(next);
    } catch (e) {
      setMsg(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="field">
      <span id={`${id}-label`}>
        {def.label}
        {def.required ? " *" : ""}
      </span>
      <div className="form-row">
        {/* 브라우저 기본 영어 문구 대신 한국어 버튼을 쓴다. 입력은 숨기고 버튼이 연다(키보드로 버튼을 눌러 선택). */}
        <input ref={fileInput} id={id} type="file" multiple hidden tabIndex={-1} aria-labelledby={`${id}-label`} disabled={busy} onChange={(e) => { void upload(e.target.files); e.target.value = ""; }} />
        <button type="button" className="btn-secondary" aria-describedby={`${id}-label`} disabled={busy} onClick={() => fileInput.current?.click()}>
          파일 선택
        </button>
        <span aria-live="polite" data-testid={`${id}-files`}>
          {ids.length > 0 ? `선택한 파일: ${names[ids[ids.length - 1]] ?? `첨부 ${ids[ids.length - 1]}`}${ids.length > 1 ? ` 외 ${ids.length - 1}개` : ""}` : "선택한 파일이 없습니다"}
        </span>
      </div>
      <span className="hint">파일당 10MB, 문서당 20개까지. pdf·이미지·문서 파일을 올릴 수 있습니다.</span>
      {ids.length > 0 && (
        <ul>
          {ids.map((n) => (
            <li key={n}>
              {names[n] ?? `첨부 ${n}`}{" "}
              <button type="button" className="btn-secondary btn-sm" onClick={() => onChange(ids.filter((x) => x !== n))}>
                {names[n] ?? `첨부 ${n}`} 빼기
              </button>
            </li>
          ))}
        </ul>
      )}
      {msg && (
        <span className="hint" role="alert">
          {msg}
        </span>
      )}
      {error && (
        <span className="hint" role="alert">
          {error}
        </span>
      )}
    </div>
  );
}
