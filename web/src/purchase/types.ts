// 서버(internal/handler/purchase_http.go)의 JSON 형식과 같다.
export type PRStatus = "draft" | "submitted" | "in_review" | "rejected" | "approved" | "ordered";
export type StepKind = "team_lead" | "dept_head" | "division_head" | "ceo" | "finance";
export type StepStatus = "waiting" | "pending" | "approved" | "rejected" | "agreed" | "objected" | "skipped";
export type HistoryAction = "submit" | "resubmit" | "approve" | "reject" | "withdraw" | "reassign" | "agree" | "object" | "cancel_submit";
export type POStatus = "open" | "partial" | "received";

export interface PRLine {
  id: number;
  itemId: number;
  itemName: string;
  quantity: number;
  unitPrice: number;
  amount: number;
}

export interface ApprovalStep {
  seq: number;
  kind: StepKind;
  approverId: number;
  status: StepStatus;
  comment: string;
  decidedAt: string;
  /** B21: 상신 당시 결재선 스냅샷과 현재 결재자(재지정 반영). 조직도 도입 이전 건은 비어 있다. */
  title?: string;
  unitId?: number;
  unitPath?: string;
  assigneeId?: number;
  assigneeName?: string;
  acting?: boolean;
  raisedNote?: string;
  layerNote?: string;
  reassignedFrom?: string;
  headChangedTo?: string;
  /** B23: 역할(approve·agree), 단계 묶음(같은 번호의 합의는 병렬), 필수 단계 여부. */
  role?: string;
  stage?: number;
  required?: boolean;
  /** B23 단계 3: 전결 최종 결재 단계 표시와 건너뜀 사유("전결"). */
  finalAuthority?: boolean;
  skipReason?: string;
  /** B29: 대결로 처리된 단계면 원결재자와 실제 처리자. */
  onBehalfOfId?: number;
  onBehalfOfName?: string;
  approverName?: string;
}

export interface AuthorityNote {
  ruleId: number;
  minAmount: number;
  maxAmount: number;
  finalOrder: number;
  finalTitle: string;
  skippedOrders: number[];
  slug: string;
  clauseNo: string;
  warning?: string;
}

export interface PurchaseRequest {
  /** 상신 취소가 지금 가능한가(요청자 본인에게만, 아무도 처리하지 않았을 때). */
  canCancelSubmit?: boolean;
  /** 본인의 임시저장·반려 건이면 고칠 수 있다(서버가 견적 비교와 같은 함수로 계산한다). */
  editable?: boolean;
  /** B23 단계 3: 상신 때 저장한 전결 판정(없으면 없음). */
  authority?: AuthorityNote;
  id: number;
  no: string;
  requesterId: number;
  requesterName: string;
  departmentId: number;
  vendorId: number;
  title: string;
  reason: string;
  status: PRStatus;
  totalAmount: number;
  rejectReason: string;
  lines: PRLine[];
  steps: ApprovalStep[];
  purchaseOrderId: number;
  createdAt: string;
  updatedAt: string;
  /** 진행 중인데 지금 차례 단계를 결재할 사람이 없을 때 true. */
  noApprover?: boolean;
  /** 위임받은 결재 건일 때만(결재함). 위임자 이름. */
  delegatedFromId?: number;
  delegatedFromName?: string;
  /** B21: 귀속 조직과 상신 당시 조직(없으면 빈 값), 현재 상신 차수. */
  ownerUnitId?: number;
  requesterUnitPath?: string;
  lineAsOf?: string;
  round?: number;
  /** B23: 문서 양식(구매요청은 구매 품의서). */
  formCode?: string;
  formName?: string;
  formKind?: string;
  /** B23 양식 문서: 필드 값, 긴급, 보존 기한, 참조·수신, 첨부(이름·크기만), 기안 때 양식의 필드 정의와 본문 서식. */
  fields?: Record<string, unknown>;
  urgent?: boolean;
  retentionUntil?: string;
  /** 보존 만료로 폐기된 문서의 폐기일(YYYY-MM-DD). 있으면 제목·본문·첨부·의견이 지워진 껍데기다(B29). */
  disposedAt?: string;
  recipients?: { kind: "cc" | "receive"; targetType: "user" | "unit"; targetId: number; name: string; path: string; note: string }[];
  attachments?: { id: number; filename: string; size: number }[];
  formFields?: import("../document/types").FieldDef[];
  formBody?: string;
  /** B23: 저장해 둔 결재선 편집(참조·수신)과 자격을 잃어 뺀 대상 안내(기안자 본인의 임시저장·반려 건). */
  draftEdits?: { role: string; targetType: string; targetId: number; group: number; name?: string }[];
  editNotes?: string[];
  /** B25: 희망 납기(없으면 빈 값)·납품처, 상신 때 판정한 예산 스냅샷(판정 전이면 없음). */
  dueDate?: string;
  deliveryPlace?: string;
  budget?: BudgetSnapshot;
}

export type BudgetState = "none" | "within" | "over" | "unregistered";

export interface BudgetSnapshot {
  state: BudgetState;
  month: string;
  budget: number;
  used: number;
  inProgress: number;
  amount: number;
  over: number;
  /** 예산 초과로 재무 단계를 실제로 더했는지(금액 구간으로 이미 있었으면 false). */
  financeAdded?: boolean | null;
}

export interface BudgetCheck {
  month: string;
  departmentId: number;
  departmentName: string;
  registered: boolean;
  budget: number;
  used: number;
  inProgress: number;
  balance: number;
  amount: number;
  over: number;
  after: number;
  state: BudgetState;
}

export interface BudgetRow {
  departmentId: number;
  departmentName: string;
  month: string;
  amount: number;
  used: number;
  inProgress: number;
  balance: number;
  usagePct: number;
  badge: "ok" | "near" | "over";
}

export interface BudgetRevision {
  departmentName: string;
  month: string;
  oldAmount?: number;
  newAmount: number;
  reason: string;
  actorName: string;
  at: string;
}

export interface Delegation {
  id: number;
  delegatorId: number;
  delegatorName: string;
  delegateId: number;
  delegateName: string;
  from: string;
  to: string;
  revokedAt: string | null;
  createdAt: string;
  /** B29: absence(부재) | delegation(위임, 기존). 위임은 사유·메모가 비어 있다. */
  kind?: "absence" | "delegation";
  reason?: "vacation" | "trip" | "training" | "other" | "";
  note?: string;
}

export interface DelegationList {
  given: Delegation[];
  received: Delegation[];
}

export interface DelegateCandidate {
  id: number;
  name: string;
  role: string;
  departmentId: number;
  /** 조직도의 직책 이름(팀장·부서장·본부장·대표·재무)과 조직장인 조직 이름. */
  title: string;
  unitName: string;
  /** 지금 부재 중이라 대결자로 고를 수 없는 사람. */
  away?: boolean;
}

export interface HistoryRecord {
  id: number;
  prId: number;
  seq: number;
  actorId: number;
  actorName?: string;
  /** 위임 결재면 위임자 ID. */
  onBehalfOfId?: number;
  onBehalfOfName?: string;
  action: HistoryAction;
  comment: string;
  at: string;
}

export interface Item {
  id: number;
  sku: string;
  name: string;
  unit: string;
  safetyStock: number;
  trackLot: boolean;
}

export interface Vendor {
  id: number;
  name: string;
  businessNo: string;
  accountNo: string;
  phone: string;
  email: string;
}

export interface Lot {
  lotId: number;
  lotNo: string;
  expiryDate: string;
  quantity: number;
}

export interface ItemStock {
  /** B24: 창고별 재고(재고가 있는 창고만). */
  byWarehouse?: { warehouseId: number; warehouseName: string; onHand: number }[];
  /** B24: 창고·로트별 재고(창고를 고르면 그 창고만). */
  warehouseLots?: { warehouseId: number; warehouseName: string; lotId: number; lotNo: string; expiryDate: string; quantity: number }[];
  item: Item;
  onHand: number;
  lots: Lot[];
  belowSafety: boolean;
  /** B27: 평균 단가·재고 금액(재고 평가 조회 권한이 있고 평가가 있을 때만 온다). */
  avgCost?: number;
  stockAmount?: number;
  /** 전체 법인 보기에서 법인별로 나눈 평균 단가·재고 금액(단가는 합산하지 않는다). */
  valuations?: { entityId: number; entityName: string; avgCost: number; stockAmount: number }[];
}

export interface ExpiringLot {
  item: Item;
  lot: Lot;
  daysToExpire: number;
}

export interface POLine {
  id: number;
  itemId: number;
  itemName: string;
  quantity: number;
  receivedQty: number;
  unitPrice: number;
}

export interface PurchaseOrder {
  id: number;
  no: string;
  prId: number;
  vendorId: number;
  status: POStatus;
  totalAmount: number;
  lines: POLine[];
  createdAt: string;
  /** 거래처가 그룹의 다른 법인이면 그 법인 이름(법인 간 거래 배지, B44). 아니면 빠진다. */
  intercompany?: string;
}

export interface DecisionResult {
  request: PurchaseRequest;
  purchaseOrder: PurchaseOrder | null;
}

/** 결재선 미리보기(GET /api/approvals/line-preview). */
export interface LineStep {
  /** B25: 예산 초과로 강제로 더한 재무 단계. */
  forced?: boolean;
  order: number;
  kind: StepKind;
  title: string;
  unitId: number;
  unitName: string;
  unitPath: string;
  approverId: number;
  approverName: string;
  acting: boolean;
  raisedFrom: string;
  raiseReason: string;
  raisedNote: string;
  layerNote: string;
  omitted: boolean;
  missing: boolean;
  reason: string;
  poolSize: number;
  /** B29: 결재자가 부재 중이면 종료 시각과 대결자(결재선은 그대로). */
  absentUntil?: string;
  absenceDelegate?: string;
}

export interface LinePreview {
  requesterId: number;
  requesterName: string;
  unitPath: string;
  asOf: string;
  steps: LineStep[];
  complete: boolean;
  problems: string[];
  notes: string[];
}

/** 앱 안 알림. 종류 코드는 서버가 주지 않는다(화면에 보이지 않는다). */
export interface AppNotification {
  id: number;
  title: string;
  body: string;
  link: string;
  createdAt: string;
  readAt: string | null;
}

/** B25 단계 2: 견적서·견적 비교(서버 internal/handler/quote_register.go 와 같은 형식). 원문 텍스트는 내려오지 않는다. */
export interface QuoteLine {
  name: string;
  quantity: number;
  unitPrice: number;
  amount: number;
}

export interface Quote {
  id: number;
  prId: number;
  vendorId: number;
  vendorName: string;
  supplier: string;
  filename: string;
  status: "read" | "unreadable";
  edited: boolean;
  lines: QuoteLine[];
  total: number;
  docTotal: number | null;
  leadDays: number | null;
  leadText: string;
  warnings: string[];
  filled: boolean;
  selected: boolean;
  lowest: boolean;
  overLowest: number;
  createdAt: string;
}

export interface QuoteCompare {
  quotes: Quote[];
  editable: boolean;
  selectedId: number;
  reason: string;
  reasonRequired: boolean;
  lowestTotal: number;
  prTotal: number;
  totalMismatch: boolean;
  note: string;
}
