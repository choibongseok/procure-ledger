// 기안 시작: 지금 쓸 수 있는 양식을 분류별로 보여 준다. 구매 품의서는 기존 구매요청 작성 화면으로 간다.
import { Link } from "react-router-dom";
import { api } from "../api/client";
import { Breadcrumbs, EmptyState, Notice } from "../layout/ui";
import { useLoad } from "../purchase/useLoad";
import type { FormSummary } from "./types";
import { CATEGORY_LABEL } from "./types";

export function DocNew() {
  const { data, error, loading } = useLoad(() => api.get<FormSummary[]>("/api/forms"), []);
  const groups = new Map<string, FormSummary[]>();
  for (const f of data ?? []) groups.set(f.category, [...(groups.get(f.category) ?? []), f]);
  return (
    <section>
      <Breadcrumbs items={[{ label: "전자결재", to: "/docs" }, { label: "새 결재 문서" }]} />
      <h1>새 결재 문서</h1>
      <p>작성할 양식을 고르세요.</p>
      {error && <Notice tone="error">{error}</Notice>}
      {loading && <p>불러오는 중…</p>}
      {data && data.length === 0 && <EmptyState>지금 쓸 수 있는 양식이 없습니다. 관리자에게 문의하세요.</EmptyState>}
      {[...groups.entries()].map(([cat, list]) => (
        <div key={cat}>
          <h2>{CATEGORY_LABEL[cat] ?? cat}</h2>
          <ul>
            {list.map((f) => (
              <li key={f.code}>
                <Link to={f.kind === "purchase" ? "/purchase-requests/new" : `/docs/new/${f.code}`}>{f.name}</Link>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </section>
  );
}
