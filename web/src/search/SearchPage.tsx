// (web/src/search). 통합 검색(B39): GNB 검색 입력에서 Enter 로 `/search?q=` 에 온다.
// 구매요청·문서·사람 절을 권한 범위 안에서 보인다. 권한 없는 절은 그리지 않고 API 도 부르지 않는다(절 컴포넌트를 마운트하지 않는다).
import { useEffect, useState, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, EmptyState, Notice, PageHead } from "../layout/ui";
import type { PersonCard } from "../org/types";
import { PrStatusBadge } from "../purchase/badges";
import type { PurchaseRequest } from "../purchase/types";
import { errorText, useLoad } from "../purchase/useLoad";

/** 절마다 가져오는 최대 건수. 더 보려면 각 업무 화면의 검색을 쓴다. */
const LIMIT = 10;
const DOC_MIN = 2;

interface DocRow {
  id: number;
  no: string;
  title: string;
  formName: string;
  formKind: "purchase" | "general";
  requesterName: string;
  status: string;
}

function Section({
  name,
  loading,
  error,
  count,
  children,
}: {
  name: string;
  loading: boolean;
  error: string;
  count: number;
  children: ReactNode;
}) {
  return (
    <section aria-label={name} className="search-section">
      <h2>{name}</h2>
      {error ? (
        <Notice tone="error">{error}</Notice>
      ) : loading ? (
        <p>불러오는 중…</p>
      ) : count === 0 ? (
        <EmptyState>검색 결과가 없습니다</EmptyState>
      ) : (
        children
      )}
    </section>
  );
}

function PurchaseSection({ q }: { q: string }) {
  const { data, error, loading } = useLoad(
    () =>
      api.get<PurchaseRequest[]>(
        `/api/purchase-requests?q=${encodeURIComponent(q)}&limit=${LIMIT}`,
      ),
    [q],
  );
  const rows = data ?? [];
  return (
    <Section
      name="구매요청"
      loading={loading}
      error={error}
      count={rows.length}
    >
      <ul className="search-list">
        {rows.map((pr) => (
          <li key={pr.id}>
            <Link to={`/purchase-requests/${pr.id}`}>{pr.title}</Link>{" "}
            <span className="hint">{pr.no}</span>{" "}
            <PrStatusBadge status={pr.status} />
          </li>
        ))}
      </ul>
    </Section>
  );
}

function DocSection({ q }: { q: string }) {
  const short = q.length < DOC_MIN;
  const { data, error, loading } = useLoad(async () => {
    if (short) return [] as DocRow[];
    const r = await api.get<{ rows: DocRow[] }>(
      `/api/docs/search?q=${encodeURIComponent(q)}`,
    );
    // 구매 품의서는 구매요청 절에 나오므로 일반 문서만 보인다.
    return r.rows.filter((d) => d.formKind === "general").slice(0, LIMIT);
  }, [q, short]);
  const rows = data ?? [];
  if (short) {
    return (
      <section aria-label="문서" className="search-section">
        <h2>문서</h2>
        <p className="hint">
          문서는 검색어를 2자 이상 입력해야 찾을 수 있습니다.
        </p>
      </section>
    );
  }
  return (
    <Section name="문서" loading={loading} error={error} count={rows.length}>
      <ul className="search-list">
        {rows.map((d) => (
          <li key={d.id}>
            <Link to={`/docs/${d.id}`}>{d.title}</Link>{" "}
            <span className="hint">
              {d.no} · {d.formName} · {d.requesterName}
            </span>
          </li>
        ))}
      </ul>
    </Section>
  );
}

function PeopleSection({ q }: { q: string }) {
  const { data, error, loading } = useLoad(
    async () =>
      (
        await api.get<{ people: PersonCard[] }>(
          `/api/org/people?q=${encodeURIComponent(q)}`,
        )
      ).people.slice(0, LIMIT),
    [q],
  );
  const rows = data ?? [];
  return (
    <Section name="사람" loading={loading} error={error} count={rows.length}>
      <ul className="search-list">
        {rows.map((p) => (
          <li key={p.id}>
            <Link to={`/org?person=${p.id}`}>{p.name}</Link>{" "}
            <span className="hint">
              {[p.rank, p.primary?.fullPath ?? p.primary?.name]
                .filter(Boolean)
                .join(" · ")}
            </span>
          </li>
        ))}
      </ul>
    </Section>
  );
}

interface KnowledgeHit {
  kind: string;
  kindLabel: string;
  title: string;
  docNo: string;
  snippet: string;
  source: string;
  link: string;
}

interface KnowledgeAnswer {
  text: string;
  found: boolean;
  sources: { n: number; source: string; link: string; kindLabel: string }[];
}

const ANSWER_ERROR = "답변을 만들지 못했습니다. 검색 결과를 직접 확인하세요.";

/** 질문하기(B47 3단계): 열람 범위 안의 근거 조각으로만 답한다. 근거 없는 문장은 서버가 뺀다. */
function AnswerCard({ q }: { q: string }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [answer, setAnswer] = useState<KnowledgeAnswer | null>(null);
  useEffect(() => {
    setAnswer(null);
    setError("");
  }, [q]);
  async function ask() {
    setBusy(true);
    setError("");
    try {
      setAnswer(await api.post<KnowledgeAnswer>("/api/knowledge/answer", { question: q }));
    } catch (e) {
      const m = errorText(e);
      setError(/한도/.test(m) ? m : ANSWER_ERROR);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="answer-card" style={{ marginBottom: 16 }}>
      <button type="button" className="btn-secondary" disabled={busy} onClick={() => void ask()}>
        {busy ? "답변을 만드는 중…" : "질문하기"}
      </button>
      {error && <Notice tone="error">{error}</Notice>}
      {answer && (
        <div role="region" aria-label="답변" className="card" style={{ marginTop: 12 }}>
          <p>{answer.text}</p>
          {answer.found && (
            <>
              <ul className="search-list">
                {answer.sources.map((s) => (
                  <li key={s.n}>
                    [{s.n}] <Badge tone="muted">{s.kindLabel}</Badge> <Link to={s.link}>{s.source}</Link>
                  </li>
                ))}
              </ul>
              <p className="hint">근거 없는 내용은 답에서 뺐습니다.</p>
            </>
          )}
        </div>
      )}
    </div>
  );
}

const KNOWLEDGE_PAGE = 5;
const KNOWLEDGE_ERROR = "내용 검색을 불러오지 못했습니다. 잠시 뒤 다시 시도하세요.";

/** 내용 검색(B47): 완료된 결재 문서·첨부·매뉴얼·규정의 안의 글. 열람 범위는 서버가 문서마다 판정하고 결과에는 건수가 없다("더 보기"만 있다). */
function KnowledgeSection({ q }: { q: string }) {
  const short = [...q].length < DOC_MIN;
  const [hits, setHits] = useState<KnowledgeHit[]>([]);
  const [more, setMore] = useState(false);
  const [loading, setLoading] = useState(!short);
  const [error, setError] = useState("");
  async function fetchPage(offset: number) {
    const r = await api.get<{ hits?: KnowledgeHit[]; more?: boolean }>(
      `/api/knowledge/search?q=${encodeURIComponent(q)}&limit=${KNOWLEDGE_PAGE}&offset=${offset}`,
    );
    return { rows: r?.hits ?? [], more: r?.more === true };
  }
  useEffect(() => {
    if (short) return;
    let live = true;
    setLoading(true);
    setError("");
    fetchPage(0)
      .then((r) => {
        if (!live) return;
        setHits(r.rows);
        setMore(r.more);
      })
      .catch(() => live && setError(KNOWLEDGE_ERROR))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [q, short]);
  async function loadMore() {
    setLoading(true);
    try {
      const r = await fetchPage(hits.length);
      setHits((h) => [...h, ...r.rows]);
      setMore(r.more);
    } catch {
      setError(KNOWLEDGE_ERROR);
    } finally {
      setLoading(false);
    }
  }
  if (short) {
    return (
      <section aria-label="내용 검색" className="search-section">
        <h2>내용 검색</h2>
        <p className="hint">내용 검색은 2자 이상 입력하세요.</p>
      </section>
    );
  }
  return (
    <section aria-label="내용 검색" className="search-section">
      <h2>내용 검색</h2>
      <AnswerCard q={q} />
      {error ? (
        <Notice tone="error">{error}</Notice>
      ) : loading && hits.length === 0 ? (
        <p>불러오는 중…</p>
      ) : hits.length === 0 ? (
        <EmptyState>일치하는 내용을 찾지 못했습니다.</EmptyState>
      ) : (
        <>
          <ul className="search-list">
            {hits.map((h, i) => (
              <li key={`${h.link}-${i}`}>
                <Badge tone="muted">{h.kindLabel}</Badge> <strong>{h.source}</strong>
                <p className="hint">{h.snippet}</p>
                <Link to={h.link}>문서 열기</Link>
              </li>
            ))}
          </ul>
          {more && (
            <button type="button" className="btn-secondary" disabled={loading} onClick={() => void loadMore()}>
              더 보기
            </button>
          )}
        </>
      )}
      <p className="hint">첨부 중 글자를 읽지 못하는 파일(스캔본 등)은 검색되지 않을 수 있습니다.</p>
    </section>
  );
}

export function SearchPage() {
  const { can } = useAuth();
  const [params] = useSearchParams();
  const q = (params.get("q") ?? "").trim();
  const showPurchase = can("purchase:read");
  const showDocs = can("draft:read");
  const showPeople = can("org:read");
  const showKnowledge = can("knowledge:search");
  return (
    <div className="search-page">
      <PageHead title="검색" />
      {!showPurchase && !showDocs && !showPeople && !showKnowledge ? (
        <Notice tone="info">
          검색할 수 있는 항목이 없습니다. 권한이 필요하면 관리자에게 문의하세요.
        </Notice>
      ) : !q ? (
        <Notice tone="info">
          상단 검색창에 검색어를 입력하고 Enter 를 누르세요.
        </Notice>
      ) : (
        <>
          <p className="hint">
            “{q}” 검색 결과(절마다 최대 {LIMIT}건)
          </p>
          {showPurchase && <PurchaseSection q={q} />}
          {showDocs && <DocSection q={q} />}
          {showPeople && <PeopleSection q={q} />}
          {showKnowledge && <KnowledgeSection q={q} />}
        </>
      )}
    </div>
  );
}
