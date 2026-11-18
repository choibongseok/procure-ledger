// (web/src/org). 조직도·인사 발령 화면이 쓰는 API 응답 형식과 한국어 표시 이름.
import { ApiError } from "../api/client";

export type OrgKind = "company" | "division" | "department" | "team";

export const KIND_LABEL: Record<OrgKind, string> = { company: "회사", division: "본부", department: "부서", team: "팀" };
/** 조직장의 직책 이름(조직 종류가 정한다). */
export const HEAD_TITLE: Record<OrgKind, string> = { company: "대표", division: "본부장", department: "부서장", team: "팀장" };

export interface TreeNode {
  id: number;
  code: string;
  name: string;
  kind: OrgKind;
  closed: boolean;
  headUserId: number;
  headName: string;
  headActing: boolean;
  direct: number;
  total: number;
  children: TreeNode[];
}

export interface Place {
  unitId: number;
  name: string;
  kind: OrgKind;
  path: string;
  /** 회사부터 이어진 소속 경로(사람 카드용). */
  fullPath: string;
  isHead: boolean;
  acting: boolean;
  title?: string;
  closed: boolean;
}

export interface PersonCard {
  id: number;
  name: string;
  employeeNo: string;
  rank: string;
  active: boolean;
  /** 휴직 중(재직은 그대로, 구분은 민감 정보라 내려오지 않는다). */
  onLeave?: boolean;
  email: string;
  phone: string;
  extension: string;
  primary: Place;
  concurrent: Place[];
  acting: Place[];
}

export interface EventRow {
  id: number;
  kind: string;
  kindLabel: string;
  subject: "unit" | "person";
  subjectId: number;
  subjectName: string;
  effectiveDate: string;
  summary: string;
  reason: string;
  createdBy: string;
  status: "scheduled" | "applied" | "cancelled";
  corrects: number;
  /** 휴직 발령에 사유가 적혀 있다는 표시(원문은 목록에 없고 "사유 보기"로만 읽는다, 읽으면 열람 기록이 남는다). */
  hasAbsenceReason?: boolean;
}

/** 휴직 구분 선택지(서버 org.AbsenceKinds 와 같다). */
export const ABSENCE_KINDS: { value: string; label: string }[] = [
  { value: "illness", label: "질병" },
  { value: "childcare", label: "육아" },
  { value: "family_care", label: "가족 돌봄" },
  { value: "personal", label: "개인" },
  { value: "other", label: "기타" },
];

export interface Rank {
  id: number;
  name: string;
  sort: number;
  active: boolean;
  inUse: number;
}

export interface Issue {
  kind: "vacant_auto" | "vacant_open" | "head_no_approve" | "unregistered";
  unitId: number;
  userId: number;
  name: string;
  detail: string;
}

export interface ConsistencyLog {
  ok: boolean;
  mismatches: number;
  broken: string;
}

export const STATUS_LABEL: Record<EventRow["status"], string> = { scheduled: "예약", applied: "적용됨", cancelled: "취소됨" };

export const ROLE_OPTIONS: { value: string; label: string }[] = [
  { value: "staff", label: "직원" },
  { value: "manager", label: "결재자" },
  { value: "finance", label: "재무" },
  { value: "auditor", label: "감사" },
  { value: "admin", label: "관리자" },
];

export const LEAVE_KINDS = ["자진", "권고", "정년", "계약 만료", "기타"];

/** 오늘 날짜(KST) "YYYY-MM-DD". */
export function todayKST(now: Date = new Date()): string {
  return now.toLocaleDateString("sv-SE", { timeZone: "Asia/Seoul" });
}

/** "2026-03-01" → "2026. 3. 1." */
export function dayText(d: string): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(d);
  return m ? `${m[1]}. ${Number(m[2])}. ${Number(m[3])}.` : d;
}

export function flatten(
  node: TreeNode | null,
  depth = 0,
  out: { node: TreeNode; depth: number }[] = [],
): { node: TreeNode; depth: number }[] {
  if (!node) return out;
  out.push({ node, depth });
  node.children.forEach((c) => flatten(c, depth + 1, out));
  return out;
}

export interface ConfirmNeed {
  needConfirm: true;
  days: number;
  message: string;
}

/** 서버가 보낸 오류에서 사용자에게 보일 문구를 꺼낸다. 30일 넘는 소급 확인 요청이면 ConfirmNeed 를 돌려준다. */
export function readError(e: unknown, fallback: string): string | ConfirmNeed {
  if (e instanceof ApiError) {
    if (e.status === 409) {
      try {
        const j = JSON.parse(e.message) as Partial<ConfirmNeed>;
        if (j.needConfirm) return { needConfirm: true, days: j.days ?? 0, message: j.message ?? "계속할까요?" };
      } catch {
        /* 일반 문구 */
      }
    }
    if ((e.status === 400 || e.status === 409 || e.status === 404) && e.message && !e.message.startsWith("{")) return e.message;
    if (e.status === 403) return "이 작업을 할 권한이 없습니다.";
  }
  return fallback;
}
