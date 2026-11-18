// 백업 상태(B38 명세 6.6·D16): 마지막 백업·복구 리허설·복구 결과를 보여 주는 읽기 전용 화면. backup:read(관리자·감사).
// 백업·복구는 서버 호스트에서 erp-admin 으로만 실행한다. 웹 세션이 탈취돼도 DB 를 덮어쓰는 경로가 생기지 않도록 이 화면에는 실행 단추가 없다.
import { api } from "../api/client";
import { Badge, EmptyState, Notice, TableWrap } from "../layout/ui";
import { useLoad } from "../purchase/useLoad";

interface Run {
  id: number;
  kind: "backup" | "restore" | "rehearsal";
  startedAt: string;
  finishedAt: string;
  result: "success" | "failed";
  setName: string;
  dbKind: string;
  bytes: number;
  chainSeq: number;
  errorCode?: string;
}

interface Status {
  last: Partial<Record<Run["kind"], Run>>;
  lastFailed: Partial<Record<Run["kind"], Run>>;
  recent: Run[];
  available: boolean;
}

const KIND_LABEL: Record<Run["kind"], string> = { backup: "백업", rehearsal: "복구 리허설", restore: "복구" };
const KINDS: Run["kind"][] = ["backup", "rehearsal", "restore"];

function when(s: string): string {
  const t = new Date(s);
  return Number.isNaN(t.getTime()) ? s : t.toLocaleString("ko-KR");
}

function size(n: number): string {
  if (n <= 0) return "-";
  if (n < 1024 * 1024) return `${Math.max(1, Math.round(n / 1024)).toLocaleString("ko-KR")} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

export function BackupStatusPage() {
  const st = useLoad(() => api.get<Status>("/api/admin/backup/status"), []);
  const d = st.data;
  return (
    <section>
      <h1>백업 상태</h1>
      <p>마지막 백업과 복구 리허설의 결과입니다. 백업·복구는 서버를 운영하는 사람이 서버 호스트에서 erp-admin 으로 실행하며, 이 화면에서는 실행할 수 없습니다.</p>
      {st.error && <Notice tone="error">{st.error}</Notice>}
      {st.loading && !d && <p className="hint">불러오는 중…</p>}
      {d && !d.available && <EmptyState hint="서버를 최신 버전으로 올린 뒤 다시 확인하세요.">백업 기록을 읽을 수 없습니다.</EmptyState>}
      {d && d.available && (
        <>
          <div className="card-grid">
            {KINDS.map((k) => {
              const ok = d.last[k];
              const bad = d.lastFailed[k];
              return (
                <section className="card" key={k} aria-label={`마지막 ${KIND_LABEL[k]}`}>
                  <h2>마지막 {KIND_LABEL[k]}</h2>
                  {ok ? (
                    <p>
                      <Badge tone="ok">성공</Badge> {when(ok.finishedAt)}
                      {ok.setName && <span className="hint"> · 세트 {ok.setName}</span>}
                    </p>
                  ) : (
                    <p>
                      <Badge tone="warn">기록 없음</Badge>
                    </p>
                  )}
                  {bad && (
                    <Notice tone="warn">
                      그 뒤에 {KIND_LABEL[k]} 실패 기록이 있습니다({when(bad.finishedAt)}{bad.errorCode ? `, 사유 ${bad.errorCode}` : ""}). 서버 호스트에서 원인을 확인하세요.
                    </Notice>
                  )}
                </section>
              );
            })}
          </div>
          {!d.last.backup && <Notice tone="warn">성공한 백업 기록이 없습니다. 서버 호스트에서 erp-admin backup 으로 백업을 만들고 주기 실행을 설정하세요.</Notice>}
          {d.last.backup && !d.last.rehearsal && <Notice tone="warn">복구 리허설 기록이 없습니다. 복구해 보기 전에는 백업이라고 할 수 없습니다. erp-admin restore --rehearse 로 한 번 해 보세요.</Notice>}
          <h2>최근 기록</h2>
          {d.recent.length === 0 ? (
            <EmptyState>아직 기록이 없습니다.</EmptyState>
          ) : (
            <TableWrap>
              <table>
                <thead>
                  <tr>
                    <th>일시</th>
                    <th>종류</th>
                    <th>결과</th>
                    <th>세트</th>
                    <th>크기</th>
                    <th>감사 체인 머리</th>
                  </tr>
                </thead>
                <tbody>
                  {d.recent.map((r) => (
                    <tr key={r.id}>
                      <td>{when(r.finishedAt)}</td>
                      <td>{KIND_LABEL[r.kind] ?? r.kind}</td>
                      <td>{r.result === "success" ? <Badge tone="ok">성공</Badge> : <Badge tone="bad">실패</Badge>}</td>
                      <td>{r.setName || "-"}</td>
                      <td>{size(r.bytes)}</td>
                      <td>{r.chainSeq > 0 ? r.chainSeq.toLocaleString("ko-KR") : "-"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </TableWrap>
          )}
        </>
      )}
    </section>
  );
}
