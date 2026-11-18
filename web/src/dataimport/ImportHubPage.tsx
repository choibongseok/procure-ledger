// 기초 데이터 가져오기 허브(B35 명세 8절): 대상 카드 7개와 가져오기 순서 안내.
import { Link } from "react-router-dom";
import { useAuth } from "../auth/AuthProvider";
import { Badge, EmptyState, Notice } from "../layout/ui";
import { errorText, useLoad } from "../purchase/useLoad";
import { importApi, TARGET_ORDER, type TargetStatus } from "./api";

function when(s?: string): string {
  return s ? new Date(s).toLocaleString("ko-KR") : "";
}

function Card({ t, order }: { t: TargetStatus; order: number }) {
  const usable = t.allowed && t.ready;
  return (
    <section className="card" aria-label={t.name} data-import-target={t.target}>
      <h3>
        {order}. {t.name}
      </h3>
      <p className="hint">{t.keyRule}</p>
      {t.lastBatchNo ? (
        <p>
          마지막 반영 {t.lastBatchNo} · 추가 {t.lastAdded}건 · 변경 {t.lastChanged}건 <span className="hint">({when(t.lastAt)})</span>
        </p>
      ) : (
        <p className="hint">아직 반영한 적이 없습니다.</p>
      )}
      {usable ? (
        <Link className="btn-primary" to={`/admin/import/${t.target}`}>
          {t.name} 가져오기
        </Link>
      ) : (
        <>
          <button type="button" disabled>
            {t.name} 가져오기
          </button>{" "}
          <Badge tone="muted">{t.reason || "지금은 가져올 수 없습니다."}</Badge>
        </>
      )}
    </section>
  );
}

export function ImportHubPage() {
  const { can } = useAuth();
  const load = useLoad(() => importApi.targets(), []);
  const targets = [...(load.data ?? [])].sort((a, b) => TARGET_ORDER.indexOf(a.target) - TARGET_ORDER.indexOf(b.target));
  return (
    <section>
      <h1>기초 데이터 가져오기</h1>
      <p>
        새 시스템을 쓰기 시작할 때 기존 엑셀의 기초 자료를 한꺼번에 넣습니다. 양식을 내려받아 채워 올리면 <strong>미리보기로 확인한 뒤 한 번에 반영</strong>합니다. 오류가 하나라도 있으면 아무것도 반영되지 않습니다.
      </p>
      <Notice tone="info">
        아래 순서대로 가져오세요: 계정과목 → 창고 → 거래처 → 품목 → 부서 예산 → 기초 재고 → 미지급 잔액. 앞 자료가 없으면 뒤 자료의 미리보기가 안내와 함께 오류로 표시됩니다.
      </Notice>
      {can("import:read") && (
        <p>
          <Link to="/admin/import/history">가져오기 이력 보기</Link>
        </p>
      )}
      {load.error && <Notice tone="error">{load.error || errorText(null)}</Notice>}
      {load.loading && !load.data && <p className="hint">불러오는 중…</p>}
      {load.data && targets.length === 0 && <EmptyState>가져올 수 있는 대상이 없습니다.</EmptyState>}
      <div className="card-grid">
        {targets.map((t, i) => (
          <Card key={t.target} t={t} order={i + 1} />
        ))}
      </div>
    </section>
  );
}
