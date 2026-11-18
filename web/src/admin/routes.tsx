// (web/src/admin). 감사 로그, 개인 토큰 화면 모듈.
import { guarded, guardedAny } from "../layout/guard";
import type { RouteModule } from "../layout/routeModule";
import { AuditLogPage } from "./AuditLogPage";
import { AccountPage } from "../auth/AccountPage";
import { AdminAuthPage } from "./AdminAuthPage";
import { BackupStatusPage } from "./BackupStatusPage";
import { OnboardingPage } from "./OnboardingPage";
import { DeliveryLogPage } from "./DeliveryLogPage";
import { NotifyAdminPage } from "./NotifyAdminPage";
import { RolesPage } from "./RolesPage";
import { PrivacyLogPage } from "./PrivacyLogPage";
import { PrivacySettingsPage } from "./PrivacySettingsPage";
import { MyPrivacyRequestsPage, PrivacyRequestsAdminPage } from "./PrivacyRequestsPage";
import { SodExceptionsPage, SodRulesPage, SodViolationsPage } from "./SodPages";
import { IntegrationsPage } from "./IntegrationsPage";
import { EntitiesPage } from "./EntitiesPage";
import { TaxAspPage } from "./TaxAspPage";
import { LlmProvidersPage, LlmRoutesPage } from "./LlmProviderPages";
import { KnowledgeAdminPage } from "./KnowledgeAdminPage";
import { ModulesPage } from "./ModulesPage";
import { WebhooksPage } from "./WebhooksPage";
import { ScimPage } from "./ScimPage";
import { SeparationPage } from "./SeparationPage";
import { adminHomeCards } from "./homeCards";
import { RetentionPage } from "./RetentionPage";
import { SettingsPage } from "./SettingsPage";
import { TokensPage } from "./TokensPage";

export const adminModule: RouteModule = {
  name: "admin",
  homeCards: adminHomeCards,
  routes: [
    { path: "/audit-logs", element: guarded("audit:read", <AuditLogPage />) },
    { path: "/admin/retention", element: guarded("retention:read", <RetentionPage />) },
    { path: "/admin/settings", element: guarded("llm:policy_manage", <SettingsPage />) },
    { path: "/admin/auth", element: guarded("auth:manage", <AdminAuthPage />) },
    { path: "/admin/roles", element: guarded("role:manage", <RolesPage />) },
    { path: "/admin/roles/separation", element: guarded("role:manage", <SeparationPage />) },
    { path: "/admin/onboarding", element: guarded("onboarding:read", <OnboardingPage />) },
    { path: "/admin/privacy-log", element: guarded("privacy:log_read", <PrivacyLogPage />) },
    { path: "/admin/privacy-settings", element: guarded("privacy:log_manage", <PrivacySettingsPage />) },
    { path: "/admin/privacy/requests", element: guardedAny(["privacy:request_manage", "privacy:log_read"], <PrivacyRequestsAdminPage />) },
    { path: "/privacy/my-requests", element: guarded("authenticated", <MyPrivacyRequestsPage />) },
    { path: "/admin/sod/rules", element: guarded("sod:read", <SodRulesPage />) },
    { path: "/admin/sod/violations", element: guarded("sod:read", <SodViolationsPage />) },
    { path: "/admin/sod/exceptions", element: guarded("sod:read", <SodExceptionsPage />) },
    { path: "/admin/integrations/webhooks", element: guardedAny(["webhook:manage", "audit:read"], <WebhooksPage />) },
    { path: "/admin/entities", element: guarded("entity:manage", <EntitiesPage />) },
    { path: "/admin/tax-asp", element: guarded("taxasp:manage", <TaxAspPage />) },
    { path: "/admin/llm/providers", element: guarded("llm:provider_manage", <LlmProvidersPage />) },
    { path: "/admin/llm/routes", element: guarded("llm:policy_manage", <LlmRoutesPage />) },
    { path: "/admin/knowledge", element: guarded("knowledge:manage", <KnowledgeAdminPage />) },
    { path: "/admin/modules", element: guarded("attendance:manage_module", <ModulesPage />) },
    { path: "/admin/integrations", element: guarded("api:manage", <IntegrationsPage />) },
    { path: "/admin/scim", element: guarded("scim:read", <ScimPage />) },
    { path: "/admin/backup", element: guarded("backup:read", <BackupStatusPage />) },
    { path: "/admin/notifications", element: guarded("notification:manage", <NotifyAdminPage />) },
    { path: "/admin/notifications/deliveries", element: guardedAny(["notification:manage", "audit:read"], <DeliveryLogPage />) },
    { path: "/account", element: guarded("authenticated", <AccountPage />) },
    { path: "/tokens", element: guarded("token:manage", <TokensPage />) },
  ],
  menu: [
    { label: "도입 점검표", path: "/admin/onboarding", permission: "onboarding:read", order: 79, group: "admin" },
    { label: "감사 로그", path: "/audit-logs", permission: "audit:read", order: 80, group: "admin" },
    { label: "보존·폐기", path: "/admin/retention", permission: "retention:read", order: 83, group: "admin" },
    { label: "관리자 설정", path: "/admin/settings", permission: "llm:policy_manage", order: 85, group: "admin" },
    { label: "로그인 설정", path: "/admin/auth", permission: "auth:manage", order: 86, group: "admin" },
    { label: "역할 관리", path: "/admin/roles", permission: "role:manage", order: 87, group: "admin" },
    { label: "알림 채널", path: "/admin/notifications", permission: "notification:manage", order: 88.4, group: "admin" },
    { label: "알림 발송 기록", path: "/admin/notifications/deliveries", permission: "audit:read", anyOf: ["notification:manage", "audit:read"], order: 88.5, group: "admin" },
    { label: "개인정보 조회 기록", path: "/admin/privacy-log", permission: "privacy:log_read", order: 88.8, group: "admin" },
    { label: "조회 기록 보관 설정", path: "/admin/privacy-settings", permission: "privacy:log_manage", order: 88.9, group: "admin" },
    { label: "정보주체 열람 요청", path: "/admin/privacy/requests", permission: "privacy:request_manage", anyOf: ["privacy:request_manage", "privacy:log_read"], order: 88.95, group: "admin" },
    { label: "권한 분리 규칙", path: "/admin/sod/rules", permission: "sod:read", order: 88.96, group: "admin" },
    { label: "권한 분리 위반", path: "/admin/sod/violations", permission: "sod:read", order: 88.97, group: "admin" },
    { label: "권한 분리 예외", path: "/admin/sod/exceptions", permission: "sod:read", order: 88.98, group: "admin" },
    { label: "연동 관리", path: "/admin/integrations", permission: "api:manage", order: 88.75, group: "admin" },
    { label: "변경 웹훅", path: "/admin/integrations/webhooks", permission: "webhook:manage", anyOf: ["webhook:manage", "audit:read"], order: 88.76, group: "admin" },
    { label: "법인 관리", path: "/admin/entities", permission: "entity:manage", order: 88.42, group: "admin" },
    { label: "전자세금계산서 대행사", path: "/admin/tax-asp", permission: "taxasp:manage", order: 88.77, group: "admin" },
    { label: "LLM 공급자", path: "/admin/llm/providers", permission: "llm:provider_manage", order: 88.781, group: "admin" },
    { label: "지식 색인", path: "/admin/knowledge", permission: "knowledge:manage", order: 88.79, group: "admin" },
    { label: "업무 모듈", path: "/admin/modules", permission: "attendance:manage_module", order: 88.791, group: "admin" },
    { label: "LLM 라우팅", path: "/admin/llm/routes", permission: "llm:policy_manage", order: 88.782, group: "admin" },
    { label: "계정 동기화", path: "/admin/scim", permission: "scim:read", order: 88.7, group: "admin" },
    { label: "백업 상태", path: "/admin/backup", permission: "backup:read", order: 88.6, group: "admin" },
    { label: "내 계정", path: "/account", permission: "authenticated", order: 89, group: "user" },
    { label: "내 정보 열람 요청", path: "/privacy/my-requests", permission: "authenticated", order: 89.5, group: "user" },
    { label: "개인 토큰", path: "/tokens", permission: "token:manage", order: 90, group: "user" },
  ],
};
