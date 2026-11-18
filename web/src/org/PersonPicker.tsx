// (web/src/org). 이름으로 사람을 찾아 고르는 입력. 조직 사람 검색 API 를 쓴다.
import { useState } from "react";
import { api } from "../api/client";
import type { PersonCard } from "./types";

interface Props {
  label: string;
  value: PersonCard | null;
  onChange: (p: PersonCard | null) => void;
  /** 이 조직과 하위 조직 사람만 찾는다. */
  unitId?: number;
  includeLeft?: boolean;
}

export function PersonPicker({ label, value, onChange, unitId, includeLeft }: Props) {
  const [q, setQ] = useState("");
  const [results, setResults] = useState<PersonCard[] | null>(null);
  const [error, setError] = useState("");

  async function search() {
    setError("");
    try {
      const p = new URLSearchParams({ q, limit: "20" });
      if (unitId) p.set("unitId", String(unitId));
      if (includeLeft) p.set("includeLeft", "1");
      const r = await api.get<{ people: PersonCard[] }>(`/api/org/people?${p}`);
      setResults(r.people);
    } catch {
      setError("검색하지 못했습니다. 잠시 후 다시 시도하세요.");
    }
  }

  if (value) {
    return (
      <div className="field">
        <label>{label}</label>
        <div>
          <strong>{value.name}</strong> <span className="hint">{value.primary.path}</span>{" "}
          <button type="button" className="btn-secondary btn-sm" onClick={() => onChange(null)}>
            다시 고르기
          </button>
        </div>
      </div>
    );
  }
  return (
    <div className="field">
      <label htmlFor={`pp-${label}`}>{label}</label>
      <div className="form-row">
        <input id={`pp-${label}`} value={q} onChange={(e) => setQ(e.target.value)} placeholder="이름·조직·직책으로 찾기" />
        <button type="button" className="btn-secondary" onClick={() => void search()}>
          찾기
        </button>
      </div>
      {error && <span className="hint">{error}</span>}
      {results && results.length === 0 && <span className="hint">검색 결과가 없습니다. 이름·조직·직책으로 다시 찾아보세요.</span>}
      {results && results.length > 0 && (
        <ul className="pick-list">
          {results.map((p) => (
            <li key={p.id}>
              <button
                type="button"
                className="btn-secondary btn-sm"
                onClick={() => {
                  onChange(p);
                  setResults(null);
                }}
              >
                {p.name}
              </button>{" "}
              <span className="hint">
                {p.rank} · {p.primary.path}
                {!p.active ? " · 퇴사" : ""}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
