// (web/src/auth). 로그인 화면 라우트 모듈.
import type { RouteModule } from "../layout/routeModule";
import { EmergencyLoginPage } from "./EmergencyLoginPage";
import { LoginPage } from "./LoginPage";
import { SetupWizardPage } from "./SetupWizardPage";
import { ForgotPasswordPage, ResetPasswordPage } from "./PasswordLinkPages";

// 로그인은 레이아웃(메뉴) 밖의 최상위 라우트다. 메뉴 항목은 없다.
export const authModule: RouteModule = {
  name: "auth",
  routes: [
    { path: "/login", element: <LoginPage /> },
    { path: "/setup", element: <SetupWizardPage /> }, // 설치 마법사(B38): 서버가 설치 중에만 연다
    { path: "/login/emergency", element: <EmergencyLoginPage /> },
    { path: "/login/forgot", element: <ForgotPasswordPage /> },
    { path: "/login/reset", element: <ResetPasswordPage /> },
  ],
  menu: [],
};
