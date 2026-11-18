//(B48 1단계 화면). 사용자 정의 도구의 서버 형식과 편집 양식 사이 변환, 한국어 라벨 표.

export interface CatField {
  name: string;
  label: string;
  type: string;
  values?: string[];
  valueLabels?: Record<string, string>;
  ops?: string[];
  sortable: boolean;
  return: boolean;
  ctx?: string[];
}

export interface CatResource {
  name: string;
  label: string;
  entityOwned: boolean;
  defaultLimit: number;
  maxLimit: number;
  fields: CatField[];
}

export interface InputSpec {
  type: string;
  values?: string[];
  required?: boolean;
  description?: string;
  min?: number;
  max?: number;
  maxLength?: number;
  default?: string | number | boolean;
}

export interface FilterSpec {
  field: string;
  op: string;
  from?: string;
  value?: string | number | boolean;
}

export interface DraftFieldInfo {
  name: string;
  label: string;
  type: string;
  required: boolean;
}

export interface DraftTarget {
  name: string;
  label: string;
  fields: DraftFieldInfo[];
}

export interface MapSpec {
  from?: string;
  value?: string | number;
}

export interface Definition {
  format: string;
  name: string;
  kind: string;
  description: string;
  resource?: string;
  target?: string;
  map?: Record<string, MapSpec>;
  input?: Record<string, InputSpec>;
  filters?: FilterSpec[];
  fields?: string[];
  sort?: { field: string; dir: string };
  roles?: string[];
}

export interface ToolRow {
  id: number;
  name: string;
  title: string;
  kind: string;
  resource: string;
  resourceLabel: string;
  currentVersion: number;
  enabled: boolean;
  ratePerMin: number;
  evalSetId: number;
  broken: boolean;
  tested: boolean;
  calls24h: number;
  denied24h: number;
  updatedAt: string;
}

export interface VersionRow {
  version: number;
  note: string;
  createdBy: string;
  createdAt: string;
  definition: string;
  defHash: string;
}

export interface PendingRequest {
  id: number;
  version: number;
  requestedBy: string;
  requestedId: number;
  requestedAt: string;
}

export interface EvalGate {
  setCode: string;
  setName: string;
  passRatePct: number;
  thresholdPct: number;
  criticalFails: number;
  message: string;
}

export interface ToolDetail extends ToolRow {
  pendingRequest?: PendingRequest;
  definition: Definition;
  modelDescription: string;
  versions: VersionRow[];
}

export interface TestResult {
  fields: string[];
  rows: Record<string, string | number | boolean | null>[];
  count: number;
  elapsedMs: number;
  callableRoles: string[];
  notice: string;
}

export const TYPE_LABEL: Record<string, string> = {
  string: "문자",
  int: "숫자",
  bool: "참/거짓",
  date: "날짜",
  enum: "선택",
};
export const OP_LABEL: Record<string, string> = {
  eq: "같음",
  in: "여러 값 중 하나",
  gte: "이상",
  lte: "이하",
  prefix: "~로 시작",
};
export const KIND_LABEL: Record<string, string> = {
  read: "조회",
  draft: "초안",
};
export const CTX_LABEL: Record<string, string> = {
  "@my_department": "내 부서",
  "@my_user": "나",
  "@today": "오늘",
};
export const ROLE_LABEL: Record<string, string> = {
  admin: "관리자",
  finance: "재무",
  manager: "팀장",
  staff: "직원",
  auditor: "감사",
};

export interface InputRow {
  key: string;
  type: string;
  required: boolean;
  description: string;
  values: string;
  min: string;
  max: string;
  maxLength: string;
  def: string;
}

export interface FilterRow {
  field: string;
  op: string;
  source: "input" | "const" | "ctx";
  from: string;
  value: string;
}

export interface MapRow {
  field: string;
  source: "none" | "input" | "const";
  from: string;
  value: string;
}

export interface DefForm {
  kind: "read" | "draft";
  target: string;
  maps: MapRow[];
  name: string;
  title: string;
  description: string;
  resource: string;
  inputs: InputRow[];
  filters: FilterRow[];
  fields: string[];
  sortField: string;
  sortDir: string;
  roles: string[];
}

export const emptyForm = (): DefForm => ({
  kind: "read",
  target: "",
  maps: [],
  name: "custom_",
  title: "",
  description: "",
  resource: "",
  inputs: [],
  filters: [],
  fields: [],
  sortField: "",
  sortDir: "desc",
  roles: [],
});
export const emptyInput = (): InputRow => ({
  key: "",
  type: "string",
  required: false,
  description: "",
  values: "",
  min: "",
  max: "",
  maxLength: "",
  def: "",
});
export const emptyFilter = (): FilterRow => ({
  field: "",
  op: "eq",
  source: "const",
  from: "",
  value: "",
});

const num = (s: string): number | undefined =>
  s.trim() === "" || Number.isNaN(Number(s)) ? undefined : Number(s);

function typed(type: string, s: string): string | number | boolean | undefined {
  if (s === "") return undefined;
  if (type === "int") return Number(s);
  if (type === "bool") return s === "true";
  return s;
}

/** 편집 양식을 서버 정의로 바꾼다. 빈 칸은 빼고, 검증은 서버가 한다. */
export function toDefinition(
  f: DefForm,
  res?: CatResource,
  draft?: DraftTarget,
): Definition {
  const input: Record<string, InputSpec> = {};
  for (const r of f.inputs) {
    if (!r.key) continue;
    const spec: InputSpec = { type: r.type };
    if (r.required) spec.required = true;
    if (r.description.trim()) spec.description = r.description.trim();
    if (r.type === "enum")
      spec.values = r.values
        .split(",")
        .map((v) => v.trim())
        .filter(Boolean);
    if (r.type === "int") {
      const mn = num(r.min);
      const mx = num(r.max);
      if (mn !== undefined) spec.min = mn;
      if (mx !== undefined) spec.max = mx;
    }
    if (r.type === "string") {
      const ml = num(r.maxLength);
      if (ml !== undefined) spec.maxLength = ml;
    }
    const dv = typed(r.type, r.def);
    if (dv !== undefined) spec.default = dv;
    input[r.key] = spec;
  }
  const filters: FilterSpec[] = f.filters
    .filter((r) => r.field)
    .map((r) => {
      const out: FilterSpec = { field: r.field, op: r.op };
      if (r.source === "input") out.from = r.from;
      else if (r.source === "ctx") out.value = r.value;
      else {
        const ft =
          res?.fields.find((x) => x.name === r.field)?.type ?? "string";
        const v = r.op === "in" ? r.value : typed(ft, r.value);
        out.value = v === undefined ? "" : v;
      }
      return out;
    });
  if (f.kind === "draft") {
    const map: Record<string, MapSpec> = {};
    for (const m of f.maps) {
      if (m.source === "input" && m.from) map[m.field] = { from: m.from };
      else if (m.source === "const" && m.value !== "") {
        const ft = draft?.fields.find((x) => x.name === m.field)?.type;
        map[m.field] = { value: ft === "int" ? Number(m.value) : m.value };
      }
    }
    const dd: Definition = {
      format: "pl-mcp-tool/1",
      name: f.name,
      kind: "draft",
      description: f.description,
      target: f.target,
      map,
    };
    if (Object.keys(input).length) dd.input = input;
    if (f.roles.length) dd.roles = f.roles;
    return dd;
  }
  const d: Definition = {
    format: "pl-mcp-tool/1",
    name: f.name,
    kind: "read",
    description: f.description,
    resource: f.resource,
    fields: f.fields,
  };
  if (Object.keys(input).length) d.input = input;
  if (filters.length) d.filters = filters;
  if (f.sortField) d.sort = { field: f.sortField, dir: f.sortDir };
  if (f.roles.length) d.roles = f.roles;
  return d;
}

/** 서버 정의를 편집 양식으로 되돌린다. */
export function fromDefinition(d: Definition, title: string): DefForm {
  const inputs: InputRow[] = Object.entries(d.input ?? {}).map(([key, s]) => ({
    key,
    type: s.type,
    required: !!s.required,
    description: s.description ?? "",
    values: (s.values ?? []).join(","),
    min: s.min === undefined ? "" : String(s.min),
    max: s.max === undefined ? "" : String(s.max),
    maxLength: s.maxLength === undefined ? "" : String(s.maxLength),
    def: s.default === undefined ? "" : String(s.default),
  }));
  const filters: FilterRow[] = (d.filters ?? []).map((r) => {
    if (r.from)
      return {
        field: r.field,
        op: r.op,
        source: "input",
        from: r.from,
        value: "",
      };
    const v = String(r.value ?? "");
    return {
      field: r.field,
      op: r.op,
      source: v.startsWith("@") ? "ctx" : "const",
      from: "",
      value: v,
    };
  });
  const maps: MapRow[] = Object.entries(d.map ?? {}).map(([field, m]) =>
    m.from
      ? { field, source: "input", from: m.from, value: "" }
      : { field, source: "const", from: "", value: String(m.value ?? "") },
  );
  return {
    kind: d.kind === "draft" ? "draft" : "read",
    target: d.target ?? "",
    maps,
    name: d.name,
    title,
    description: d.description,
    resource: d.resource ?? "",
    inputs,
    filters,
    fields: d.fields ?? [],
    sortField: d.sort?.field ?? "",
    sortDir: d.sort?.dir ?? "desc",
    roles: d.roles ?? [],
  };
}

/** 결과 칸 값을 사람이 읽는 글로 바꾼다(선택 칸은 한국어 이름). */
export function cellText(f: CatField | undefined, v: unknown): string {
  if (v === null || v === undefined || v === "") return "-";
  if (typeof v === "boolean") return v ? "예" : "아니오";
  if (typeof v === "number") return v.toLocaleString("ko-KR");
  const s = String(v);
  return f?.valueLabels?.[s] ?? s;
}

/** 줄 단위로 서로 없는 줄을 표시하는 단순 비교(버전 비교 화면). */
export function diffLines(
  a: string,
  b: string,
): {
  left: { text: string; changed: boolean }[];
  right: { text: string; changed: boolean }[];
} {
  const la = a.split("\n");
  const lb = b.split("\n");
  const sa = new Set(la);
  const sb = new Set(lb);
  return {
    left: la.map((t) => ({ text: t, changed: !sb.has(t) })),
    right: lb.map((t) => ({ text: t, changed: !sa.has(t) })),
  };
}

export function pretty(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

export interface ImportResult {
  imported: number;
  rejected: number;
  items: { name: string; ok: boolean; message: string }[];
}
