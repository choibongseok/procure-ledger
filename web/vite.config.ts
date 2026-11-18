/// <reference types="vitest" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// 개발 서버는 /api 를 Go 서버(기본 :8080)로 넘긴다.
export default defineConfig({
  plugins: [react()],
  server: { proxy: { "/api": "http://localhost:8080" } },
  build: { outDir: "dist", emptyOutDir: true },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test-setup.ts"],
    // 검증이 겹쳐 돌거나 기계가 바쁠 때 화면 시험(렌더가 큰 문서·양식 화면)이 기본 5초 제한을 넘겨 간헐 실패하던 것을 막는다.
    // 시험 한 건의 전체 제한을 늘린다(findBy·waitFor 의 비동기 대기 제한은 test-setup.ts).
    testTimeout: 30000,
    hookTimeout: 30000,
  },
});
