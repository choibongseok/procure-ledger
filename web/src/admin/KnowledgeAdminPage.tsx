//(색인 현황)·erp(양식 연결)·platform(메뉴). 관리 > 지식 색인(B47 명세 11.2): 색인 현황, 다시 색인, 양식별 "내용 검색에 포함".
// 색인 현황에는 문서 제목이 나오지 않는다(건수만). 업로드 매뉴얼·규정 관리는 KnowledgeUploads 절이다.
import { useState } from "react";
import { api } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { Badge, ConfirmDialog, EmptyState, LoadingRows, Notice, PageHead, TableWrap, useToast } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";
import { KnowledgeUploads } from "./KnowledgeUploads";

interface Status {
  docs: Record<string, number>;
  states: Record<string, number>;
  chunks: number;
  queueOpen: number;
  oldestQueue: string;
  generation: number;
  changedAt: string;
}

interface FormRow {
  code: string;
  name: string;
  active: boolean;
  searchable: boolean;
}

const KIND_LABEL: Record<string, string> = { approval_doc: "결재 문서 본문", attachment: "첨부", upload: "업로드 매뉴얼·규정" };
const STATE_LABEL: Record<string, string> = {
  ready: "색인됨",
  pending: "처리 대기",
  no_text: "글자 없음(스캔본 등)",
  unreadable: "읽을 수 없음(글꼴 문제)",
  unsupported: "지원하지 않는 형식",
  too_long: "너무 길어 앞부분만 색인",
  failed: "색인 실패",
};

function when(iso: string): string {
  if (!iso) return "-";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString("ko-KR");
}

export function KnowledgeAdminPage() {
  const { entities } = useAuth();
  const toast = useToast();
  const status = useLoad(() => api.get<Status>("/api/admin/knowledge/status"), []);
  const forms = useLoad(() => api.get<FormRow[]>("/api/admin/forms"), []);
  const [entity, setEntity] = useState("");
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const st = status.data;
  const multi = (entities?.length ?? 0) > 1;

  async function reindex() {
    setBusy(true);
    setError("");
    try {
      const r = await api.post<{ done: number; skipped: number }>("/api/admin/knowledge/reindex", { entity, kind: "" });
      toast(`다시 색인했습니다. 처리 ${r.done}건, 내용이 같아 건너뜀 ${r.skipped}건`);
      setConfirm(false);
      await status.reload();
    } catch (e) {
      setError(errorText(e));
      setConfirm(false);
    } finally {
      setBusy(false);
    }
  }

  async function setSearchable(f: FormRow, searchable: boolean) {
    setError("");
    try {
      await api.put(`/api/admin/knowledge/forms/${encodeURIComponent(f.code)}`, { searchable });
      toast(searchable ? `「${f.name}」 문서를 내용 검색에 포함합니다.` : `「${f.name}」 문서를 내용 검색에서 뺍니다. 색인은 다음 다시 색인 때 정리됩니다.`);
      await forms.reload();
    } catch (e) {
      setError(errorText(e));
    }
  }

  return (
    <section>
      <PageHead title="지식 색인" />
      <p className="hint">완료된 결재 문서의 본문을 내용 검색에 쓰도록 색인합니다. 색인에는 열람 권한이 들어 있지 않고, 검색할 때마다 문서를 열 수 있는 사람인지 다시 판정합니다.</p>
      {error && <Notice tone="error">{error}</Notice>}

      <h2>색인 현황</h2>
      {status.loading && <LoadingRows cols={2} rows={3} />}
      {status.error && <Notice tone="error">{status.error}</Notice>}
      {st && (
        <>
          <TableWrap>
            <table className="stack-table" aria-label="색인 현황">
              <thead>
                <tr>
                  <th>종류</th>
                  <th className="num nowrap">색인된 문서</th>
                </tr>
              </thead>
              <tbody>
                {Object.entries(KIND_LABEL).map(([k, label]) => (
                  <tr key={k}>
                    <td data-label="종류">{label}</td>
                    <td data-label="색인된 문서" className="num nowrap">{(st.docs[k] ?? 0).toLocaleString("ko-KR")}건</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
          <p>
            조각 {st.chunks.toLocaleString("ko-KR")}개 · 처리 대기 {st.queueOpen}건
            {st.oldestQueue && ` (가장 오래된 대기 ${when(st.oldestQueue)})`} · 마지막 갱신 {when(st.changedAt)}
          </p>
          {st.queueOpen > 0 && <Notice tone="info">처리 대기 문서는 보통 1분 안에 검색됩니다. 오래 줄어들지 않으면 서버 작업이 멈춘 것이니 운영 담당자에게 알려 주세요.</Notice>}
          {Object.keys(st.states).some((s) => s !== "ready") && (
            <ul className="hint">
              {Object.entries(st.states)
                .filter(([s]) => s !== "ready")
                .map(([s, n]) => (
                  <li key={s}>
                    {STATE_LABEL[s] ?? s}: {n}건
                  </li>
                ))}
            </ul>
          )}
        </>
      )}

      <h2>다시 색인</h2>
      <p className="hint">내용이 바뀌지 않은 문서는 건너뜁니다. 몇 번을 눌러도 결과는 같습니다. 서버 호스트에서는 <code>erp-admin knowledge-reindex</code> 로도 할 수 있습니다.</p>
      <div className="form-row">
        {multi && (
          <div className="field">
            <label htmlFor="kn-entity">법인</label>
            <select id="kn-entity" value={entity} onChange={(e) => setEntity(e.target.value)}>
              <option value="">전체 법인</option>
              {entities?.map((e) => (
                <option key={e.code} value={e.code}>
                  {e.name}
                </option>
              ))}
            </select>
          </div>
        )}
        <button type="button" disabled={busy} onClick={() => setConfirm(true)}>
          다시 색인
        </button>
      </div>
      {confirm && (
        <ConfirmDialog title="색인을 다시 만들까요?" confirmLabel="다시 색인" busy={busy} variant="primary" onCancel={() => setConfirm(false)} onConfirm={() => void reindex()}>
          완료된 결재 문서를 처음부터 다시 읽어 색인합니다. 문서가 많으면 시간이 걸릴 수 있고, 그동안에도 검색은 계속 됩니다.
        </ConfirmDialog>
      )}

      <KnowledgeUploads onChanged={() => void status.reload()} />

      <h2>양식별 내용 검색</h2>
      <p className="hint">끄면 그 양식의 완료 문서는 내용 검색에 나오지 않습니다(인사·급여처럼 본문이 민감한 양식에 씁니다). 검색 화면에서는 바로 빠지고 색인은 다음 다시 색인 때 정리됩니다.</p>
      {forms.loading && <LoadingRows cols={2} rows={3} />}
      {forms.error && <Notice tone="error">{forms.error}</Notice>}
      {forms.data && forms.data.length === 0 && <EmptyState>등록된 양식이 없습니다.</EmptyState>}
      {forms.data && forms.data.length > 0 && (
        <TableWrap>
          <table className="stack-table" aria-label="양식별 내용 검색">
            <thead>
              <tr>
                <th>양식</th>
                <th className="nowrap">상태</th>
                <th className="nowrap">내용 검색에 포함</th>
              </tr>
            </thead>
            <tbody>
              {forms.data.map((f) => (
                <tr key={f.code}>
                  <td data-label="양식">{f.name}</td>
                  <td data-label="상태" className="nowrap">{f.active ? <Badge tone="ok">사용 중</Badge> : <Badge tone="muted">사용 중지</Badge>}</td>
                  <td data-label="내용 검색에 포함" className="nowrap">
                    <label>
                      <input type="checkbox" checked={f.searchable} onChange={(e) => void setSearchable(f, e.target.checked)} /> {f.searchable ? "포함" : "제외"}
                    </label>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </TableWrap>
      )}
    </section>
  );
}
