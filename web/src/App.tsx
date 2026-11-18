// (web/src/layout 과 함께). 라우팅 골격 — 화면 추가는 각 영역 routes.tsx 에서 한다.
import { Route, Routes } from "react-router-dom";
import { useAuth } from "./auth/AuthProvider";
import { SetupPage } from "./auth/SetupPage";
import { Layout } from "./layout/Layout";
import { allMenu, appModules, publicModules } from "./routes";

export function App() {
  const { user, setup } = useAuth();
  // 제한 세션(비밀번호 변경·2단계 등록 전)이면 설정 화면만 그린다. 서버도 그 밖의 API 를 403 으로 막는다.
  if (user && setup) return <SetupPage setup={setup} />;
  return (
    <Routes>
      {publicModules.flatMap((m) => m.routes).map((r) => (
        <Route key={r.path} path={r.path} element={r.element} />
      ))}
      <Route element={<Layout menu={allMenu} />}>
        {appModules.flatMap((m) => m.routes).map((r) => (
          <Route key={r.path} path={r.path} element={r.element} />
        ))}
        <Route path="*" element={<p>페이지를 찾을 수 없습니다.</p>} />
      </Route>
    </Routes>
  );
}
