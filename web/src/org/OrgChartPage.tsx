// (web/src/org). 조직도: 조직 트리, 구성원 목록, 검색, 기준일, 사람 카드.
import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { ApiError, api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { ExportButtons } from "../layout/ExportButtons";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { OrgTree } from "./OrgTree";
import { HEAD_TITLE, STATUS_LABEL, dayText, todayKST, type EventRow, type PersonCard, type TreeNode } from "./types";

function titles(c: PersonCard): string {
  const t: string[] = [];
  for (const p of [c.primary, ...c.concurrent]) if (p.isHead) t.push(`${p.name} ${HEAD_TITLE[p.kind]}`);
  for (const p of c.acting) t.push(`${p.name} 직무대행`);
  return t.join(" · ");
}

function PersonRow({ c, onOpen }: { c: PersonCard; onOpen: (c: PersonCard) => void }) {
  return (
    <tr>
      <td className="nowrap">
        <button type="button" className="btn-secondary btn-sm" onClick={() => onOpen(c)}>
          {c.name}
        </button>
        {!c.active && (
          <>
            {" "}
            <Badge tone="muted">퇴사</Badge>
          </>
        )}
        {c.active && c.onLeave && (
          <>
            {" "}
            <Badge tone="warn">휴직 중</Badge>
          </>
        )}
      </td>
      <td className="nowrap">{c.rank}</td>
      <td>{titles(c) && <Badge tone="info">{titles(c)}</Badge>}</td>
      <td className="org-path">{c.primary.path}</td>
      <td>{c.concurrent.length > 0 ? c.concurrent.map((p) => p.name).join(", ") : "-"}</td>
      <td className="nowrap">{c.email}</td>
      <td className="nowrap">{c.phone}</td>
      <td className="nowrap">{c.extension}</td>
    </tr>
  );
}

export function OrgChartPage() {
  const { can } = useAuth();
  const today = todayKST();
  const [asOf, setAsOf] = useState(today);
  const [root, setRoot] = useState<TreeNode | null | undefined>(undefined);
  // root 가 어느 기준일의 트리인지. 기준일을 바꾼 직후 새 트리가 오기 전에는 구성원 조회를 하지 않는다(없던 조직에 404 를 내지 않으려는 것).
  const [treeDay, setTreeDay] = useState("");
  const [selected, setSelected] = useState<TreeNode | null>(null);
  const [members, setMembers] = useState<PersonCard[] | null>(null);
  const [q, setQ] = useState("");
  const [searched, setSearched] = useState("");
  const [includeLeft, setIncludeLeft] = useState(false);
  const [card, setCard] = useState<PersonCard | null>(null);
  const [history, setHistory] = useState<EventRow[] | null>(null);
  const [error, setError] = useState("");

  const loadTree = useCallback(async () => {
    setError("");
    try {
      const r = await api.get<{ root: TreeNode | null }>(`/api/org/tree?asOf=${asOf}`);
      setRoot(r.root);
      setTreeDay(asOf);
    } catch {
      setError("조직도를 불러오지 못했습니다. 잠시 후 다시 시도하세요.");
      setRoot(null);
    }
  }, [asOf]);

  useEffect(() => {
    void loadTree();
  }, [loadTree]);

  // 기준일을 바꿨을 때 선택해 둔 조직이 그날 없으면(조직이 아예 없는 날짜 포함) 안내 문구 하나만 보인다.
  const stale = useMemo(() => {
    if (!selected || root === undefined || treeDay !== asOf) return false;
    if (root === null) return true;
    const has = (n: TreeNode): boolean => n.id === selected.id || n.children.some(has);
    return !has(root);
  }, [root, selected, treeDay, asOf]);

  const loadMembers = useCallback(
    async (n: TreeNode) => {
      try {
        const p = new URLSearchParams({ asOf });
        if (includeLeft) p.set("includeLeft", "1");
        const r = await api.get<{ members: PersonCard[] }>(`/api/org/units/${n.id}/members?${p}`);
        setMembers(r.members);
        setSearched("");
      } catch (e) {
        // 그 기준일에 없던 조직이면 404 다. 오류가 아니라 stale 안내로 보이므로 목록만 비운다.
        if (e instanceof ApiError && e.status === 404) {
          setMembers(null);
          return;
        }
        setError("조직도를 불러오지 못했습니다. 잠시 후 다시 시도하세요.");
      }
    },
    [asOf, includeLeft],
  );

  useEffect(() => {
    if (selected && !stale && !searched && treeDay === asOf) void loadMembers(selected);
  }, [selected, stale, asOf, includeLeft, loadMembers, searched, treeDay]);

  async function search(e: FormEvent) {
    e.preventDefault();
    if (!q.trim()) {
      setSearched("");
      if (selected) void loadMembers(selected);
      return;
    }
    try {
      const p = new URLSearchParams({ q, asOf });
      if (includeLeft) p.set("includeLeft", "1");
      const r = await api.get<{ people: PersonCard[] }>(`/api/org/people?${p}`);
      setMembers(r.people);
      setSearched(q);
    } catch {
      setError("조직도를 불러오지 못했습니다. 잠시 후 다시 시도하세요.");
    }
  }

  async function openCard(c: PersonCard) {
    setHistory(null);
    try {
      const fresh = await api.get<PersonCard>(`/api/org/people/${c.id}?asOf=${asOf}`);
      setCard(fresh);
      if (can("org:history")) {
        const h = await api.get<{ events: EventRow[] }>(`/api/org/people/${c.id}/history`);
        setHistory(h.events);
      }
    } catch {
      setCard(null);
      setError("이 사람의 정보를 찾을 수 없습니다.");
    }
  }

  // 통합 검색(B39)의 사람 결과가 `/org?person=ID` 로 들어오면 그 사람 카드를 연다.
  const [urlParams] = useSearchParams();
  const personParam = urlParams.get("person");
  useEffect(() => {
    const id = Number(personParam);
    if (id > 0) void openCard({ id } as PersonCard);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [personParam]);

  const notToday = asOf !== today;
  return (
    <section>
      <h1>조직도</h1>
      <div className="form-row">
        <label>
          기준일 <input type="date" value={asOf} onChange={(e) => e.target.value && setAsOf(e.target.value)} />
        </label>
        {notToday && (
          <button type="button" className="btn-secondary" onClick={() => setAsOf(today)}>
            오늘로 돌아가기
          </button>
        )}
        <form onSubmit={search} className="form-row" aria-label="조직도 검색">
          <label>
            검색 <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="이름·조직·직책" />
          </label>
          <button type="submit">찾기</button>
        </form>
        {can("org:history") && (
          <label>
            <input type="checkbox" checked={includeLeft} onChange={(e) => setIncludeLeft(e.target.checked)} /> 퇴사자 포함
          </label>
        )}
      </div>
      {notToday && (
        <Notice tone="info">
          {dayText(asOf)} 기준 조직도입니다({asOf < today ? "과거" : "예약된 발령 반영"}).
        </Notice>
      )}
      {error && <Notice tone="error">{error}</Notice>}
      {stale && <Notice tone="warn">이 날짜에는 없던 조직입니다. 조직을 다시 선택하세요.</Notice>}
      {root === null && !error && !stale && (
        <EmptyState>
          {asOf !== today ? "이 날짜에는 등록된 조직이 없습니다." : "등록된 조직이 없습니다. 관리자에게 조직 등록을 요청하세요."}
        </EmptyState>
      )}
      {root && (
        <div className="org-layout">
          <div className="card org-tree-box">
            <OrgTree root={root} selectedId={selected?.id ?? null} onSelect={setSelected} />
          </div>
          <div className="org-main">
            {!selected && !searched && <EmptyState>조직을 선택하면 구성원이 보입니다.</EmptyState>}
            {(searched || (selected && !stale)) && members && (
              <>
                <h2>{searched ? `"${searched}" 검색 결과` : `${selected?.name} 구성원`}</h2>
                <ExportButtons
                  path="/api/org/people/export"
                  params={{ asOf, includeLeft, ...(searched ? { q: searched } : { unitId: selected?.id }) }}
                  disabled={members.length === 0}
                  sensitive
                />
                {members.length === 0 ? (
                  <EmptyState>
                    {searched ? "검색 결과가 없습니다. 이름·조직·직책으로 다시 찾아보세요." : "이 조직에는 구성원이 없습니다."}
                  </EmptyState>
                ) : (
                  <TableWrap>
                    <table className="org-members">
                      <thead>
                        <tr>
                          <th>이름</th>
                          <th>직위</th>
                          <th>직책</th>
                          <th>주소속</th>
                          <th>겸직</th>
                          <th>이메일</th>
                          <th>휴대전화</th>
                          <th>내선</th>
                        </tr>
                      </thead>
                      <tbody>
                        {members.map((c) => (
                          <PersonRow key={c.id} c={c} onOpen={(p) => void openCard(p)} />
                        ))}
                      </tbody>
                    </table>
                  </TableWrap>
                )}
              </>
            )}
            {card && (
              <section className="card" aria-label="사람 카드">
                <h2>
                  {card.name} {!card.active && <Badge tone="muted">퇴사</Badge>}
                  {card.active && card.onLeave && <Badge tone="warn">휴직 중</Badge>}
                </h2>
                <dl>
                  <dt>직위</dt>
                  <dd>{card.rank}</dd>
                  <dt>사번</dt>
                  <dd>{card.employeeNo}</dd>
                  <dt>소속</dt>
                  <dd>{card.primary.fullPath || card.primary.path}</dd>
                  {card.concurrent.length > 0 && (
                    <>
                      <dt>겸직</dt>
                      <dd>{card.concurrent.map((p) => p.fullPath || p.path).join(", ")}</dd>
                    </>
                  )}
                  {titles(card) && (
                    <>
                      <dt>직책</dt>
                      <dd>{titles(card)}</dd>
                    </>
                  )}
                  {card.acting.length > 0 && (
                    <>
                      <dt>직무대행</dt>
                      <dd>{card.acting.map((p) => p.fullPath || p.path).join(", ")}</dd>
                    </>
                  )}
                  <dt>이메일</dt>
                  <dd>{card.email}</dd>
                  <dt>휴대전화</dt>
                  <dd>{card.phone || "-"}</dd>
                  <dt>내선</dt>
                  <dd>{card.extension || "-"}</dd>
                </dl>
                {can("org:manage") && (
                  <p>
                    <Link to={`/org/appointments?user=${card.id}`}>이 사람 발령 등록</Link>
                  </p>
                )}
                {history && (
                  <>
                    <h3>발령 이력</h3>
                    {history.length === 0 ? (
                      <EmptyState>등록된 발령이 없습니다.</EmptyState>
                    ) : (
                      <TableWrap>
                        <table>
                          <thead>
                            <tr>
                              <th>효력일</th>
                              <th>종류</th>
                              <th>내용</th>
                              <th>사유</th>
                              <th>상태</th>
                            </tr>
                          </thead>
                          <tbody>
                            {history.map((h) => (
                              <tr key={h.id}>
                                <td className="nowrap">{dayText(h.effectiveDate)}</td>
                                <td className="nowrap">{h.kindLabel}</td>
                                <td>{h.summary}</td>
                                <td>{h.reason || "-"}</td>
                                <td className="nowrap">
                                  <Badge tone={h.status === "cancelled" ? "muted" : h.status === "scheduled" ? "warn" : "ok"}>
                                    {STATUS_LABEL[h.status]}
                                  </Badge>
                                </td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </TableWrap>
                    )}
                  </>
                )}
                <button type="button" className="btn-secondary" onClick={() => setCard(null)}>
                  카드 닫기
                </button>
              </section>
            )}
          </div>
        </div>
      )}
    </section>
  );
}
