// 결재 문서 출력 화면(B23 단계 3, 명세 3.14): 브라우저 인쇄용 A4 한 장 틀.
// 위: 양식·문서 번호·기안 / 결재란(기안 → 결재 → 합의, 전결 표시) / 본문(마스킹은 서버가 한다) / 수신·참조·첨부·결재 의견·보존 기한·전결 근거.
// 인쇄 버튼을 누르면 출력 사실을 감사에 남기고(document.print) 브라우저 인쇄를 연다. 서명 칸은 이름 글자로 대신한다(전자서명 없음).
import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api } from "../api/client";
import { Notice } from "../layout/ui";
import { dateTime, won } from "../purchase/labels";
import type {
  ApprovalStep,
  HistoryRecord,
  PurchaseRequest,
} from "../purchase/types";
import { errorText, useLoad } from "../purchase/useLoad";
import { AuthorityLine } from "./AuthorityLine";
import { show } from "./DocView";
import { useUnitNames } from "./FormLineAdmin";
import { ROLE_LABEL } from "./types";

const COLS = 8; // 결재란은 한 줄에 8칸까지, 넘으면 줄을 바꿔 이어서 그린다.

/** 결재란 칸 한 개의 처리 결과 문구. */
export function stepResult(s: ApprovalStep): string {
  if (s.skipReason) return `(${s.skipReason})`;
  switch (s.status) {
    case "approved":
      return s.finalAuthority ? "전결" : "승인";
    case "agreed":
      return "동의";
    case "objected":
      return "이견";
    case "rejected":
      return "반려";
    case "skipped":
      return "건너뜀";
    default:
      return "대기";
  }
}

interface Cell {
  head: string;
  name: string;
  result: string;
  at: string;
  /** 대결로 처리된 칸의 원결재자 문구(작은 글씨). */
  sub?: string;
}

function cells(d: PurchaseRequest): Cell[] {
  const out: Cell[] = [
    {
      head: "기안",
      name: d.requesterName,
      result: d.status === "draft" ? "작성" : "기안",
      at: d.createdAt ? dateTime(d.createdAt) : "",
    },
  ];
  for (const s of d.steps) {
    const role = ROLE_LABEL[s.role ?? "approve"] ?? "결재";
    out.push({
      head: `${role}${s.title && s.title !== role ? ` ${s.title}` : ""}`,
      name:
        s.kind === "finance"
          ? s.assigneeName || "재무 담당"
          : s.assigneeName || "—",
      result: stepResult(s),
      at: s.decidedAt ? dateTime(s.decidedAt) : "",
    });
    if (s.onBehalfOfId) {
      const c = out[out.length - 1];
      c.sub = `원결재자 ${s.onBehalfOfName || c.name}`;
      c.name = `${s.approverName || c.name}(대결)`;
    }
  }
  return out;
}

/** 출력 화면 공용 인쇄 스타일(결재 문서·발주서가 같이 쓴다). 글자 크기(px)는 화면 글자 체계(본문 14·h1 20)와 무관한 인쇄 용지 기준이라 숫자로 둔다(디자인 인계 지시문 4 검토). */
export function PrintStyle() {
  return (
      <style>{`
        .print-page { max-width: 210mm; margin: 0 auto; background: #fff; color: #000; padding: 12mm; position: relative; }
        .print-page table { width: 100%; border-collapse: collapse; }
        .print-page th, .print-page td { border: 1px solid #444; padding: 4px 6px; font-size: 13px; vertical-align: top; overflow-wrap: anywhere; }
        @media (max-width: 600px) { .print-page { padding: 8px; } .print-page h1 { font-size: 20px; } }
        .print-page tbody tr:last-child td, .print-page tbody tr:last-child th { border-bottom: 1px solid #444 !important; }
        .print-sign { border-collapse: collapse; margin-left: auto; width: auto !important; }
        .print-sign td, .print-sign th { text-align: center; min-width: 84px; }
        .print-mark { position: absolute; top: 40%; left: 20%; font-size: 64px; color: rgba(0,0,0,0.12); transform: rotate(-20deg); pointer-events: none; }
        @media print { .no-print { display: none !important; } body { background: #fff; } .print-page { padding: 0; max-width: none; } nav, aside, header { display: none !important; } }
      `}</style>
  );
}

/** 결재란: 기안 → 결재 → 합의, 한 줄에 8칸까지(넘으면 줄을 바꿔 이어서 그린다). */
export function SignTable({ d }: { d: PurchaseRequest }) {
  const grid = cells(d);
  const rows: Cell[][] = [];
  for (let i = 0; i < grid.length; i += COLS) rows.push(grid.slice(i, i + COLS));
  return (
    <>
      {rows.map((row, ri) => (
        <div key={ri} style={{ overflowX: "auto", display: "flex", justifyContent: "flex-end", marginTop: 14 }}>
          <table
            className="print-sign"
            aria-label="결재란"
            style={{ margin: 0 }}
          >
            <tbody>
              <tr>
                {row.map((c, i) => (
                  <th key={i}>{c.head}</th>
                ))}
              </tr>
              <tr>
                {row.map((c, i) => (
                  <td key={i}>
                    {c.name}
                    {c.sub && <div style={{ fontSize: 10 }}>{c.sub}</div>}
                  </td>
                ))}
              </tr>
              <tr>
                {row.map((c, i) => (
                  <td key={i}>{c.result}</td>
                ))}
              </tr>
              <tr>
                {row.map((c, i) => (
                  <td key={i} style={{ fontSize: 11 }}>
                    {c.at}
                  </td>
                ))}
              </tr>
            </tbody>
          </table>
        </div>
      ))}
    </>
  );
}

/** 출력 머리글: 회사 설정의 로고·회사명(설정이 없거나 조회에 실패하면 아무것도 그리지 않는다). 출력은 열 때의 현재 설정으로 그려진다. */
export function CompanyHeader() {
  const [c, setC] = useState<{ name: string; hasLogo: boolean } | null>(null);
  useEffect(() => {
    let alive = true;
    api
      .get<{ name?: string; hasLogo?: boolean }>("/api/company")
      .then((r) => {
        if (alive && r && typeof r.name === "string" && r.name !== "") setC({ name: r.name, hasLogo: r.hasLogo === true });
      })
      .catch(() => undefined);
    return () => {
      alive = false;
    };
  }, []);
  if (!c) return null;
  return (
    <div aria-label="회사 머리글" style={{ display: "flex", alignItems: "center", gap: 12 }}>
      {c.hasLogo && <img src="/api/company/logo" alt={`${c.name} 로고`} style={{ maxHeight: 40, maxWidth: 160 }} />}
      <strong>{c.name}</strong>
    </div>
  );
}

export function PrintView({ kind }: { kind: "doc" | "purchase" }) {
  const { id } = useParams();
  const units = useUnitNames();
  const base = kind === "doc" ? "/api/approval-docs" : "/api/purchase-requests";
  const doc = useLoad(() => api.get<PurchaseRequest>(`${base}/${id}`), [id]);
  const hist = useLoad(
    () => api.get<HistoryRecord[]>(`/api/approvals/${id}/history`),
    [id],
  );
  const [err, setErr] = useState("");

  if (doc.error)
    return (
      <Notice tone="error">
        {doc.error.includes("찾을 수") ? "문서를 찾을 수 없습니다." : doc.error}
      </Notice>
    );
  if (!doc.data) return <p>불러오는 중…</p>;
  const d = doc.data;
  const names = Object.fromEntries(
    (d.attachments ?? []).map((a) => [a.id, a.filename]),
  );
  const comments = (hist.data ?? []).filter((h) => h.comment.trim() !== "");
  if (d.disposedAt)
    return (
      <Notice tone="error">
        폐기된 문서는 출력할 수 없습니다. <Link to={kind === "doc" ? `/docs/${d.id}` : `/purchase-requests/${d.id}`}>문서로 돌아가기</Link>
      </Notice>
    );
  const back = kind === "doc" ? `/docs/${d.id}` : `/purchase-requests/${d.id}`;

  async function print() {
    setErr("");
    try {
      await api.post(`/api/approval-docs/${id}/print`);
      window.print();
    } catch (e) {
      setErr(errorText(e));
    }
  }

  return (
    <section className="print-page" aria-label="출력 화면">
      <PrintStyle />
      <div className="no-print" style={{ display: "flex", alignItems: "center", gap: 16, flexWrap: "wrap" }}>
        <button type="button" onClick={() => void print()}>
          인쇄
        </button>
        <Link to={back}>돌아가기</Link>
        {err && <Notice tone="error">{err}</Notice>}
      </div>
      {d.status === "draft" && <div className="print-mark">임시저장</div>}
      <CompanyHeader />
      <h1 style={{ textAlign: "center" }}>{d.formName || "구매 품의서"}</h1>
      <table aria-label="문서 정보">
        <tbody>
          <tr>
            <th>문서 번호</th>
            <td>{d.no.startsWith("TMP-") ? "임시저장" : d.no}</td>
            <th>기안일</th>
            <td>{d.createdAt ? dateTime(d.createdAt) : "—"}</td>
          </tr>
          <tr>
            <th>기안 부서</th>
            <td>{d.requesterUnitPath || "—"}</td>
            <th>기안자</th>
            <td>
              {d.requesterName}
              {d.urgent ? " (긴급)" : ""}
            </td>
          </tr>
          <tr>
            <th>제목</th>
            <td colSpan={3}>{d.title}</td>
          </tr>
        </tbody>
      </table>
      <SignTable d={d} />
      <h2>본문</h2>
      {kind === "doc" ? (
        <dl className="kv">
          {(d.formFields ?? []).map((f) => (
            <div key={f.key}>
              <dt>{f.label}</dt>
              <dd>{show(f, d.fields?.[f.key], names, units, true)}</dd>
            </div>
          ))}
        </dl>
      ) : (
        <table aria-label="품목">
          <thead>
            <tr>
              <th>품목</th>
              <th>수량</th>
              <th>단가</th>
              <th>금액</th>
            </tr>
          </thead>
          <tbody>
            {d.lines.map((l) => (
              <tr key={l.id}>
                <td>{l.itemName}</td>
                <td>{l.quantity.toLocaleString("ko-KR")}</td>
                <td>{won(l.unitPrice)}</td>
                <td>{won(l.amount)}</td>
              </tr>
            ))}
            <tr>
              <th colSpan={3}>합계</th>
              <td>{won(d.totalAmount)}</td>
            </tr>
          </tbody>
        </table>
      )}
      {(d.recipients ?? []).filter(
        (r) => !(r.kind === "cc" && r.targetType === "user" && r.note),
      ).length > 0 && (
        <>
          <h2>수신·참조</h2>
          <ul>
            {(d.recipients ?? [])
              .filter(
                (r) => !(r.kind === "cc" && r.targetType === "user" && r.note),
              )
              .map((r) => (
                <li key={`${r.kind}-${r.targetType}-${r.targetId}`}>
                  {ROLE_LABEL[r.kind]}: {r.name}
                  {r.path ? ` (${r.path})` : ""}
                </li>
              ))}
          </ul>
        </>
      )}
      {(d.attachments ?? []).length > 0 && (
        <>
          <h2>첨부</h2>
          <ul>
            {(d.attachments ?? []).map((a) => (
              <li key={a.id}>
                {a.filename} ({Math.ceil(a.size / 1024).toLocaleString("ko-KR")}
                KB)
              </li>
            ))}
          </ul>
        </>
      )}
      {comments.length > 0 && (
        <>
          <h2>결재 의견</h2>
          <ul>
            {comments.map((h) => (
              <li key={h.id}>
                {h.actorName ?? ""}: {h.comment} ({dateTime(h.at)})
              </li>
            ))}
          </ul>
        </>
      )}
      <p>보존 기한: {d.retentionUntil || "—"}</p>
      <AuthorityLine note={d.authority} plain />
    </section>
  );
}
