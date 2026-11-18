//(업로드 문서)·erp(검색 화면 연결). 사내 매뉴얼·규정 원문 보기(B47 명세 8절): 검색 결과 출처 링크 `/knowledge/:id` 의 도착지다.
// 열람 범위 밖이거나 지워진 문서는 서버가 같은 404 를 주므로 화면도 같은 문구만 보인다(존재를 알리지 않는다).
import { useState } from "react";
import { useParams } from "react-router-dom";
import { ApiError, api, apiDownload } from "../api/client";
import { Badge, EmptyState, Notice, PageHead } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";

interface Section {
  locatorKind: string;
  locator: string;
  heading: string;
  text: string;
}
interface DocView {
  id: number;
  kind: string;
  kindLabel: string;
  title: string;
  version: number;
  uploadedAt: string;
  filename: string;
  sections: Section[];
}

function sectionTitle(s: Section): string {
  switch (s.locatorKind) {
    case "page":
      return `${s.locator}쪽`;
    case "clause":
      return s.heading ? `${s.locator} (${s.heading})` : s.locator;
    case "heading":
      return s.heading || s.locator;
    default:
      return s.locator;
  }
}

export function KnowledgeDocPage() {
  const { id } = useParams();
  const doc = useLoad(() => api.get<DocView>(`/api/knowledge/uploads/${encodeURIComponent(id ?? "")}`), [id]);
  const [err, setErr] = useState("");
  if (doc.error) {
    const gone = /찾을 수 없|not found|404/i.test(doc.error);
    return (
      <section>
        <PageHead title="사내 문서" />
        {gone ? <EmptyState>문서를 찾을 수 없습니다.</EmptyState> : <Notice tone="error">{doc.error}</Notice>}
      </section>
    );
  }
  const d = doc.data;
  if (!d) return <p>불러오는 중…</p>;
  return (
    <section>
      <PageHead title={d.title} />
      <p>
        <Badge tone="muted">{d.kindLabel}</Badge>
        {d.version > 1 && <> v{d.version}</>} · 올린 날 {new Date(d.uploadedAt).toLocaleDateString("ko-KR")}
      </p>
      {err && <Notice tone="error">{err}</Notice>}
      <p>
        <button
          type="button"
          className="btn-secondary"
          onClick={() => void apiDownload(`/api/knowledge/uploads/${d.id}/file`).catch((e) => setErr(e instanceof ApiError || e instanceof Error ? errorText(e) : "내려받지 못했습니다."))}
        >
          원본 내려받기
        </button>
      </p>
      {d.sections.length === 0 && <EmptyState>색인된 내용이 없습니다.</EmptyState>}
      {d.sections.map((s, i) => (
        <article key={i} className="card">
          {sectionTitle(s) && <h3>{sectionTitle(s)}</h3>}
          <p style={{ whiteSpace: "pre-wrap" }}>{s.text}</p>
        </article>
      ))}
    </section>
  );
}
