// 목록·보고서 내보내기 공용 단추(B28, 3.8).
// 서버가 열람 범위·마스킹·상한·감사를 모두 처리한다. 이 부품은 현재 적용된 필터를 그대로 보내고 오류 문구를 보인다.
import { useState } from "react";
import { ApiError, apiDownload } from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { ConfirmDialog, Notice } from "./ui";

export interface ExportButtonsProps {
  /** 내보내기 라우트 경로(예 "/api/purchase-requests/export"). */
  path: string;
  /** **현재 적용된 필터만**. 입력 중이지만 조회하지 않은 값은 넣지 않는다. 빈 값은 보내지 않는다. */
  params?: Record<string, string | number | boolean | undefined | null>;
  /** 목록이 비었거나 아직 불러오는 중이면 true: 단추가 꺼지고 안내가 보인다. */
  disabled?: boolean;
  /** 민감 열(이메일·휴대전화 등)이 있는 데이터셋이면 true: mask:bypass 권한자에게 "원문 포함" 체크가 나온다. */
  sensitive?: boolean;
}

/** 쿼리 문자열을 만든다. 빈 값·undefined 는 뺀다. */
export function exportQuery(format: "xlsx" | "csv", params: ExportButtonsProps["params"], raw: boolean): string {
  const q = new URLSearchParams({ format });
  for (const [k, v] of Object.entries(params ?? {})) {
    if (v === undefined || v === null || v === "" || v === false) continue;
    q.set(k, v === true ? "1" : String(v));
  }
  if (raw) q.set("raw", "1");
  return q.toString();
}

export function ExportButtons({ path, params, disabled = false, sensitive = false }: ExportButtonsProps) {
  const { can } = useAuth();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [raw, setRaw] = useState(false);
  const [confirming, setConfirming] = useState<"xlsx" | "csv" | null>(null);

  async function run(format: "xlsx" | "csv") {
    setBusy(true);
    setError("");
    try {
      await apiDownload(`${path}?${exportQuery(format, params, sensitive && raw)}`);
    } catch (e) {
      setError(
        e instanceof ApiError && e.message
          ? e.message
          : "파일을 만들지 못했습니다. 조건을 좁혀 다시 시도하고, 계속되면 관리자에게 알려 주세요.",
      );
    } finally {
      setBusy(false);
    }
  }

  function start(format: "xlsx" | "csv") {
    if (sensitive && raw)
      setConfirming(format); // 원문 포함은 확인 대화상자를 거친다
    else void run(format);
  }

  const off = disabled || busy;
  return (
    <div className="export-buttons" role="group" aria-label="내보내기">
      <div className="form-row">
        <button type="button" className="btn-secondary" disabled={off} onClick={() => start("xlsx")}>
          {busy ? "내보내는 중…" : "엑셀(XLSX)"}
        </button>
        <button type="button" className="btn-secondary" disabled={off} onClick={() => start("csv")}>
          CSV
        </button>
        {sensitive && can("mask:bypass") && (
          <label>
            <input type="checkbox" checked={raw} onChange={(e) => setRaw(e.target.checked)} /> 원문 포함(민감 정보)
          </label>
        )}
        {disabled && !busy && <span className="hint">내보낼 자료가 없습니다.</span>}
      </div>
      {error && <Notice tone="error">{error}</Notice>}
      {confirming && (
        <ConfirmDialog
          title="원문을 포함해 내보낼까요?"
          confirmLabel="원문 포함해 내보내기"
          onCancel={() => setConfirming(null)}
          onConfirm={() => {
            const f = confirming;
            setConfirming(null);
            void run(f);
          }}
        >
          이 파일에는 가리지 않은 연락처가 들어갑니다. 파일은 시스템 밖으로 나가며 이 내보내기는 감사 로그에 남습니다.
        </ConfirmDialog>
      )}
    </div>
  );
}
