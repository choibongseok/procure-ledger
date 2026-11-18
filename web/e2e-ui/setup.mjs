// 설치 마법사 화면 스모크(B38).
//
// scripts/verify-ui.sh 가 빈 DB 로 띄운 서버(dev IdP 없음, PL_SETUP_CODE 지정)에 대해 돈다. 환경변수: UI_BASE_URL(필수), UI_SETUP_CODE(필수), UI_SHOT_DIR.
// 시나리오: 로그인 화면의 "초기 설정" 링크 → 틀린 설치 코드 거부 → 올바른 코드 → 4단계 입력 → 설치 완료(세션 없음) → 로그인 →
//          마법사 경로 404(화면은 안내) → 폭 390 에서 가로 넘침 없음.
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright";

const BASE = process.env.UI_BASE_URL;
const CODE = process.env.UI_SETUP_CODE;
if (!BASE || !CODE) {
  console.error("UI_BASE_URL 과 UI_SETUP_CODE 가 필요합니다");
  process.exit(2);
}
const SHOTS = process.env.UI_SHOT_DIR || "ui-screenshots";
mkdirSync(SHOTS, { recursive: true });

const problems = [];
let failed = 0;
let step = "(시작 전)";
const ok = (n) => console.log(`  통과    ${n}`);
const bad = (n, why) => {
  failed++;
  console.log(`  실패    ${n} — ${why}`);
};
async function check(name, fn) {
  step = name;
  try {
    await fn();
    ok(name);
  } catch (e) {
    bad(name, e.message.split("\n")[0]);
  }
}
const shot = (page, name) => page.screenshot({ path: join(SHOTS, `setup-${name}.png`), fullPage: true });

const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1100, height: 900 } });
const page = await ctx.newPage();
page.on("pageerror", (e) => problems.push(`예외 @ ${step}: ${e.message}`));
page.on("console", (m) => {
  if (m.type() === "error" && !m.text().startsWith("Failed to load resource")) problems.push(`콘솔 오류 @ ${step}: ${m.text()}`);
});
page.on("response", (r) => {
  const u = new URL(r.url());
  const intended =
    (r.status() === 401 && (u.pathname === "/api/me" || u.pathname === "/api/setup/check")) || // 비로그인 /api/me, 일부러 틀린 설치 코드
    (r.status() === 404 && (u.pathname === "/api/setup/status" || u.pathname === "/setup")); // 완료 뒤 닫힌 마법사
  if (r.status() >= 400 && !intended) problems.push(`응답 ${r.status()} @ ${step}: ${u.pathname}`);
});

await check("로그인 화면에 초기 설정 링크가 보인다", async () => {
  await page.goto(`${BASE}/login`);
  await page.getByRole("link", { name: "초기 설정 시작" }).waitFor({ timeout: 8000 });
  await shot(page, "01-login-pending");
});

await check("틀린 설치 코드는 한국어 문구로 거부된다", async () => {
  await page.getByRole("link", { name: "초기 설정 시작" }).click();
  await page.getByLabel("설치 코드", { exact: true }).fill("WRONG-CODE");
  await page.getByRole("button", { name: "확인" }).click();
  await page.getByText("설치 코드가 올바르지 않습니다").waitFor({ timeout: 5000 });
  await shot(page, "02-wrong-code");
});

await check("올바른 설치 코드로 다음 단계가 열린다", async () => {
  await page.getByLabel("설치 코드", { exact: true }).fill(CODE);
  await page.getByRole("button", { name: "확인" }).click();
  await page.getByRole("heading", { name: "회사 정보" }).waitFor({ timeout: 5000 });
});

await check("회사 정보 → 첫 관리자 → 로그인 방식 → 데모 → 확인", async () => {
  await page.getByLabel("회사명", { exact: true }).fill("에이씨엠이 주식회사");
  await page.getByLabel("회계연도 시작월", { exact: true }).selectOption("4");
  await page.getByRole("button", { name: "다음" }).click();
  await page.getByRole("heading", { name: "첫 관리자" }).waitFor();
  await page.getByLabel("이름", { exact: true }).fill("오너");
  await page.getByLabel("이메일", { exact: true }).fill("owner@acme.test");
  await page.getByLabel("비밀번호", { exact: true }).fill("Wizard-Pass-2026!");
  await page.getByLabel("비밀번호 확인", { exact: true }).fill("Wizard-Pass-2026!");
  await shot(page, "03-admin");
  await page.setViewportSize({ width: 390, height: 800 }); // 좁은 화면에서 가로 넘침이 없어야 한다
  if (await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)) throw new Error("첫 관리자 단계가 폭 390 에서 가로로 넘친다");
  await shot(page, "03b-admin-390");
  await page.setViewportSize({ width: 1100, height: 900 });
  await page.getByRole("button", { name: "다음" }).click();
  await page.getByRole("heading", { name: "로그인 방식" }).waitFor();
  await page.getByRole("button", { name: "다음" }).click();
  await page.getByRole("heading", { name: "데모 데이터" }).waitFor();
  await page.getByRole("button", { name: "다음" }).click();
  await page.getByRole("heading", { name: "확인" }).waitFor();
  await page.getByText("에이씨엠이 주식회사").waitFor();
  await shot(page, "04-confirm");
});

await check("설치 완료 뒤 세션 없이 로그인 안내가 나온다", async () => {
  await page.getByRole("button", { name: "설치 완료" }).click();
  await page.getByRole("heading", { name: "설치가 끝났습니다" }).waitFor({ timeout: 15000 });
  const cookies = await ctx.cookies();
  if (cookies.some((c) => /session/i.test(c.name))) throw new Error("완료 응답이 세션 쿠키를 만들었다");
  await shot(page, "05-done");
});

await check("방금 정한 이메일·비밀번호로 로그인한다", async () => {
  await page.goto(`${BASE}/login`);
  await page.getByLabel("이메일", { exact: true }).fill("owner@acme.test");
  await page.getByLabel("비밀번호", { exact: true }).fill("Wizard-Pass-2026!");
  await page.getByRole("button", { name: "로그인" }).click();
  // 첫 관리자는 비상 관리자라 2단계 인증이 필요한 구성이면 코드 입력 또는 등록 화면으로 간다. 로그인 화면을 벗어났는지만 본다.
  await page.waitForFunction(() => !document.querySelector("#login-password"), null, { timeout: 15000 });
  await shot(page, "06-after-login");
});

await check("완료 뒤 /setup 과 /api/setup/* 는 404 다", async () => {
  for (const path of ["/setup", "/api/setup/status"]) {
    const r = await ctx.request.get(`${BASE}${path}`);
    if (r.status() !== 404) throw new Error(`${path} 가 ${r.status()}`);
  }
  const post = await ctx.request.post(`${BASE}/api/setup/complete`, { data: {}, headers: { "X-Setup-Code": CODE } });
  if (post.status() !== 404) throw new Error(`complete 가 ${post.status()}`);
});

await check("폭 390 에서 가로 넘침이 없다", async () => {
  const mobile = await browser.newContext({ viewport: { width: 390, height: 800 } });
  const p = await mobile.newPage();
  await p.goto(`${BASE}/login`);
  await p.getByRole("button", { name: "로그인" }).waitFor();
  const over = await p.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
  if (over) throw new Error("로그인 화면이 가로로 넘친다");
  await mobile.close();
});

await browser.close();
for (const p of problems) {
  failed++;
  console.log(`  실패    ${p}`);
}
console.log(failed === 0 ? "설치 마법사 스모크: 통과" : `설치 마법사 스모크: 실패 ${failed}건`);
process.exit(failed === 0 ? 0 : 1);
