// 기초 데이터 가져오기(B35) API 형식과 호출. 화면은 fetch 를 직접 쓰지 않는다.
import { api, apiDownload, apiFetch } from "../api/client";

export interface ColumnGuide {
  label: string;
  required: boolean;
  help: string;
  example: string;
}

export interface TargetStatus {
  target: string;
  name: string;
  needsBaseDate: boolean;
  keyRule: string;
  allowed: boolean;
  ready: boolean;
  reason?: string;
  lastBatchNo?: string;
  lastAdded: number;
  lastChanged: number;
  lastAt?: string;
  columns: ColumnGuide[];
}

export interface RowChange {
  column: string;
  old: string;
  new: string;
}

export interface RowVerdict {
  rowNo: number;
  key: string;
  verdict: "add" | "change" | "same" | "error";
  reason?: string;
  warn?: string;
  changes?: RowChange[];
}

export interface Counts {
  total: number;
  add: number;
  same: number;
  change: number;
  error: number;
  warn: number;
}

export interface Preview {
  batchNo: string;
  token: string;
  target: string;
  baseDate?: string;
  counts: Counts;
  applicable: boolean;
  message?: string;
  keyRule: string;
  summary?: string[];
  fileWarnings?: string[];
  rows: RowVerdict[];
  rowsTruncated: boolean;
  sameFileBatch?: string;
}

export interface Batch {
  no: string;
  target: string;
  targetName: string;
  status: "previewed" | "applied" | "expired" | "canceled";
  baseDate?: string;
  counts: Counts;
  summary?: string[];
  journalNos?: string[];
  createdBy: string;
  createdAt: string;
  appliedAt?: string;
}

export interface BatchDetail extends Batch {
  rows: RowVerdict[];
  truncated: boolean;
}

/** 대상 순서(명세 3.4): 앞 대상이 DB 에 있어야 뒤 대상을 가져올 수 있다. */
export const TARGET_ORDER = ["account", "warehouse", "vendor", "item", "budget", "opening_stock", "opening_payable"];

export const STATUS_LABEL: Record<Batch["status"], string> = { previewed: "미리보기", applied: "반영됨", expired: "만료", canceled: "취소" };
export const VERDICT_LABEL: Record<RowVerdict["verdict"], string> = { add: "추가", change: "변경", same: "동일", error: "오류" };

export const importApi = {
  targets: () => api.get<{ targets: TargetStatus[] }>("/api/imports/targets").then((r) => r.targets ?? []),
  template: (target: string) => apiDownload(`/api/imports/templates/${target}`),
  preview: (target: string, file: File, baseDate: string) => {
    const f = new FormData();
    f.append("file", file);
    if (baseDate) f.append("baseDate", baseDate);
    return apiFetch<Preview>(`/api/imports/${target}/preview`, { method: "POST", body: f });
  },
  apply: (target: string, batchNo: string, token: string, file: File) => {
    const f = new FormData();
    f.append("file", file);
    f.append("token", token);
    return apiFetch<Batch>(`/api/imports/${target}/batches/${batchNo}/apply`, { method: "POST", body: f });
  },
  cancel: (target: string, batchNo: string) => apiFetch<void>(`/api/imports/${target}/batches/${batchNo}/cancel`, { method: "POST" }),
  /** 미리보기한 본인이 받는 오류 파일(대상 권한). 조회 권한자는 errorFileRead 를 쓴다. */
  errorFile: (target: string, batchNo: string) => apiDownload(`/api/imports/${target}/batches/${batchNo}/errors`),
  errorFileRead: (batchNo: string) => apiDownload(`/api/imports/${batchNo}/errors`),
  history: (status: string, target: string) => {
    const q = new URLSearchParams();
    if (status) q.set("status", status);
    if (target) q.set("target", target);
    const s = q.toString();
    return api.get<{ batches: Batch[] }>(`/api/imports${s ? `?${s}` : ""}`).then((r) => r.batches ?? []);
  },
  batch: (no: string) => api.get<BatchDetail>(`/api/imports/${no}`),
};
