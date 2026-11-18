// 구매요청 작성 화면의 "관련 규정" 영역. 금액·품목이 바뀌면 그에 맞는 사내 규정 조항을 보여 준다.
// 열람 범위 밖 문서는 서버가 결과에서 뺀다(화면은 건수나 "더 있음"을 알 수 없다). 검색 권한이 없으면 이 컴포넌트를 쓰는 쪽이 그리지 않으므로 API 도 부르지 않는다.
import { useEffect, useState } from "react";
import { api } from "../api/client";
import { Notice } from "../layout/ui";

/** 서버 응답의 조항 한 건. 점수·내부 식별자는 화면에 쓰지 않는다. */
export interface PolicyClause {
  docTitle: string;
  clauseNo: string;
  heading: string;
  text: string;
  source: string;
  /** "tier"(금액 구간 결재선) / "item"(품목 관련). 서버가 구분하지 않으면 빈 값. */
  reason: string;
}

type Raw = Record<string, unknown>;
const pick = (r: Raw, ...keys: string[]): string => {
  for (const k of keys) if (typeof r[k] === "string") return r[k] as string;
  return "";
};

/** 응답 키가 snake_case 든 camelCase 든 같은 모양으로 맞춘다. */
export function toClauses(body: unknown): PolicyClause[] {
  const hits = (body as { hits?: Raw[] } | null)?.hits;
  if (!Array.isArray(hits)) return [];
  return hits.map((h) => ({
    docTitle: pick(h, "doc_title", "docTitle", "DocTitle"),
    clauseNo: pick(h, "clause_no", "clauseNo", "ClauseNo"),
    heading: pick(h, "heading", "Heading"),
    text: pick(h, "text", "Text"),
    source: pick(h, "source", "Source"),
    reason: pick(h, "reason", "Reason"),
  }));
}

const DEBOUNCE_MS = 400;

export function PolicyHints({ amount, items }: { amount: number; items: string[] }) {
  const [clauses, setClauses] = useState<PolicyClause[]>([]);
  const [error, setError] = useState("");
  const key = items.join(",");
  const hasInput = amount > 0 || items.length > 0;

  useEffect(() => {
    if (!hasInput) {
      setClauses([]);
      setError("");
      return;
    }
    let stale = false;
    const timer = setTimeout(() => {
      const qs = new URLSearchParams({ amount: String(Math.max(0, Math.round(amount))), items: key });
      api
        .get<unknown>(`/api/policies/related?${qs.toString()}`)
        .then((body) => {
          if (stale) return;
          setClauses(toClauses(body));
          setError("");
        })
        .catch(() => {
          if (stale) return;
          setClauses([]);
          setError("관련 규정을 불러오지 못했습니다. 작성은 계속할 수 있습니다.");
        });
    }, DEBOUNCE_MS);
    return () => {
      stale = true;
      clearTimeout(timer);
    };
  }, [amount, key, hasInput]);

  // reason 이 하나도 없는 응답은 한 묶음으로 보인다. 품목 묶음이 비면 그 제목은 그리지 않는다.
  const grouped = clauses.some((c) => c.reason === "tier" || c.reason === "item");
  const item = clauses.filter((c) => c.reason === "item");
  const tier = grouped ? clauses.filter((c) => c.reason !== "item") : clauses;

  return (
    <aside className="card" aria-label="관련 규정">
      <h2>관련 규정</h2>
      {!hasInput && <p>금액이나 품목을 입력하면 관련 규정이 여기에 나타납니다.</p>}
      {error && <Notice tone="error">{error}</Notice>}
      {hasInput && !error && clauses.length === 0 && <p>관련 규정이 없습니다.</p>}
      {tier.length > 0 && <ClauseGroup title={grouped ? "이 금액의 결재선" : ""} list={tier} />}
      {item.length > 0 && <ClauseGroup title="품목 관련 참고" list={item} />}
    </aside>
  );
}

function ClauseGroup({ title, list }: { title: string; list: PolicyClause[] }) {
  return (
    <section>
      {title && <h3>{title}</h3>}
      <ul>
        {list.map((c, i) => (
          <li key={`${c.source}-${i}`}>
            <strong>
              {c.docTitle} {c.clauseNo}
              {c.heading ? ` ${c.heading}` : ""}
            </strong>
            <p>{c.text}</p>
            <small>출처: {c.source}</small>
          </li>
        ))}
      </ul>
    </section>
  );
}
