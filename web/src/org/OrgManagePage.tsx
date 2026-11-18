// (web/src/org). 조직 관리(admin): 조직 변경, 직위 표, 정합성 점검.
import { useCallback, useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { api } from "../api/client";
import { Badge, EmptyState, Notice, TableWrap, useToast } from "../layout/ui";
import { CommandForm } from "./CommandForm";
import { OrgTree } from "./OrgTree";
import { OrphansPanel } from "./OrphansPanel";
import {
  KIND_LABEL,
  HEAD_TITLE,
  STATUS_LABEL,
  dayText,
  readError,
  type ConsistencyLog,
  type EventRow,
  type Issue,
  type Rank,
  type TreeNode,
} from "./types";

type Tab = "units" | "ranks" | "check";

function RanksTab({ onChanged }: { onChanged: () => void }) {
  const [ranks, setRanks] = useState<Rank[] | null>(null);
  const [name, setName] = useState("");
  const toast = useToast();
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      setRanks((await api.get<{ ranks: Rank[] }>("/api/org/ranks")).ranks);
    } catch {
      setError("직위 표를 불러오지 못했습니다.");
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  async function save(next: Rank[]) {
    setError("");
    try {
      const r = await api.put<{ ranks: Rank[] }>("/api/org/ranks", { ranks: next });
      setRanks(r.ranks);
      toast("직위 표를 저장했습니다.");
      onChanged();
    } catch (e) {
      const m = readError(e, "저장하지 못했습니다. 입력한 내용을 확인하세요.");
      setError(typeof m === "string" ? m : m.message);
    }
  }

  if (!ranks) return error ? <Notice tone="error">{error}</Notice> : <p className="hint">불러오는 중…</p>;
  return (
    <div>
      {error && <Notice tone="error">{error}</Notice>}
      {ranks.length === 0 ? (
        <EmptyState>등록된 직위가 없습니다.</EmptyState>
      ) : (
        <TableWrap>
          <table>
            <thead>
              <tr>
                <th>순서</th>
                <th>직위</th>
                <th>재직</th>
                <th>상태</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {ranks.map((r, i) => (
                <tr key={r.id}>
                  <td className="num">{r.sort}</td>
                  <td>{r.name}</td>
                  <td className="num">{r.inUse}명</td>
                  <td>
                    <Badge tone={r.active ? "ok" : "muted"}>{r.active ? "사용" : "중지"}</Badge>
                  </td>
                  <td className="nowrap">
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      disabled={i === 0}
                      onClick={() =>
                        void save([
                          { ...r, sort: ranks[i - 1].sort },
                          { ...ranks[i - 1], sort: r.sort },
                        ])
                      }
                    >
                      위로
                    </button>{" "}
                    <button type="button" className="btn-secondary btn-sm" onClick={() => void save([{ ...r, active: !r.active }])}>
                      {r.active ? "사용 중지" : "다시 사용"}
                    </button>{" "}
                    <button
                      type="button"
                      className="btn-secondary btn-sm"
                      onClick={() => {
                        const n = window.prompt("새 직위 이름", r.name);
                        if (n && n.trim()) void save([{ ...r, name: n.trim() }]);
                      }}
                    >
                      이름 변경
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
      <form
        className="form-row"
        onSubmit={(e) => {
          e.preventDefault();
          if (!name.trim()) return;
          void save([{ id: 0, name: name.trim(), sort: (ranks[ranks.length - 1]?.sort ?? 0) + 10, active: true, inUse: 0 }]);
          setName("");
        }}
      >
        <label>
          새 직위 <input value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <button type="submit">추가</button>
      </form>
    </div>
  );
}

interface ActingRow {
  userId: number;
  name: string;
  from: string;
  to: string;
}

/** 조직 상세의 직무대행 목록과 변경 이력(명세 8.2). 조직 트리가 다시 불러와질 때마다 새로 읽는다. */
function UnitExtras({ unit, version }: { unit: TreeNode; version: unknown }) {
  const [acting, setActing] = useState<ActingRow[] | null>(null);
  const [history, setHistory] = useState<EventRow[] | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    setFailed(false);
    Promise.all([
      api.get<{ acting: ActingRow[] }>(`/api/org/units/${unit.id}`),
      api.get<{ events: EventRow[] }>(`/api/org/events?unitId=${unit.id}&limit=20`),
    ])
      .then(([u, e]) => {
        setActing(u.acting ?? []);
        setHistory(e.events);
      })
      .catch(() => setFailed(true));
  }, [unit.id, version]);
  if (failed) return <Notice tone="error">조직 상세를 불러오지 못했습니다. 잠시 후 다시 시도하세요.</Notice>;
  if (!acting || !history) return <p className="hint">불러오는 중…</p>;
  return (
    <>
      <h3>직무대행</h3>
      {acting.length === 0 ? (
        <p className="hint">진행 중이거나 예정된 직무대행이 없습니다.</p>
      ) : (
        <ul>
          {acting.map((a) => (
            <li key={`${a.userId}-${a.from}`}>
              {a.name}{" "}
              <span className="hint">
                {dayText(a.from)}~{dayText(a.to)}
              </span>
            </li>
          ))}
        </ul>
      )}
      <h3>변경 이력</h3>
      {history.length === 0 ? (
        <EmptyState>이 조직의 변경 이력이 없습니다.</EmptyState>
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
  );
}

function CheckTab() {
  return (
    <div>
      <OrphansPanel />
      <h2>조직 정합성</h2>
      <ConsistencyPart />
    </div>
  );
}

function ConsistencyPart() {
  const [issues, setIssues] = useState<Issue[] | null>(null);
  const [log, setLog] = useState<ConsistencyLog | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api
      .get<{ issues: Issue[]; log?: ConsistencyLog }>("/api/org/consistency")
      .then((r) => {
        setIssues(r.issues);
        setLog(r.log ?? null);
      })
      .catch(() => setError("점검 목록을 불러오지 못했습니다."));
  }, []);
  if (error) return <Notice tone="error">{error}</Notice>;
  if (!issues) return <p className="hint">불러오는 중…</p>;
  const logLine =
    log &&
    (log.broken ? (
      <Notice tone="error">발령 이력이 규칙에 맞지 않습니다: {log.broken} 관리자에게 알리세요.</Notice>
    ) : log.ok ? (
      <Notice tone="ok">발령 이력과 현재 조직이 일치합니다.</Notice>
    ) : (
      <Notice tone="error">발령 이력과 현재 조직이 맞지 않는 구간이 {log.mismatches}건 있습니다. 관리자에게 알리세요.</Notice>
    ));
  if (issues.length === 0)
    return (
      <div>
        {logLine}
        <EmptyState>점검할 항목이 없습니다.</EmptyState>
      </div>
    );
  const groups: [Issue["kind"], string][] = [
    ["vacant_auto", "조직장이 자동으로 공석이 된 조직"],
    ["vacant_open", "공석인데 직무대행도 없는 조직"],
    ["head_no_approve", "조직장(직무대행 포함)인데 결재 권한 역할이 없는 사람"],
    ["unregistered", "조직에 등록되지 않은 사용자"],
  ];
  return (
    <div>
      {logLine}
      {groups.map(([k, title]) => {
        const list = issues.filter((i) => i.kind === k);
        if (list.length === 0) return null;
        return (
          <section key={k}>
            <h3>{title}</h3>
            <ul>
              {list.map((i) => (
                <li key={`${k}-${i.unitId}-${i.userId}`}>
                  {i.name} <span className="hint">{i.detail}</span>
                </li>
              ))}
            </ul>
          </section>
        );
      })}
    </div>
  );
}

const UNIT_ACTIONS: [string, string][] = [
  ["unit_create", "하위 조직 신설"],
  ["unit_rename", "이름 변경"],
  ["unit_move", "이동"],
  ["unit_sort", "순서 변경"],
  ["head_set", "조직장 보임"],
  ["head_clear", "조직장 해임"],
  ["acting_set", "직무대행 등록"],
  ["unit_close", "폐지"],
  ["unit_reopen", "재개"],
];

export function OrgManagePage() {
  const [params] = useSearchParams();
  const [tab, setTab] = useState<Tab>(params.get("tab") === "check" ? "check" : "units");
  const [root, setRoot] = useState<TreeNode | null | undefined>(undefined);
  const [ranks, setRanks] = useState<Rank[]>([]);
  const [selected, setSelected] = useState<TreeNode | null>(null);
  const [action, setAction] = useState("");
  const toast = useToast();
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    try {
      const [t, r] = await Promise.all([api.get<{ root: TreeNode | null }>("/api/org/tree"), api.get<{ ranks: Rank[] }>("/api/org/ranks")]);
      setRoot(t.root);
      setRanks(r.ranks);
      setSelected((cur) => {
        if (!cur || !t.root) return t.root ? cur : null;
        const find = (n: TreeNode): TreeNode | null => (n.id === cur.id ? n : n.children.map(find).find(Boolean) || null);
        return find(t.root);
      });
    } catch {
      setError("조직도를 불러오지 못했습니다. 잠시 후 다시 시도하세요.");
      setRoot(null);
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  const actions = selected
    ? UNIT_ACTIONS.filter(([k]) => {
        if (selected.kind === "company" && ["unit_move", "unit_close", "unit_reopen"].includes(k)) return false;
        if (selected.closed) return k === "unit_reopen";
        return k !== "unit_reopen";
      })
    : [];

  return (
    <section>
      <h1>조직 관리</h1>
      <div className="form-row" role="tablist">
        {(
          [
            ["units", "조직"],
            ["ranks", "직위"],
            ["check", "점검"],
          ] as [Tab, string][]
        ).map(([k, label]) => (
          <button
            key={k}
            type="button"
            role="tab"
            aria-selected={tab === k}
            className="btn-secondary"
            aria-pressed={tab === k}
            onClick={() => setTab(k)}
          >
            {label}
          </button>
        ))}
      </div>
      {error && <Notice tone="error">{error}</Notice>}
      {tab === "ranks" && <RanksTab onChanged={() => void load()} />}
      {tab === "check" && <CheckTab />}
      {tab === "units" && (
        <>
          {root === null && !error && <EmptyState>등록된 조직이 없습니다. 관리자에게 조직 등록을 요청하세요.</EmptyState>}
          {root && (
            <div className="org-layout">
              <div className="card org-tree-box">
                <OrgTree
                  root={root}
                  selectedId={selected?.id ?? null}
                  onSelect={(n) => {
                    setSelected(n);
                    setAction("");
                  }}
                />
              </div>
              <div className="org-main">
                {!selected && <EmptyState>조직을 선택하세요.</EmptyState>}
                {selected && (
                  <section className="card" aria-label="조직 상세">
                    <h2>
                      {selected.name} {selected.closed && <Badge tone="muted">폐지</Badge>}
                    </h2>
                    <dl>
                      <dt>종류</dt>
                      <dd>{KIND_LABEL[selected.kind]}</dd>
                      <dt>조직 코드</dt>
                      <dd>{selected.code}</dd>
                      <dt>{HEAD_TITLE[selected.kind]}</dt>
                      <dd>{selected.headName ? `${selected.headName}${selected.headActing ? "(직무대행)" : ""}` : "공석"}</dd>
                      <dt>인원</dt>
                      <dd>
                        직속 {selected.direct}명 / 전체 {selected.total}명
                      </dd>
                    </dl>
                    <div className="form-row">
                      {actions.map(([k, label]) => (
                        <button
                          key={k}
                          type="button"
                          className={k === "unit_close" ? "btn-danger btn-sm" : "btn-secondary btn-sm"}
                          onClick={() => {
                            setAction(k);
                          }}
                        >
                          {label}
                        </button>
                      ))}
                    </div>
                    {action && (
                      <CommandForm
                        key={`${action}-${selected.id}`}
                        kind={action}
                        tree={root}
                        ranks={ranks}
                        unit={selected}
                        onDone={(m) => {
                          toast(m);
                          setAction("");
                          void load();
                        }}
                        onCancel={() => setAction("")}
                      />
                    )}
                    <UnitExtras unit={selected} version={root} />
                  </section>
                )}
              </div>
            </div>
          )}
        </>
      )}
    </section>
  );
}
