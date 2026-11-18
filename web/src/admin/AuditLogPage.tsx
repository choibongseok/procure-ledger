// 감사 로그 조회와 변조 검증 화면.
import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { PermissionGate } from "../auth/PermissionGate";
import { ExportButtons } from "../layout/ExportButtons";
import { Badge, EmptyState, Notice, TableWrap, type Tone } from "../layout/ui";
import { CHANNEL_LABELS, RESULT_LABELS, actionLabel, actorWithTokenText, channelText, detailText, diffItemText, diffParts, targetIdText } from "./auditLabels";
import { TARGET_TYPE_LABELS } from "./targetTypes";

export interface AuditRecord {
  seq: number;
  at: string;
  actorId: number;
  actorRole: string;
  actorName?: string;
  tokenLabel?: string;
  tokenName?: string;
  channel: string;
  action: string;
  targetType: string;
  targetId: string;
  result: string;
  diff: { key: string; old?: string; new?: string }[];
  detail: string;
  prevHash: string;
  hash: string;
}

interface ListResponse {
  records: AuditRecord[];
  nextBeforeSeq: number;
}

export type AnchorState = "disabled" | "empty" | "ok" | "file_corrupt" | "tail_deleted" | "content_recomputed" | "anchor_lost";

/** 외부 앵커 대조 결과. message 는 서버가 만든 한국어 문장이다. */
export interface AnchorResult {
  state: AnchorState;
  message: string;
  lines: number;
  lastSeq: number;
  /** 과거에 앵커 유실이 있었고 다시 시작한 적이 있다. 재기동해도 유지된다. */
  lostHistory?: boolean;
}

/** 검증 요약 등급. ok 만 보면 "외부 대조 없음"이 통과로 읽히므로 서버가 따로 준다. */
export type VerifyLevel = "failed" | "chain_only" | "lost_history" | "verified";

const ANCHOR_FAILED: AnchorState[] = ["file_corrupt", "tail_deleted", "content_recomputed", "anchor_lost"];
const ANCHOR_TITLES: Record<AnchorState, string> = {
  disabled: "외부 대조 없음",
  empty: "외부 대조 없음",
  ok: "외부 앵커 일치",
  file_corrupt: "앵커 파일 손상",
  tail_deleted: "마지막 기록 삭제 의심",
  content_recomputed: "기록 내용 재작성 의심",
  anchor_lost: "외부 앵커 유실",
};

const LEVEL_TITLES: Record<VerifyLevel, string> = {
  failed: "검증 실패",
  chain_only: "기록 체인만 확인(외부 대조 없음)",
  lost_history: "앵커 유실 이력 있음",
  verified: "외부 대조까지 완전 통과",
};

export interface VerifyResponse {
  ok: boolean;
  /** 외부 앵커 대조까지 끝난 완전 통과인지. 앵커가 꺼졌거나 비었거나 유실 이력이 있으면 false. */
  externallyVerified?: boolean;
  level?: VerifyLevel;
  summary?: string;
  chainOk?: boolean;
  anchor?: AnchorResult;
  checked: number;
  brokenSeq: number;
  brokenWhy: string;
  headHash: string;
}

const PAGE = 50;
const RESULT_TONE: Record<string, Tone> = { success: "ok", denied: "bad", error: "bad" };

function VerifySummary({ verify, onRestart }: { verify: VerifyResponse; onRestart: () => void }) {
  const chainOk = verify.chainOk ?? verify.ok;
  const anchor = verify.anchor;
  const anchorFailed = !!anchor && ANCHOR_FAILED.includes(anchor.state);
  let text: string;
  if (!chainOk) text = `${verify.brokenSeq}번 기록에서 변조가 감지되었습니다. ${verify.brokenWhy}`;
  else if (anchorFailed) text = `${verify.checked}건의 기록은 서로 이어져 있지만, 외부 앵커 대조에서 변조가 감지되었습니다.`;
  else if (anchor && (anchor.state === "disabled" || anchor.state === "empty"))
    text = `${verify.checked}건을 검증했고 기록 사이의 변조는 없습니다. 다만 외부 대조는 하지 못했습니다.`;
  else if (anchor?.lostHistory)
    text = `${verify.checked}건을 검증했고 변조는 없습니다. 다만 앵커 유실 이력이 있어 완전 통과로 보지 않습니다.`;
  else text = `${verify.checked}건을 검증했고 변조가 없습니다.`;
  if (anchor?.state === "anchor_lost" && chainOk)
    text = `${verify.checked}건의 기록은 서로 이어져 있지만, 외부 앵커 기록이 유실되어 대조할 수 없습니다.`;
  const bad = !chainOk || anchorFailed;
  const warn = !bad && (!!anchor?.lostHistory || (!!anchor && (anchor.state === "disabled" || anchor.state === "empty")));
  const level: VerifyLevel | undefined =
    verify.level ?? (verify.externallyVerified === undefined ? undefined : verify.externallyVerified ? "verified" : "chain_only");
  return (
    <>
      <Notice tone={bad ? "error" : warn ? "warn" : "ok"} role="status">
        {text}
      </Notice>
      {/* 상태 상자 하나에 보조 설명 두 줄: 같은 말을 하는 상자가 겹쳐 보이지 않게 한다. */}
      {(level || anchor) && (
        <div className="verify-detail">
          {level && (
            <p data-testid="verify-level" data-level={level}>
              <strong className={`tone-${level === "failed" ? "bad" : level === "verified" ? "ok" : "warn"}`}>{LEVEL_TITLES[level]}</strong>
              {verify.summary ? `: ${verify.summary}` : ""}
            </p>
          )}
          {anchor && (
            <p data-testid="anchor-status" data-state={anchor.state}>
              <strong className={`tone-${anchorFailed ? "bad" : anchor.state === "ok" ? "ok" : "warn"}`}>
                {ANCHOR_TITLES[anchor.state] ?? "외부 대조"}
              </strong>
              : {anchor.message}
            </p>
          )}
        </div>
      )}
      {anchor?.state === "anchor_lost" && (
        <PermissionGate permission="audit:verify">
          <button type="button" className="btn-danger" onClick={onRestart}>
            앵커 다시 시작
          </button>
        </PermissionGate>
      )}
    </>
  );
}

/** 비고 한 줄. 동작 이름과 같은 말이면(예: 거래처 등록) 되풀이하지 않고, 있을 때는 변경 목록 아래 별도 줄로 보인다. */
function detailLine(r: AuditRecord) {
  const text = detailText(r.detail);
  if (!text || text === actionLabel(r.action)) return null;
  return <div>{text}</div>;
}

/** 한 기록의 상세. 일시·사용자·경로·동작·대상·결과와 변경 항목의 [항목, 이전, 이후] 표. 개인정보 값은 서버가 애초에 넣지 않는다. */
function RecordDetail({ record: r, onClose }: { record: AuditRecord; onClose: () => void }) {
  return (
    <section className="card" aria-label={`기록 ${r.seq}번 상세`} data-testid="audit-detail">
      <h2>기록 {r.seq}번 상세</h2>
      <dl>
        <dt>일시</dt>
        <dd>{new Date(r.at).toLocaleString("ko-KR")}</dd>
        <dt>사용자</dt>
        <dd>{actorWithTokenText(r)}</dd>
        <dt>경로</dt>
        <dd>{channelText(r)}</dd>
        <dt>동작</dt>
        <dd>{actionLabel(r.action)}</dd>
        <dt>대상</dt>
        <dd>
          {TARGET_TYPE_LABELS[r.targetType] ?? "기타"} {targetIdText(r.targetType, r.targetId)}
        </dd>
        <dt>결과</dt>
        <dd>{RESULT_LABELS[r.result] ?? "기타"}</dd>
      </dl>
      {r.diff.length > 0 ? (
        <TableWrap>
          <table aria-label="변경 내용 상세">
            <thead>
              <tr>
                <th>항목</th>
                <th>이전</th>
                <th>이후</th>
              </tr>
            </thead>
            <tbody>
              {r.diff.map((d) => {
                const p = diffParts(d, { tokenLabel: r.tokenLabel });
                return (
                  <tr key={d.key}>
                    <td>{p.label}</td>
                    <td>{p.before}</td>
                    <td>{p.after}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </TableWrap>
      ) : (
        <p>바뀐 항목이 기록되어 있지 않습니다.</p>
      )}
      {detailLine(r)}
      <p className="hint">이전 값이 없는 항목(새로 생긴 값)은 이전 칸이 빈칸입니다. 연락처·이메일 같은 개인정보 값은 기록에 들어 있지 않습니다.</p>
      <button type="button" className="btn-secondary" onClick={onClose}>
        상세 닫기
      </button>
    </section>
  );
}

export function AuditLogPage() {
  const { entities } = useAuth();
  const [records, setRecords] = useState<AuditRecord[]>([]);
  const [next, setNext] = useState(0);
  const [targetType, setTargetType] = useState("");
  const [result, setResult] = useState("");
  const [channel, setChannel] = useState("");
  const [entity, setEntity] = useState(""); // 법인 코드("" 는 볼 수 있는 범위 전체). 법인이 둘 이상일 때만 고르는 칸이 보인다.
  const [who, setWho] = useState(""); // 입력 중인 사용자 이름
  const [whoApplied, setWhoApplied] = useState(""); // 입력이 멈춘 뒤 실제로 거는 값
  const [detail, setDetail] = useState<AuditRecord | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [verify, setVerify] = useState<VerifyResponse | null>(null);

  const load = useCallback(
    async (before: number, append: boolean) => {
      setLoading(true);
      setError("");
      const q = new URLSearchParams({ limit: String(PAGE) });
      if (targetType) q.set("targetType", targetType);
      if (result) q.set("result", result);
      if (channel) q.set("channel", channel);
      if (entity) q.set("entity", entity);
      if (whoApplied) q.set("actorName", whoApplied);
      if (before) q.set("beforeSeq", String(before));
      try {
        const res = await api.get<ListResponse>(`/api/audit-logs?${q}`);
        setRecords((prev) => (append ? [...prev, ...res.records] : res.records));
        setNext(res.records.length >= PAGE ? res.nextBeforeSeq : 0);
      } catch {
        setError("감사 로그를 불러오지 못했습니다.");
      } finally {
        setLoading(false);
      }
    },
    [targetType, result, channel, entity, whoApplied],
  );

  useEffect(() => {
    void load(0, false);
  }, [load]);

  // 이름을 칠 때마다 조회하지 않도록 입력이 잠깐 멈춘 뒤에 건다.
  useEffect(() => {
    const t = setTimeout(() => setWhoApplied(who.trim()), 300);
    return () => clearTimeout(t);
  }, [who]);

  async function runVerify() {
    setError("");
    try {
      setVerify(await api.post<VerifyResponse>("/api/audit-logs/verify"));
      void load(0, false);
    } catch {
      setError("검증을 실행하지 못했습니다.");
    }
  }

  async function restartAnchor() {
    setError("");
    try {
      await api.post("/api/audit-logs/anchor/restart");
      await runVerify();
    } catch {
      setError("앵커를 다시 시작하지 못했습니다.");
    }
  }

  return (
    <section>
      <h1>감사 로그</h1>
      <div className="form-row">
        <label>
          사용자
          <input type="text" placeholder="이름" value={who} onChange={(e) => setWho(e.target.value)} maxLength={40} />
        </label>
        <label>
          대상
          <select value={targetType} onChange={(e) => setTargetType(e.target.value)}>
            <option value="">전체</option>
            {Object.entries(TARGET_TYPE_LABELS).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </label>
        {entities.length > 1 && (
          <label>
            법인
            <select value={entity} onChange={(e) => setEntity(e.target.value)}>
              <option value="">전체</option>
              {entities.map((en) => (
                <option key={en.code} value={en.code}>
                  {en.name}
                </option>
              ))}
            </select>
          </label>
        )}
        <label>
          경로
          <select value={channel} onChange={(e) => setChannel(e.target.value)}>
            <option value="">전체</option>
            {Object.entries(CHANNEL_LABELS).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </label>
        <label>
          결과
          <select value={result} onChange={(e) => setResult(e.target.value)}>
            <option value="">전체</option>
            {Object.entries(RESULT_LABELS).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </label>
        <PermissionGate permission="audit:verify">
          <button type="button" onClick={runVerify}>
            변조 여부 검증
          </button>
        </PermissionGate>
      </div>

      <ExportButtons
        path="/api/audit-logs/export"
        params={{ targetType, result, channel, entity, actorName: whoApplied }}
        disabled={loading || records.length === 0}
      />

      {verify && <VerifySummary verify={verify} onRestart={restartAnchor} />}
      {error && <Notice tone="error">{error}</Notice>}

      <TableWrap>
        <table className="audit-table">
          <thead>
            <tr>
              <th className="nowrap">번호</th>
              <th>일시</th>
              <th>사용자</th>
              <th>경로</th>
              <th>동작</th>
              <th>대상</th>
              <th>결과</th>
              <th>변경 내용</th>
              <th>상세</th>
            </tr>
          </thead>
          <tbody>
            {records.map((r) => (
              <tr key={r.seq} data-result={r.result}>
                <td className="num">{r.seq}</td>
                <td className="nowrap">{new Date(r.at).toLocaleString("ko-KR")}</td>
                <td className="nowrap">{actorWithTokenText(r)}</td>
                <td className="nowrap">{channelText(r)}</td>
                <td>{actionLabel(r.action)}</td>
                <td>
                  {TARGET_TYPE_LABELS[r.targetType] ?? "기타"} {targetIdText(r.targetType, r.targetId)}
                </td>
                <td className="nowrap">
                  <Badge tone={RESULT_TONE[r.result] ?? "muted"}>{RESULT_LABELS[r.result] ?? "기타"}</Badge>
                </td>
                <td>
                  {r.diff.length > 0 && (
                    <ul>
                      {r.diff.map((d) => (
                        <li key={d.key}>{diffItemText(d, { tokenLabel: r.tokenLabel })}</li>
                      ))}
                    </ul>
                  )}
                  {detailLine(r)}
                </td>
                <td className="nowrap">
                  <button type="button" className="btn-secondary btn-sm" aria-label={`${r.seq}번 기록 상세`} onClick={() => setDetail(r)}>
                    상세
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </TableWrap>
      {detail && <RecordDetail record={detail} onClose={() => setDetail(null)} />}
      {!loading && records.length === 0 && !error && <EmptyState>기록이 없습니다.</EmptyState>}
      {next > 0 && (
        <button type="button" className="btn-secondary" disabled={loading} onClick={() => load(next, true)}>
          더 보기
        </button>
      )}
    </section>
  );
}
