import type { PurchaseRequest } from "../purchase/types";
// (web/src/document). 결재 양식·양식 문서 API 의 JSON 형식(internal/handler/form_register.go, approval_doc_register.go)과 한국어 표시 이름.

export type FieldType = "text" | "textarea" | "number" | "amount" | "date" | "select" | "table" | "attachment" | "unit";

export interface FieldDef {
  key: string;
  label: string;
  type: FieldType;
  required: boolean;
  help?: string;
  sensitive?: boolean;
  maxLength?: number;
  min?: number;
  max?: number;
  decimals?: number;
  options?: string[];
  columns?: FieldDef[];
  minRows?: number;
  maxRows?: number;
  maxFiles?: number;
  accept?: string[];
}

export const FIELD_TYPE_LABEL: Record<FieldType, string> = {
  text: "한 줄 글",
  textarea: "여러 줄 글",
  number: "숫자",
  amount: "금액(원)",
  date: "날짜",
  select: "선택",
  table: "표",
  attachment: "첨부 파일",
  unit: "조직 선택",
};

export interface StepSpec {
  role: string;
  kind?: string;
  source: string;
  level?: number;
  field?: string;
  required: boolean;
  stage?: number;
  userId?: number;
  unitId?: number;
  unitCode?: string;
}

export interface FormDefinition {
  system: boolean;
  fields: FieldDef[];
  body: string;
  line: { rule?: string; steps?: StepSpec[] };
  recipients: { cc: { unit_id?: number; unit_code?: string }[]; receive: { unit_id?: number; unit_code?: string }[] };
  numbering: { pattern: string; assign_at: "submit" | "create"; reset: "yearly" | "never" };
  retention_years: number;
  /** B29: 법정 보존(연한이 지나도 폐기 불가). 시스템 양식(구매 품의서·지출 결의서·재고 조정 전표)은 서버가 항상 참으로 본다. */
  legal_hold?: boolean;
  dept_limit: number[];
  settings: { line_edit: "none" | "add_only" | "free"; objection_action: "record" | "reject"; amount_field: string; dept_visible: boolean; urgent_allowed: boolean };
}

export interface FormSummary {
  id: number;
  code: string;
  category: string;
  name: string;
  kind: string;
  version: number;
  active: boolean;
}

export interface FormDetail extends FormSummary {
  versionId: number;
  definition: FormDefinition;
}

export interface FormAdminRow extends FormSummary {
  latestVersion: number;
  latestStatus: string;
  publishedCount: number;
  hasDraftVersion: boolean;
}

export interface FormVersion {
  id: number;
  formCode: string;
  version: number;
  status: "draft" | "published" | "retired";
  definition: FormDefinition;
  createdAt: string;
  publishedAt: string;
}

export const CATEGORY_LABEL: Record<string, string> = { general: "일반", purchase: "구매", finance: "재무", cooperation: "협조", other: "기타" };
export const VERSION_STATUS_LABEL: Record<string, string> = { draft: "작성 중", published: "게시", retired: "퇴역" };
export const LINE_EDIT_LABEL: Record<string, string> = { none: "바꿀 수 없음", add_only: "합의·참조·수신만 추가", free: "자유롭게 편집" };
export const OBJECTION_LABEL: Record<string, string> = { record: "이견을 기록하고 진행", reject: "이견이 있으면 반려" };
export const ROLE_LABEL: Record<string, string> = { approve: "결재", agree: "합의", cc: "참조", receive: "수신", remove: "기본 단계 빼기" };

export interface LineEdit {
  /** remove 는 free 양식에서 필수가 아닌 기본 단계를 뺀다(targetType default, targetId 는 기본 단계 번호). */
  role: "agree" | "cc" | "receive" | "approve" | "remove";
  targetType: "user" | "unit" | "default";
  targetId: number;
  group: number;
  /** 화면 표시용 이름(서버에는 보내도 무시된다). */
  name?: string;
}

export interface LineStepView {
  skipped?: boolean;
  skipNote?: string;
  order: number;
  kind: string;
  title: string;
  unitPath: string;
  unitName: string;
  approverId: number;
  approverName: string;
  acting: boolean;
  raisedNote: string;
  layerNote: string;
  omitted: boolean;
  missing: boolean;
  reason: string;
  poolSize: number;
  /** B29: 결재자가 부재 중이면 종료 시각과 대결자(결재선은 그대로). */
  absentUntil?: string;
  absenceDelegate?: string;
  role: string;
  stage: number;
  required: boolean;
  defIndex?: number;
  /** B25: 예산 초과로 강제로 더한 재무 단계. */
  forced?: boolean;
}

export interface LineRecipientView {
  kind: "cc" | "receive";
  targetType: "user" | "unit";
  targetId: number;
  name: string;
  path: string;
  note: string;
}

export interface DocLinePreview {
  authority?: import("../purchase/types").AuthorityNote;
  requesterName: string;
  unitPath: string;
  steps: LineStepView[];
  recipients: LineRecipientView[];
  complete: boolean;
  problems: string[];
  notes: string[];
}

export interface PersonalLine {
  id: number;
  name: string;
  formCode: string;
  edits: LineEdit[];
}

export type InboxBox = "drafted" | "pending" | "in_progress" | "done" | "cc" | "receive" | "rejected";

export const BOXES: InboxBox[] = ["drafted", "pending", "in_progress", "done", "cc", "receive", "rejected"];

export const BOX_LABELS: Record<InboxBox, string> = {
  drafted: "기안함",
  pending: "결재 대기함",
  in_progress: "결재 진행함",
  done: "완료함",
  cc: "참조함",
  receive: "수신함",
  rejected: "반려함",
};

export const BOX_HELP: Record<InboxBox, string> = {
  drafted: "내가 기안한 임시저장·결재 중·완료 문서입니다.",
  pending: "지금 내가 결재하거나 합의할 차례인 문서입니다(위임받은 문서 포함).",
  in_progress: "결재선에 내가 있고 아직 진행 중인 문서입니다(지금 내 차례가 아닌 것).",
  done: "내가 관여했고 결재가 완료된 문서입니다.",
  cc: "참조로 지정된 문서입니다.",
  receive: "내가 접수할 수 있는 수신처(조직장·수신 담당자)로 도착한 문서입니다.",
  rejected: "내가 기안했거나 결재선에 있었고 반려된 문서입니다.",
};

export interface InboxItem {
  id: number;
  no: string;
  title: string;
  formCode: string;
  formName: string;
  formKind: "purchase" | "general";
  requesterName: string;
  unitPath: string;
  status: PurchaseRequest["status"];
  urgent: boolean;
  unread: boolean;
  /** B29: 보존 기한이 지나 폐기된 문서(제목·본문은 지워지고 번호·양식·기안자만 남는다). */
  disposed?: boolean;
  updatedAt: string;
  amount: number;
  receiptId: number;
  receiptStatus: string;
  receiptUnit: string;
}

export type InboxCounts = Record<InboxBox, { total: number; unread: number }>;

export interface DocReceipt {
  id: number;
  docId: number;
  docNo: string;
  docTitle: string;
  unitId: number;
  unitName: string;
  status: "pending" | "received" | "assigned" | "done" | "returned";
  receivedBy: string;
  receivedAt: string;
  assigneeId: number;
  assigneeName: string;
  doneBy: string;
  doneAt: string;
  returnedBy: string;
  returnedAt: string;
  comment: string;
  reason: string;
  canHandle: boolean;
}

export function receiptStatusLabel(s: string): string {
  return ({ pending: "접수 대기", received: "접수됨", assigned: "담당자 지정", done: "처리 완료", returned: "반송" } as Record<string, string>)[s] ?? s;
}
