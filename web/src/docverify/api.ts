// 증빙 문서 판정 API 타입과 호출.
import { api, apiFetch } from "../api/client";

export type Outcome = "match" | "mismatch" | "hold";

export const OUTCOME_LABEL: Record<Outcome, string> = {
  match: "일치",
  mismatch: "불일치",
  hold: "판단 보류",
};

export interface VerdictItem {
  field: string;
  expected: string;
  actual: string;
  /** 발주 열 값. 이전 형식으로 저장된 판정에는 없다. */
  po?: string;
  /** 입고 열 값. 입고 번호 없이 판정했거나 해당 없으면 "-". */
  receipt?: string;
  /** 문서 열 값(actual 과 같다). */
  document?: string;
  result: Outcome;
  note: string;
}

/** 원문 인용 구간. start·end 는 sourceText 의 글자(코드 포인트) 위치 [start,end) 다. */
export interface Citation {
  start: number;
  end: number;
  quote: string;
}

export interface Verdict {
  id: number;
  documentId: number;
  poId: number;
  receiptId: number;
  outcome: Outcome;
  items: VerdictItem[];
  extraction: {
    kind: string;
    supplier: string;
    businessNo: string;
    totalAmount: number;
    lines: { name: string; quantity: number; unitPrice: number; amount: number }[];
    citations?: Record<string, Citation>;
  };
  /** 원문(역할별 마스킹 적용). 이미지 증빙이거나 원문이 없으면 빈 문자열. */
  sourceText?: string;
  confirmedBy: number;
  confirmedOutcome: Outcome | "";
  createdAt: string;
}

export function verifyDocument(form: FormData): Promise<Verdict> {
  return apiFetch<Verdict>("/api/documents/verify", { method: "POST", body: form });
}

/** 이미지 증빙 업로드 가능 상태: allowed(가능), forbidden_role(재무만 가능), disabled(관리자 설정으로 꺼짐). */
export type ImageUpload = "allowed" | "forbidden_role" | "disabled";

export function imageUploadStatus(): Promise<{ imageUpload: ImageUpload }> {
  return api.get("/api/documents/capabilities");
}

/** 이미지를 올릴 수 없을 때 화면에 보이는 안내(내부 용어 없이). */
export const IMAGE_NOTICE: Record<Exclude<ImageUpload, "allowed">, string> = {
  forbidden_role: "이미지 증빙은 재무 담당자만 올릴 수 있습니다. PDF 나 텍스트로 올려 주세요.",
  disabled: "이미지 증빙 전송이 꺼져 있어 이미지는 올릴 수 없습니다. 필요하면 관리자에게 켜 달라고 요청하세요. PDF 나 텍스트는 올릴 수 있습니다.",
};

export function listVerdicts(): Promise<{ verdicts: Verdict[] }> {
  return api.get("/api/documents/verdicts");
}

export function getVerdict(id: number): Promise<Verdict> {
  return api.get(`/api/documents/verdicts/${id}`);
}

export function confirmVerdict(id: number, outcome: Outcome, comment: string): Promise<Verdict> {
  return api.post<Verdict>(`/api/documents/verdicts/${id}/confirm`, { outcome, comment });
}

/** 근거 항목 이름을 화면용 한국어로 바꾼다(내부 필드명을 그대로 보이지 않는다). */
export function fieldLabel(field: string, lines?: { name: string }[]): string {
  const fixed: Record<string, string> = {
    business_no: "사업자번호",
    total_amount: "합계 금액",
    "document.sum": "문서 줄 금액 합계",
    "document.arithmetic": "문서 내부 계산",
    extraction: "문서 읽기",
    grounding: "원문 교차 확인",
    receipt: "입고 대조",
  };
  if (fixed[field]) return fixed[field];
  const m = /^line\[(\d+)\]\.(quantity|unit_price|amount|name)$/.exec(field);
  if (m) {
    const part = { quantity: "수량", unit_price: "단가", amount: "금액", name: "품목" }[m[2] as "quantity"];
    const name = lines?.[Number(m[1])]?.name?.trim();
    // 품목 이름을 알면 "볼트 M8 수량" 처럼 이름으로, 모르면 "2번째 줄 수량" 으로 보인다.
    return name ? `${name} ${part}` : `${Number(m[1]) + 1}번째 줄 ${part}`;
  }
  const n = /^line\[(.+)\]$/.exec(field);
  if (n) return `품목 ${n[1]}`;
  return field;
}

/** 근거 항목이 가리키는 원문 인용 키를 찾는다. 줄 금액·합계 같은 계산 항목은 같은 이름의 인용을 쓴다. */
export function citationKey(field: string): string {
  if (field === "document.sum") return "total_amount";
  return field;
}

/** 원문을 [앞, 강조, 뒤]로 자른다. 위치가 원문 범위 밖이면 null(강조하지 않는다). */
export function splitSource(text: string, c: Citation): [string, string, string] | null {
  const chars = Array.from(text);
  if (c.start < 0 || c.end > chars.length || c.start >= c.end) return null;
  return [chars.slice(0, c.start).join(""), chars.slice(c.start, c.end).join(""), chars.slice(c.end).join("")];
}

const MONEY_FIELD = /^(total_amount|document\.sum|line\[\d+\]\.(unit_price|amount))$/;
const COUNT_FIELD = /^line\[\d+\]\.quantity$/;

/** 금액·수량 값을 천 단위 구분으로 보인다(금액은 "원" 을 붙인다). 숫자가 아니거나 다른 항목이면 그대로 둔다. */
export function formatValue(field: string, value: string): string {
  if (!/^-?\d+$/.test(value)) return value;
  const n = Number(value).toLocaleString("ko-KR");
  if (MONEY_FIELD.test(field)) return `${n}원`;
  if (COUNT_FIELD.test(field)) return n;
  return value;
}

/** 설명 칸: 서버가 설명을 주지 않은 일치 항목은 일치한다는 문구를 보인다. */
export function noteText(it: { result: Outcome; note: string }): string {
  if (it.note) return it.note;
  return it.result === "match" ? "발주·입고와 일치합니다." : "";
}

/** 발주·입고·문서 세 열의 값. 이전 형식 판정(po·receipt 없음)은 기준 값을 발주 열에 두고 입고 열은 "-" 로 둔다. */
export function itemColumns(it: VerdictItem): { po: string; receipt: string; document: string } {
  const legacy = it.po === undefined && it.receipt === undefined;
  return {
    po: legacy ? it.expected : it.po || "-",
    receipt: legacy ? "-" : it.receipt || "-",
    document: it.document ?? it.actual,
  };
}

/** 입고 번호 없이 판정했을 때 표 위에 보이는 안내. */
/** 입고 이력(발주별) 한 줄. 서버는 입고 줄 단위로 주므로 receiptNo(RC-000012)로 묶어 입고 단위 선택지를 만든다. */
export interface ReceiptChoice {
  id: number;
  no: string;
  receivedAt: string;
  summary: string;
}

/** 발주 번호의 입고 목록(최근 100줄 안). 입고가 없거나 권한이 없으면 빈 목록이다. */
export async function listReceiptsOfPO(poId: number): Promise<ReceiptChoice[]> {
  const r = await api.get<{ rows: { receiptNo: string; itemName: string; quantity: number; receivedAt: string }[] }>(
    `/api/receipt-history?poId=${poId}`,
  );
  const byNo = new Map<string, ReceiptChoice & { parts: string[] }>();
  for (const x of r.rows ?? []) {
    const id = Number(x.receiptNo.replace(/^RC-0*/, ""));
    if (!Number.isFinite(id) || id <= 0) continue;
    const c = byNo.get(x.receiptNo) ?? { id, no: x.receiptNo, receivedAt: (x.receivedAt ?? "").slice(0, 10), summary: "", parts: [] };
    c.parts.push(`${x.itemName} ${x.quantity.toLocaleString("ko-KR")}`);
    byNo.set(x.receiptNo, c);
  }
  return [...byNo.values()].map(({ parts, ...c }) => ({ ...c, summary: parts.join(", ") }));
}

export const AUTO_JOURNAL_NOTICE =
  "일치로 확정하면 이 입고의 금액으로 자동 매입 전표(임시)가 만들어집니다. 입고를 고르지 않으면 전표가 만들어지지 않고 전표 대기에 올라갑니다.";

export const NO_RECEIPT_NOTICE =
  "입고 번호 없이 판정했습니다. 발주 전량 기준으로 대조하고 입고 열은 비웁니다.";
