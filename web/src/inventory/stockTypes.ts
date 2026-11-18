// 재고 깊이(B24) API 의 JSON 형식과 한국어 표시 이름.
export interface Warehouse {
  id: number;
  code: string;
  name: string;
  location: string;
  active: boolean;
  itemCount: number;
  /** 사업장(B44). 지정이 없으면(주사업장) 빠진다. */
  siteId?: number;
  siteName?: string;
}

export interface Department {
  id: number;
  name: string;
}

export interface Allocation {
  itemId: number;
  lotId: number;
  lotNo: string;
  expiry: string;
  qty: number;
  onHand: number;
  received: string;
}

export interface IssueSlipLine {
  itemId: number;
  itemName: string;
  sku: string;
  lotId: number;
  lotNo: string;
  qty: number;
}

export interface IssueSlip {
  id: number;
  no: string;
  departmentName: string;
  warehouseName: string;
  issueDate: string;
  method: string;
  memo: string;
  actorName: string;
  lines: IssueSlipLine[];
}

export interface TransferSlip {
  id: number;
  no: string;
  fromName: string;
  toName: string;
  moveDate: string;
  memo: string;
  actorName: string;
  lines: { itemName: string; sku: string; lotNo: string; qty: number }[];
  /** B51: 법인 간 이동(내부 거래). 법인 하나 설치와 같은 법인 이동에는 아래 키가 없다. 상대 법인이 내 범위 밖이면 상대 창고 이름이 빈 글자다(법인 이름만 보인다). */
  intercompany?: boolean;
  fromEntity?: string;
  toEntity?: string;
  /** 이동 금액(동결). 재고 평가 조회 권한이 있을 때만 온다. */
  costAmount?: number;
}

/** B51: 다른 법인의 도착 창고 후보(양쪽 법인에서 재고 관리 권한이 있을 때만 온다). */
export interface TransferTarget {
  id: number;
  code: string;
  name: string;
  entityId: number;
  entityName: string;
}

export interface LedgerPreview {
  kind: "transfer_out" | "transfer_in";
  warehouseName: string;
  itemName: string;
  lotNo: string;
  qty: number;
  /** B51: 법인 간 이동 줄. 단가·금액은 재고 평가 조회 권한이 있을 때만 온다. noAvg 는 출발 법인에 평균 단가가 없어 0원으로 이동한다는 표시다. */
  intercompany?: boolean;
  entityName?: string;
  noAvg?: boolean;
  unitCost?: number;
  amount?: number;
}

export const ISSUE_METHOD_LABEL: Record<string, string> = { fifo: "선입선출", expiry: "유효기한 임박 우선", manual: "직접 고르기" };

export interface StockCountLine {
  itemId: number;
  itemName: string;
  sku: string;
  lotId: number;
  lotNo: string;
  systemQty: number;
  countedQty: number | null;
  diff: number;
  reason: string;
}

export interface StockCount {
  id: number;
  no: string;
  warehouseId: number;
  warehouseName: string;
  baseDate: string;
  status: "draft" | "approving" | "applied" | "rejected" | "no_diff" | "discarded";
  docId: number;
  docNo: string;
  rejectReason: string;
  createdAt: string;
  lines: StockCountLine[];
  diffLines: number;
  uncounted: number;
  movedSince: number;
}

export const COUNT_STATUS_LABEL: Record<string, string> = {
  draft: "작성 중",
  approving: "결재 중",
  applied: "반영 완료",
  rejected: "반려",
  no_diff: "차이 없음 종료",
  discarded: "폐기",
};

export interface LedgerRow {
  date: string;
  kind: string;
  kindLabel: string;
  warehouse: string;
  lotNo: string;
  docNo: string;
  in: number;
  out: number;
  balance: number;
  /** B27: 평가 단가와 금액 증감(입고 +, 출고 −). 재고 평가 조회 권한이 있을 때만 온다. */
  unitCost?: number;
  amount?: number;
  /** B51: 법인 간 이동 행. kindLabel 에 상대 법인 이름이 들어 있다. */
  intercompany?: boolean;
  counterEntity?: string;
}

export interface LedgerPage {
  opening: number;
  rows: LedgerRow[];
  totalIn: number;
  totalOut: number;
  closing: number;
  total: number;
}
