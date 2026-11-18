//
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { api, setCsrfToken, setUnauthorizedHandler } from "../api/client";
import { ROLE_LABELS, type PermissionOrOpen, type Role } from "./permissions";
import { setCurrentEntity, type CurrentEntity, type EntityChoice, type MeEntities } from "./entities";

export interface CurrentUser {
  id: number;
  name: string;
  role: Role;
  departmentId: number;
  departmentName?: string;
  /** 조직도의 주소속 조직 이름(상단 바에 보인다). 서버가 조직도에 없는 사용자는 부서 이름으로 채운다. */
  unitName?: string;
}

interface MeResponse {
  user: CurrentUser;
  csrfToken: string;
  setup?: string;
  /** 서버가 계산한 실효 권한(역할·부여의 합집합). 화면은 이 목록으로만 메뉴·단추를 정한다(B33). */
  permissions?: string[];
  roles?: string[];
  /** 다법인(B44): 고를 수 있는 법인·현재 법인·그룹 범위. 서버가 안 주면(옛 서버·시험) 법인이 없는 것으로 본다. */
  entities?: MeEntities["entities"];
  currentEntity?: MeEntities["currentEntity"];
  groupScope?: boolean;
  /** 켜진 업무 모듈 키(B50). 서버가 안 주면(옛 서버·시험) 켜진 모듈이 없는 것으로 본다. */
  modules?: string[];
}

interface AuthState {
  user: CurrentUser | null;
  /** 제한 세션 사유(비밀번호 변경·2단계 등록 전). 비어 있으면 정상 세션이다. */
  setup: string;
  /** 이 사람에게 권한을 준 역할 이름(표시용). */
  roles: string[];
  /** 상단 바·홈에 보이는 역할 표시: 서버가 준 역할 이름(사용자 정의 역할 포함), 없으면 기본 역할 이름. */
  roleText: string;
  loading: boolean;
  can: (perm: PermissionOrOpen) => boolean;
  /** 업무 모듈이 켜져 있는지(B50). 꺼진 모듈의 메뉴는 숨긴다(서버는 라우트를 404 로 막는다). */
  hasModule: (key: string) => boolean;
  refresh: () => Promise<void>;
  /** 고를 수 있는 법인. 하나 이하이면 법인 선택기를 그리지 않는다. */
  entities: EntityChoice[];
  currentEntity: CurrentEntity | null;
  groupScope: boolean;
  /** 세션의 현재 법인을 바꾼다(코드, "*" 는 전체 법인 조회). 성공하면 현재 법인 정보를 갱신한다. */
  switchEntity: (code: string) => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children, initialUser, initialSetup, initialPermissions }: { children: ReactNode; initialUser?: CurrentUser | null; initialSetup?: string; initialPermissions?: string[] }) {
  const [user, setUser] = useState<CurrentUser | null>(initialUser ?? null);
  const [setup, setSetup] = useState(initialSetup ?? "");
  // 서버가 준 실효 권한만 판정에 쓴다. 권한 목록이 없으면 전부 거부한다(역할 표로 대신하는 길은 없다. 시험은 test-setup.ts 가 initialPermissions 를 채워 준다).
  const [perms, setPerms] = useState<ReadonlySet<string> | null>(initialPermissions ? new Set(initialPermissions) : null);
  const [roles, setRoles] = useState<string[]>([]);
  const [modules, setModules] = useState<ReadonlySet<string>>(new Set());
  const [loading, setLoading] = useState(initialUser === undefined);
  const [entityState, setEntityState] = useState<MeEntities>({ entities: [], currentEntity: { all: false }, groupScope: false });

  const refresh = useCallback(async () => {
    try {
      const me = await api.get<MeResponse>("/api/me");
      setCsrfToken(me.csrfToken);
      setUser(me.user);
      setSetup(me.setup ?? "");
      setPerms(me.permissions ? new Set(me.permissions) : null);
      setRoles(me.roles ?? []);
      setModules(new Set(me.modules ?? []));
      setEntityState({ entities: me.entities ?? [], currentEntity: me.currentEntity ?? { all: false }, groupScope: me.groupScope === true });
    } catch {
      setUser(null);
      setSetup("");
      setPerms(null);
      setRoles([]);
      setModules(new Set());
      setEntityState({ entities: [], currentEntity: { all: false }, groupScope: false });
    } finally {
      setLoading(false);
    }
  }, []);

  const switchEntity = useCallback(async (code: string) => {
    setEntityState(await setCurrentEntity(code));
  }, []);

  useEffect(() => {
    setUnauthorizedHandler(() => setUser(null));
    if (initialUser === undefined) void refresh();
    return () => setUnauthorizedHandler(undefined);
  }, [initialUser, refresh]);

  const value = useMemo<AuthState>(
    () => ({
      user,
      setup,
      roles,
      roleText: roles.length > 0 ? roles.join(" · ") : user ? (ROLE_LABELS[user.role] ?? "") : "",
      loading,
      refresh,
      entities: entityState.entities,
      currentEntity: entityState.entities.length > 0 ? entityState.currentEntity : null,
      groupScope: entityState.groupScope,
      switchEntity,
      hasModule: (key) => modules.has(key),
      can: (perm) => {
        if (perm === "public") return true;
        if (user === null) return false;
        if (perm === "authenticated") return true;
        return perms ? perms.has(perm) : false;
      },
    }),
    [user, setup, roles, perms, modules, loading, refresh, entityState, switchEntity],
  );
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("AuthProvider 안에서만 쓸 수 있습니다");
  return ctx;
}
