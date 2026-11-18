// 브라우저 화면 스모크(B14).
//
// 실제 빌드 산출물(bin/server, PL_DEV_IDP=1)을 헤드리스 Chromium 으로 돌린다. scripts/verify-ui.sh 가 서버를 띄우고
// 이 스크립트를 부른다. 환경변수: UI_BASE_URL(필수), UI_SHOT_DIR(스크린샷 폴더, 기본 ui-screenshots).
//
// 시나리오: dev 로그인 → 구매요청 작성(관련 규정)·상신 → 결재(팀장·부서장·재무) → 발주·입고(부분 → 잔량)
//          → 문서 검증(근거 강조) → 역할별 홈(카드)·메뉴 → 감사 로그 화면 → 권한 없는 메뉴·카드·화면이 없는지.
// 실패 기준: 단계 실패, 콘솔 오류(console.error·처리되지 않은 예외), 의도하지 않은 4xx/5xx 응답, 요청 실패.
// 단계가 실패하면 그 단계를 FAIL 로 남기고 의존하는 뒤 단계는 "선행 단계 실패"로 센다(통과로 위장하지 않는다).
import { mkdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { chromium } from "playwright";

const BASE = process.env.UI_BASE_URL;
if (!BASE) {
  console.error("UI_BASE_URL 이 필요합니다");
  process.exit(2);
}
const SHOTS = process.env.UI_SHOT_DIR || "ui-screenshots";
mkdirSync(SHOTS, { recursive: true });

const ACCOUNTS = {
  staff: "staff@pl.test",
  lead: "lead@pl.test",
  head: "head@pl.test",
  finance: "finance@pl.test",
  auditor: "auditor@pl.test",
  admin: "admin@pl.test",
};

// ---- 결과 집계 ----
const results = [];
const problems = []; // 콘솔 오류·네트워크 실패
let current = "(시작 전)";

function pass(name) {
  results.push({ name, ok: true });
  console.log(`  통과    ${name}`);
}
function fail(name, why) {
  results.push({ name, ok: false, why });
  console.log(`  실패    ${name} — ${why}`);
}

/** 의도된 4xx 는 여기에 적는다. 기본은 비어 있다: 로그인 화면의 비로그인 /api/me 401 만 허용한다. */
function expectedResponse(url, status) {
  const u = new URL(url);
  if (status === 401 && u.pathname === "/api/me") return true;
  // B43: 같은 승인번호의 매입 수기 등록은 한국어 사유로 거부된다(중복 거부 단계).
  if ((status === 400 || status === 409) && u.pathname === "/api/finance/tax-invoices/receive" && current.startsWith("전자세금계산서(B43")) return true;
  // B46 3단계: 기준선 미달 라우팅 저장은 저장 전 평가가 확인을 요구하는 409 로 거부된다(확인 칸 단계).
  if (status === 409 && u.pathname === "/api/admin/llm/routes" && current.startsWith("저장 전 평가(B46 3단계")) return true;
  // B47 2단계: 지원하지 않는 형식(.hwp)의 업로드는 한국어 사유로 거부된다.
  return status === 400 && u.pathname === "/api/admin/knowledge/uploads" && current.startsWith("사내 지식 업로드(B47");
}

function watch(page, who) {
  page.on("console", (m) => {
    if (m.type() !== "error") return;
    // 브라우저가 실패한 응답마다 남기는 "Failed to load resource" 는 아래 response 검사가 의도 여부를 가려 센다.
    if (m.text().startsWith("Failed to load resource")) return;
    problems.push(`[${who}] 콘솔 오류 @ ${current}: ${m.text()}`);
  });
  page.on("pageerror", (e) => problems.push(`[${who}] 처리되지 않은 예외 @ ${current}: ${e.message}`));
  page.on("response", (r) => {
    if (r.status() >= 400 && !expectedResponse(r.url(), r.status())) {
      problems.push(`[${who}] 의도하지 않은 ${r.status()} @ ${current}: ${r.request().method()} ${r.url()}`);
    }
  });
  page.on("requestfailed", (r) => {
    // 페이지 이동으로 취소된 요청은 실패가 아니다.
    if ((r.failure()?.errorText ?? "").includes("ABORTED")) return;
    problems.push(`[${who}] 요청 실패 @ ${current}: ${r.method()} ${r.url()} ${r.failure()?.errorText}`);
  });
}

const browser = await chromium.launch();
const sessions = {};

let activeWho = null; // 가장 최근에 쓴 세션(실패 스크린샷 대상)
async function session(who) {
  activeWho = who;
  if (sessions[who]) return sessions[who];
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 }, locale: "ko-KR" });
  // 모든 대기는 상태를 기다리는 방식(waitFor·auto-wait)이고 고정 sleep 이 없다. 검증이 겹쳐 돌아 서버·브라우저가 바쁠 때 10초는 모자라 간헐 실패(입고 화면 등)가 났다.
  context.setDefaultTimeout(30_000);
  const page = await context.newPage();
  watch(page, who);
  sessions[who] = { context, page };
  return sessions[who];
}

/** dev IdP 로 로그인한다(같은 OIDC 코드 경로). 로그인 화면을 거치지 않고 로그인 요청 주소로 바로 간다. */
async function loginDirect(who) {
  const { page } = await session(who);
  await page.goto(`${BASE}/api/auth/login?login_hint=${encodeURIComponent(ACCOUNTS[who])}`);
  await page.waitForURL(`${BASE}/`);
  await page.getByRole("heading", { name: "홈" }).waitFor();
  return page;
}

async function shot(page, name) {
  // 스크롤된 채 전체 페이지를 찍으면 고정된 상단 바가 이미지 중간에 겹쳐 찍힌다. 맨 위로 올린 뒤 찍는다.
  await page.evaluate(() => window.scrollTo(0, 0));
  // 폰트 로딩 대기가 부하에 따라 30초를 넘기는 일이 있어(화면 문제가 아니다) 짧은 제한으로 한 번 다시 찍는다.
  try {
    await page.screenshot({ path: join(SHOTS, `${name}.png`), fullPage: true, timeout: 15000 });
  } catch {
    await page.screenshot({ path: join(SHOTS, `${name}.png`), fullPage: true, timeout: 30000 });
  }
}

/**
 * step(이름, 의존 여부, 함수). 함수가 던지면 FAIL. dependsOn 이 true 이고 앞 단계가 실패했으면 건너뛰지 않고 실패로 센다.
 * 알려진 결함으로 표시할 단계는 knownDefect 에 이유를 적는다(실패로 남는다).
 */
async function step(name, fn, { needs = null } = {}) {
  current = name;
  // UI_ONLY=<이름 일부>: 그 이름의 단계만 돌린다(한 단계를 고칠 때 전체 한 시간을 다시 기다리지 않으려는 개발용. 기본은 전체).
  if (process.env.UI_ONLY && !name.includes(process.env.UI_ONLY)) return { failed: false, name, skipped: true };
  if (needs && needs.failed) {
    fail(name, `선행 단계 실패로 실행하지 못함(${needs.name})`);
    return { failed: true, name };
  }
  try {
    await fn();
    pass(name);
    return { failed: false, name };
  } catch (e) {
    const lines = String(e?.message ?? e).split("\n");
    // 시간 초과는 어느 위치(locator)를 기다리다 났는지가 원인 추적에 필요하다: 호출 기록의 첫 "waiting for" 줄을 함께 남긴다.
    const waiting = lines.find((l) => /waiting for/.test(l));
    fail(name, waiting ? `${lines[0]} [${waiting.trim().slice(0, 160)}]` : lines[0]);
    const who = activeWho;
    try {
      if (who) {
        const body = (await sessions[who].page.locator("main").first().innerText({ timeout: 2000 })).replace(/\s+/g, " ").slice(0, 240);
        console.log(`    실패 시점의 화면(${who}, ${sessions[who].page.url()}): ${body}`);
      }
    } catch {
      /* 화면 글을 못 읽어도 무시 */
    }
    try {
      if (who) await sessions[who].page.screenshot({ path: join(SHOTS, `FAIL-${name.replace(/[^0-9A-Za-z가-힣]+/g, "_")}.png`), fullPage: true });
    } catch {
      /* 스크린샷 실패는 무시 */
    }
    return { failed: true, name };
  }
}

/** 화면 요청이 잠잠해질 때까지 기다리되(최대 10초) 끝내 잠잠해지지 않아도 단계를 실패시키지 않는다: 알림 종 같은 주기 요청이 있는 화면에서 networkidle 이 부하에 따라 간헐적으로 30초를 넘겨
 *  관계없는 단계가 실패하던 문제를 막는다. 뒤따르는 단언이 화면 내용을 직접 기다리므로 로딩 실패는 그쪽에서 잡힌다. */
async function settle(page) {
  await page.waitForLoadState("networkidle", { timeout: 10000 }).catch(() => {});
}

/** 링크를 눌러 새 화면으로 넘어가는 단계: 주소가 바뀌는지 먼저 확인하고(이동 실패), 안 바뀌면 한 번 더 누른 뒤에도 안 바뀌면 "이동 실패"로 알린다.
 *  화면 제목을 기다리다 시간 초과가 나면 이동이 안 된 것인지 화면 로딩이 느린 것인지 가려지지 않아서 만든 도우미다. */
async function clickToUrl(page, link, urlRe, what) {
  await link.click();
  try {
    await page.waitForURL(urlRe, { timeout: 10000 });
  } catch {
    await link.click(); // 클릭이 화면 갱신(재렌더)과 겹쳐 먹히지 않은 경우를 한 번 더 시도한다
    try {
      await page.waitForURL(urlRe, { timeout: 10000 });
    } catch {
      throw new Error(`${what}: 링크를 두 번 눌러도 주소가 ${urlRe} 로 바뀌지 않음(현재 ${new URL(page.url()).pathname})`);
    }
  }
}

function assert(cond, msg) {
  if (!cond) throw new Error(msg);
}

/**
 * 이 사용자에게 보이는 메뉴 전체(GNB 모듈 이름 + 모듈마다의 SNB 항목 이름). 3층 내비는 SNB 가 현재 모듈 것만 그려서
 * 모듈 링크를 차례로 눌러 모아 오고, 끝나면 원래 주소로 돌아온다.
 */
async function menuLabels(page) {
  const start = page.url();
  const gnb = page.getByRole("navigation", { name: "모듈" });
  const modules = (await gnb.getByRole("link").allInnerTexts()).map((s) => s.trim());
  const out = [...modules];
  for (const m of modules) {
    await gnb.getByRole("link", { name: m, exact: true }).click();
    await gnb.locator('a[aria-current="true"]').filter({ hasText: m }).waitFor(); // 현재 모듈로 바뀐 뒤에 SNB 를 읽는다
    if (m === "홈") continue; // 홈은 SNB 가 없다
    const snb = page.locator(`nav.snb[aria-label="${m} 메뉴"]`);
    await snb.waitFor();
    out.push(...(await snb.locator("ul:not(.snb-modules) a").allInnerTexts()).map((t) => t.trim()));
  }
  await page.goto(start);
  await page.getByRole("heading", { level: 1 }).first().waitFor();
  return [...new Set(out)];
}
async function cardTitles(page) {
  return page.locator("[data-home-card]").evaluateAll((els) => els.map((e) => e.getAttribute("aria-label")));
}

/** API 호출(마스터 데이터 준비·PO 조회용). 세션 쿠키와 CSRF 토큰을 그대로 쓴다. */
async function api(who, method, path, body) {
  const { context } = await session(who);
  const me = await (await context.request.get(`${BASE}/api/me`)).json();
  const res = await context.request.fetch(`${BASE}${path}`, {
    method,
    headers: { "X-CSRF-Token": me.csrfToken, Origin: BASE, "Content-Type": "application/json" },
    data: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok()) throw new Error(`${method} ${path} → ${res.status()}`);
  return res.status() === 204 ? null : res.json();
}

// ---------------------------------------------------------------------------
console.log("--- 로그인 화면 ---");
let s = await step("로그인 화면에서 개발용 계정으로 로그인(staff)", async () => {
  const { page } = await session("staff");
  await page.goto(`${BASE}/`);
  await page.waitForURL(`${BASE}/login`);
  await page.getByRole("heading", { name: "로그인" }).waitFor();
  await page.getByRole("heading", { name: "개발용 계정" }).waitFor();
  await shot(page, "01-login");
  await page.getByRole("link", { name: /구매직원/ }).click();
  await page.waitForURL(`${BASE}/`);
  await page.getByRole("heading", { name: "홈" }).waitFor();
  await page.getByText("구매직원님, 안녕하세요.").first().waitFor().catch(() => {});
  assert((await page.getByText("구매직원님, 안녕하세요.").count()) > 0, "홈 인사말에 로그인한 사용자 이름이 없음");
});
const loginOk = s;

await step("첫 Tab 대상이 '본문으로 건너뛰기' 이고 Enter 후 포커스가 main", async () => {
  const { page } = await session("staff");
  await page.goto(`${BASE}/`);
  await page.getByRole("heading", { name: "홈" }).waitFor();
  await page.evaluate(() => (document.activeElement instanceof HTMLElement ? document.activeElement.blur() : null));
  await page.keyboard.press("Tab");
  const first = page.getByRole("link", { name: "본문으로 건너뛰기" });
  await first.waitFor();
  assert(await first.evaluate((el) => el === document.activeElement), "첫 Tab 대상이 본문으로 건너뛰기 링크가 아님");
  await page.keyboard.press("Enter");
  assert(await page.evaluate(() => document.activeElement?.id === "main"), "Enter 후 포커스가 main 이 아님");
}, { needs: loginOk });

// B30(AC-1080): 로그인 화면은 켜진 방식만 버튼으로 그리고 폭 390 에서 넘치지 않는다. 설정 응답을 가로채 방식 조합 3종을 본다.
console.log("\n--- 로그인 방식별 화면(B30) ---");
for (const [label, shotName, cfg, buttons] of [
  ["이메일만", "01b-login-email-only", { login: false, devIdp: false, emailLogin: true, passwordResetByMail: false, providers: [] }, []],
  ["이메일+구글+MS", "01c-login-google-ms", { login: false, devIdp: false, emailLogin: true, passwordResetByMail: true, providers: [{ id: "google", label: "Google 로 로그인", kind: "google" }, { id: "microsoft", label: "Microsoft 로 로그인", kind: "microsoft" }] }, ["Google 로 로그인", "Microsoft 로 로그인"]],
  ["SAML 포함 3종", "01d-login-saml", { login: false, devIdp: false, emailLogin: true, passwordResetByMail: true, providers: [{ id: "google", label: "Google 로 로그인", kind: "google" }, { id: "microsoft", label: "Microsoft 로 로그인", kind: "microsoft" }, { id: "saml", label: "회사 SSO", kind: "saml" }] }, ["Google 로 로그인", "Microsoft 로 로그인", "회사 SSO"]],
]) {
  await step(`로그인 화면 ${label}: 켜진 방식만 보이고 폭 390 에서 넘치지 않는다`, async () => {
    const context = await browser.newContext({ viewport: { width: 390, height: 800 }, locale: "ko-KR" });
    context.setDefaultTimeout(30_000);
    const page = await context.newPage();
    try {
      await page.route("**/api/auth/config", (route) => route.fulfill({ json: cfg }));
      await page.goto(`${BASE}/login`);
      await page.getByRole("heading", { name: "로그인" }).waitFor();
      await page.getByLabel("비밀번호").waitFor();
      for (const b of buttons) await page.getByRole("link", { name: b }).waitFor();
      assert((await page.locator(".login-providers a.btn").count()) === buttons.length, "켜지지 않은 방식 버튼이 보임");
      const { sw, cw } = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth }));
      assert(sw <= cw, `로그인 화면이 가로로 넘침(${sw} > ${cw})`);
      await shot(page, shotName);
    } finally {
      await context.close();
    }
  });
}

console.log("\n--- 구매요청 작성·상신 ---");
let prSetup = await step("마스터 데이터 준비(거래처·품목, finance API)", async () => {
  await loginDirect("finance");
  await api("finance", "POST", "/api/vendors", { name: "한빛상사", businessNo: "123-45-67890", accountNo: "110-123-456789", phone: "010-1234-5678", email: "sales@hanbit.example" });
  await api("finance", "POST", "/api/items", { sku: "BOLT-M8", name: "볼트 M8", unit: "EA", safetyStock: 0, trackLot: false });
}, { needs: loginOk });

let prId = null;
const submitted = await step("구매요청 작성: 금액에 맞는 관련 규정이 보이고 상신된다", async () => {
  const { page } = await session("staff");
  await page.goto(`${BASE}/purchase-requests/new`);
  await page.getByRole("heading", { name: "새 구매요청" }).waitFor();
  await page.getByLabel("제목").fill("볼트 구매");
  await page.getByLabel("거래처").selectOption({ label: "한빛상사" });
  await page.getByLabel("구매 사유").fill("브라우저 스모크");
  await page.getByLabel("1번째 줄 품목").selectOption({ label: "볼트 M8 (BOLT-M8)" });
  await page.getByLabel("1번째 줄 수량").fill("10");
  await page.getByLabel("희망 납기 (선택)").fill("2099-12-31"); // 납기 내 입고로 거래처 평가에 반영된다
  await page.getByLabel("1번째 줄 단가").fill("1000000");
  const hints = page.getByRole("complementary", { name: "관련 규정" });
  await hints.getByText("제5조").first().waitFor(); // 1천만 원 이상 → 제5조
  await hints.getByText(/출처:/).first().waitFor().catch(() => {});
  assert((await hints.getByText(/출처:/).count()) > 0, "관련 규정에 출처가 없음");
  await hints.getByText("구매 결재 규정").first().waitFor().catch(() => {});
  assert((await hints.getByText("구매 결재 규정").count()) > 0, "관련 규정에 '구매 결재 규정' 이 없음");
  // 묶음: "이 금액의 결재선"에는 그 금액 구간 조항이 정확히 1건, 다른 구간 조항은 없고, 품목 묶음은 있어도 2건 이하.
  const tierItems = hints.locator("section").filter({ has: page.getByRole("heading", { name: "이 금액의 결재선" }) }).locator("li");
  await tierItems.first().waitFor();
  assert((await tierItems.count()) === 1, `금액 구간 묶음이 1건이 아님(${await tierItems.count()})`);
  const tierText = await tierItems.first().innerText();
  assert(tierText.includes("제5조") && !/제3조|제4조/.test(tierText), `금액 구간 조항이 제5조가 아님: ${tierText.slice(0, 60)}`);
  const refItems = hints.locator("section").filter({ has: page.getByRole("heading", { name: "품목 관련 참고" }) }).locator("li");
  assert((await refItems.count()) <= 2, `품목 묶음이 2건을 넘음(${await refItems.count()})`);
  // 금액을 100만 원 미만으로 바꾸면 제3조로 바뀐다(확인 뒤 원래 금액으로 되돌린다: 결재선이 3단계여야 한다).
  await page.getByLabel("1번째 줄 단가").fill("900000");
  await page.getByLabel("1번째 줄 수량").fill("1");
  await tierItems.getByText("제3조").first().waitFor();
  assert((await tierItems.count()) === 1 && (await tierItems.first().innerText()).includes("제3조"), "100만 원 미만인데 금액 구간 조항이 제3조 1건이 아님");
  await page.getByLabel("1번째 줄 수량").fill("10");
  await page.getByLabel("1번째 줄 단가").fill("1000000");
  await tierItems.getByText("제5조").first().waitFor();
  // B21: 결재선 미리보기(조직도로 만든 사람·직책·조직)가 보이고, 같은 결재선으로 상신된다.
  const linePreview = page.getByRole("complementary", { name: "결재선 미리보기" });
  await linePreview.getByRole("row", { name: /1 팀장 구매팀장 .*구매1팀/ }).waitFor();
  await linePreview.getByRole("row", { name: /2 부서장 구매부서장 .*구매팀/ }).waitFor();
  await linePreview.getByRole("row", { name: /3 재무 재무 담당/ }).waitFor();
  await shot(page, "02-purchase-form-related-policy");
  await page.getByRole("button", { name: "저장 후 상신" }).click();
  await page.waitForURL(/\/purchase-requests\/\d+$/);
  prId = page.url().split("/").at(-1);
  // 결재선·이력·첨부는 본문이 아니라 우측 패널("결재 정보")에 있다. 현재 단계는 aria-current="step".
  const info = page.getByRole("complementary", { name: "결재 정보" });
  await info.getByRole("heading", { name: "결재선" }).waitFor();
  await info.getByText(/상신 당시 조직: .*구매1팀/).waitFor(); // B21: 상신 당시 조직 표시
  await info.locator('li[aria-current="step"]').filter({ hasText: /1\. 팀장.*결재 차례.*구매팀장/ }).waitFor();
  await info.getByRole("listitem").filter({ hasText: /2\. 부서장.*대기.*구매부서장/ }).waitFor();
  await info.getByRole("listitem").filter({ hasText: /3\. 재무.*대기.*재무 담당/ }).waitFor(); // 1천만 원 이상: 팀장·부서장·재무 3단계 결재선
  await page.getByText("상신", { exact: true }).first().waitFor().catch(() => {});
  assert((await page.getByText("상신", { exact: true }).count()) > 0, "상신 뒤 상태가 '상신' 이 아님");
  await shot(page, "03-purchase-submitted");
}, { needs: prSetup });

console.log("\n--- 새 레이아웃 4화면(1440) ---");
await step("새 3층 레이아웃: 홈·구매요청 목록·결재함·결재 화면을 1440 폭으로 찍는다(lead)", async () => {
  const page = await loginDirect("lead");
  await page.setViewportSize({ width: 1440, height: 900 });
  try {
    await page.goto(`${BASE}/`);
    await page.getByRole("heading", { name: "홈" }).waitFor();
    assert((await page.locator(".gnb").count()) === 1, "GNB 가 없음");
    assert((await page.locator("nav.snb").count()) === 0, "홈에 SNB 가 그려짐");
    await settle(page);
    await shot(page, "L1-home-1440");
    await page.goto(`${BASE}/purchase-requests`);
    await page.getByRole("heading", { name: "구매요청" }).first().waitFor();
    assert((await page.locator("nav.snb").count()) === 1, "구매요청 목록에 SNB 가 없음");
    await settle(page);
    await shot(page, "L2-purchase-list-1440");
    // 툴바 정렬: 입력칸과 조회 단추의 하단·높이가 1px 이내(.field 아래 여백이 단추를 어긋나게 보이게 한 회귀 방지)
    const tb = await page.evaluate(() => {
      const bar = document.querySelector(".toolbar");
      const btn = [...bar.querySelectorAll("button")].find((b) => b.textContent.trim() === "조회");
      const inp = bar.querySelector("input, select");
      const r1 = btn.getBoundingClientRect();
      const r2 = inp.getBoundingClientRect();
      return { dBottom: Math.abs(r1.bottom - r2.bottom), dHeight: Math.abs(r1.height - r2.height) };
    });
    assert(tb.dBottom <= 1 && tb.dHeight <= 1, "툴바 조회 단추와 입력칸이 어긋남: " + JSON.stringify(tb));
    await page.goto(`${BASE}/approvals`);
    await page.getByRole("heading", { name: "결재함" }).waitFor();
    await settle(page);
    await shot(page, "L3-approval-inbox-1440");
    await page.goto(`${BASE}/approvals/${prId}`);
    await page.getByRole("button", { name: "승인", exact: true }).waitFor();
    await settle(page);
    await shot(page, "L4-approval-detail-1440");
  } finally {
    await page.setViewportSize({ width: 1280, height: 900 });
  }
}, { needs: submitted });

console.log("\n--- 결재 ---");
let approved = submitted;
for (const [who, label, n] of [["lead", "팀장", "04"], ["head", "부서장", "05"], ["finance", "재무", "06"]]) {
  approved = await step(`${label} 결재함에서 승인(${who})`, async () => {
    const page = await loginDirect(who);
    await page.goto(`${BASE}/approvals`);
    await page.getByRole("heading", { name: "결재함" }).waitFor();
    await page.getByRole("link", { name: "볼트 구매" }).click();
    await page.waitForURL(`${BASE}/approvals/${prId}`);
    await page.getByRole("button", { name: "승인", exact: true }).waitFor();
    await shot(page, `${n}-approval-${who}`);
    await page.getByLabel(/의견/).fill("스모크 승인");
    await page.getByRole("button", { name: "승인", exact: true }).click();
    await page.getByRole("status").filter({ hasText: "결재했습니다" }).waitFor();
    if (who === "finance") {
      await page.getByText(/발주 .* 이\(가\) 생성되었습니다/).waitFor();
      await shot(page, "07-approval-done-po-created");
    }
  }, { needs: approved });
}

console.log("\n--- 입고 ---");
let received = approved;
received = await step("입고 화면: 부분 입고(6/10) 뒤 잔량 입고(4)", async () => {
  const page = await loginDirect("finance");
  await page.goto(`${BASE}/receipts`);
  await page.getByRole("heading", { name: "입고", exact: true }).waitFor();
  // 이 흐름이 만든 발주를 번호로 골라 누른다(목록 첫 행이 우연히 다른 발주여도 흔들리지 않게). 발주 번호는 API 로 읽는다.
  const open = await api("finance", "GET", "/api/purchase-orders?status=open");
  const po = (Array.isArray(open) ? open : []).find((p) => String(p.prId) === String(prId));
  assert(po, `상신한 구매요청(${prId})의 발주를 찾지 못함: ${JSON.stringify(open).slice(0, 160)}`);
  const poButton = page.getByRole("button", { name: po.no, exact: true });
  await poButton.click();
  await page.getByLabel("볼트 M8 입고 수량").fill("6");
  await shot(page, "08-receipt-form");
  await page.getByRole("button", { name: "입고 처리" }).click();
  await page.getByRole("status").filter({ hasText: "입고를 처리했습니다" }).waitFor();
  // 목록의 그 발주 행이 부분 입고로 바뀐 것을 기다린 뒤(재조회가 끝난 뒤) 잔량을 입고한다.
  await page.getByRole("row", { name: new RegExp(`${po.no} .*부분 입고`) }).waitFor();
  await shot(page, "09-receipt-partial");
  await poButton.click();
  await page.getByLabel("볼트 M8 입고 수량").fill("4");
  await page.getByRole("button", { name: "입고 처리" }).click();
  await page.getByRole("status").filter({ hasText: "입고를 처리했습니다" }).waitFor();
  // 그 발주 행이 목록에서 사라지는 것으로 완료를 확인한다(다른 열린 발주가 있어도 흔들리지 않는다).
  await poButton.waitFor({ state: "detached" });
  await shot(page, "10-receipt-complete");
}, { needs: approved });

console.log("\n--- 재고 깊이(B24 단계 1): 출고·창고 이동·창고 관리 ---");
await step("출고(finance): 부서·창고·품목·수량으로 선입선출 배분을 받아 출고를 처리한다", async () => {
  const page = await loginDirect("finance");
  await page.goto(`${BASE}/inventory/issues`);
  await page.getByRole("heading", { name: "출고 전표 작성" }).waitFor();
  for (const n of ["재고", "출고", "창고 이동"]) await page.getByRole("link", { name: n, exact: true }).first().waitFor();
  await page.getByLabel("출고 부서").selectOption({ index: 1 });
  await page.getByLabel("출고 창고").selectOption({ index: 1 });
  await page.getByLabel("품목 찾기").fill("볼트");
  await page.getByLabel("품목 선택").selectOption({ index: 1 });
  await page.getByLabel("출고 수량", { exact: true }).fill("2");
  await page.getByRole("button", { name: "로트 배분 계산" }).click();
  await page.getByText(/합계 2 \/ 출고 수량 2/).waitFor();
  await shot(page, "62-stock-issue");
  await page.getByRole("button", { name: "출고 처리" }).click();
  await page.getByRole("status").filter({ hasText: /출고를 처리했습니다. 전표 번호 OUT-/ }).waitFor();
  await page.getByRole("cell", { name: /OUT-\d{4}-\d{4}/ }).first().waitFor();
}, { needs: received });
await step("창고 이동(finance): 창고를 등록하고 재고 기록 미리보기를 본 뒤 이동한다", async () => {
  const page = await loginDirect("finance");
  await page.goto(`${BASE}/inventory/transfers`);
  await page.getByRole("heading", { name: "이동 전표 작성" }).waitFor();
  await page.getByLabel("창고 코드").fill("WH-02");
  await page.getByLabel("이름", { exact: true }).fill("물류센터");
  await page.getByRole("button", { name: "창고 등록" }).click();
  await page.getByRole("status").filter({ hasText: "창고를 등록했습니다" }).waitFor();
  await page.getByLabel("출발 창고 선택").selectOption({ label: "본사 창고" });
  await page.getByLabel("도착 창고 선택").selectOption({ label: "물류센터" });
  await page.getByLabel("품목 찾기").fill("볼트");
  await page.getByLabel("품목 선택").selectOption({ index: 1 });
  await page.getByLabel("로트 선택").selectOption({ index: 1 });
  await page.getByRole("button", { name: "품목 줄 추가" }).click();
  await page.getByLabel(/이동 수량/).fill("3");
  await page.getByRole("button", { name: "재고 기록 미리보기" }).click();
  await page.getByRole("cell", { name: "이동 입고" }).waitFor();
  await shot(page, "63-stock-transfer");
  await page.getByRole("button", { name: "이동 처리" }).click();
  await page.getByRole("status").filter({ hasText: /이동을 처리했습니다. 전표 번호 MV-/ }).waitFor();
  await page.getByRole("cell", { name: /MV-\d{4}-\d{4}/ }).first().waitFor();
}, { needs: received });
await step("법인 간 재고 이동(B51 4단계 finance): 고를 수 있는 법인이 하나면 창고 이동 화면에 다른 법인 묶음·법인 간 표시·안내가 없고, 도착 후보 조회가 비어 있으며, 수불부 행에 법인 간 표시가 없고, 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("finance");
  const targets = await api("finance", "GET", "/api/stock-transfers?targets=1");
  assert(Array.isArray(targets) && targets.length === 0, "고를 수 있는 법인이 하나인데 다른 법인 도착 후보가 있음");
  const recent = await api("finance", "GET", "/api/stock-transfers?limit=10");
  assert(recent.length > 0 && recent.every((t) => t.intercompany === undefined && t.fromEntity === undefined), "같은 법인 이동 응답에 법인 간 표시가 있음");
  await page.goto(`${BASE}/inventory/transfers`);
  await page.getByRole("heading", { name: "이동 전표 작성" }).waitFor();
  await page.getByRole("cell", { name: /MV-\d{4}-\d{4}/ }).first().waitFor();
  await page.getByLabel("도착 창고 선택").selectOption({ label: "물류센터" });
  assert((await page.locator("optgroup").count()) === 0, "법인이 하나인데 다른 법인 묶음이 보임");
  assert((await page.getByTestId("intercompany-notice").count()) === 0, "같은 법인 이동에 법인 간 이동 안내가 보임");
  const body = await page.locator("main").innerText();
  assert(!/법인 간|intercompany|cost_amount|entity/.test(body), "창고 이동 화면에 법인 간 표시·내부 코드가 보임");
  await shot(page, "63b-stock-transfer-single-entity");
  await narrowCheck(page, "63c-stock-transfer-narrow", async () => {
    await page.getByRole("heading", { name: "이동 전표 작성" }).waitFor();
  });
}, { needs: received });
await step("재고 탭: 직원에게는 출고·창고 이동 탭이 보이지 않는다(staff)", async () => {
  const page = await loginDirect("staff");
  await page.goto(`${BASE}/inventory`);
  await page.getByRole("heading", { name: "재고" }).first().waitFor();
  assert((await page.getByRole("link", { name: "출고", exact: true }).count()) === 0, "직원에게 출고 탭이 보임");
  assert((await page.getByRole("link", { name: "창고 이동", exact: true }).count()) === 0, "직원에게 창고 이동 탭이 보임");
}, { needs: received });

await step("재고 화면 창고 선택(staff): 품목을 열고 창고를 고르면 그 창고의 재고만 보인다", async () => {
  const page = await loginDirect("staff");
  await page.goto(`${BASE}/inventory`);
  await page.getByLabel("품목 검색").fill("볼트");
  await page.getByRole("button", { name: /볼트 M8/ }).click();
  await page.getByText(/창고별: .*물류센터 3/).waitFor();
  await page.getByLabel("재고 창고 선택").selectOption({ label: "물류센터" });
  await page.getByText("이 창고의 재고").waitFor();
  await page.getByText("3", { exact: true }).first().waitFor();
  await shot(page, "66-stock-by-warehouse");
}, { needs: received });

console.log("\n--- 재고 깊이(B24 단계 2): 수불부·재고 실사 ---");
await step("수불부(staff): 품목을 골라 조회하면 기초·입고·출고·기말이 맞고 직원에게도 수불부 탭이 보인다", async () => {
  const page = await loginDirect("staff");
  await page.goto(`${BASE}/inventory/ledger`);
  await page.getByRole("heading", { name: "수불부" }).waitFor();
  await page.getByRole("button", { name: "조회" }).click();
  await page.getByText("품목을 고르세요. 수불부는 품목마다 봅니다.").waitFor();
  await page.getByLabel("품목 찾기").fill("볼트");
  await page.getByLabel("수불부 품목 선택").selectOption({ index: 1 });
  await page.getByLabel("시작일").fill("2020-01-01");
  await page.getByRole("button", { name: "조회" }).click();
  await page.getByText(/기초 0 \+ 입고 .* − 출고 .* = 기말/).waitFor();
  await page.getByRole("cell", { name: "이동 입고" }).first().waitFor();
  await shot(page, "64-stock-ledger");
  assert((await page.getByRole("link", { name: "실사", exact: true }).count()) === 0, "직원에게 실사 탭이 보임");
  assert((await page.getByRole("columnheader", { name: "단가" }).count()) === 0, "직원에게 단가 열이 보임");
  assert((await page.getByRole("columnheader", { name: "금액" }).count()) === 0, "직원에게 금액 열이 보임");
}, { needs: received });
await step("수불부 금액(finance): 입고 단가와 출고 평균 단가 금액이 단가·금액 열에 나오고 재고 화면에 평균 단가·재고 금액이 보인다", async () => {
  const page = await loginDirect("finance");
  await page.goto(`${BASE}/inventory/ledger`);
  await page.getByLabel("품목 찾기").fill("볼트");
  await page.getByLabel("수불부 품목 선택").selectOption({ index: 1 });
  await page.getByLabel("시작일").fill("2020-01-01");
  await page.getByRole("button", { name: "조회" }).click();
  await page.getByRole("columnheader", { name: "단가" }).waitFor();
  await page.getByRole("columnheader", { name: "금액" }).waitFor();
  await page.getByRole("cell", { name: "1,000,000원" }).first().waitFor();
  await shot(page, "80-ledger-valuation");
  await page.goto(`${BASE}/inventory`);
  await page.getByLabel("품목 검색").fill("볼트");
  await page.getByRole("button", { name: /볼트 M8/ }).click();
  await page.getByText(/평균 단가 1,000,000원 · 재고 금액 .*원\(이동평균, 공급가액 기준\)/).waitFor();
}, { needs: received });
await step("월 마감 탭: 재고 조회 역할에게 보이고(수량만), 마감한 달이 없으면 안내가 나오며, 마감 단추는 마감 권한이 있을 때만 그려진다", async () => {
  const staff = await loginDirect("staff");
  await staff.goto(`${BASE}/inventory/close`);
  await staff.getByRole("heading", { name: "월 마감" }).waitFor();
  await staff.getByText("아직 마감한 달이 없습니다. 이번 달이 끝나면 지난 달을 마감할 수 있습니다.").waitFor();
  assert((await staff.getByRole("button", { name: "이 달 마감" }).count()) === 0, "직원에게 마감 단추가 보임");
  assert((await staff.getByText("기말 재고 금액").count()) === 0, "직원에게 금액 열이 보임");
  const fin = await loginDirect("finance");
  await fin.goto(`${BASE}/inventory/close`);
  await fin.getByRole("heading", { name: "월 마감" }).waitFor();
  await fin.getByRole("link", { name: "월 마감", exact: true }).waitFor();
  await shot(fin, "81-month-close");
}, { needs: received });
await step("월별 재고 현황 탭: 월·창고·검색 조건과 내보내기 단추가 보이고, 금액 열은 평가 조회 권한이 있는 역할에게만 보인다", async () => {
  const staff = await loginDirect("staff");
  await staff.goto(`${BASE}/inventory/month-stock`);
  await staff.getByRole("heading", { name: "월별 재고 현황" }).waitFor();
  await staff.getByRole("button", { name: "조회" }).click();
  await staff.getByText(/이 조건에 맞는 재고 기록이 없습니다/).waitFor();
  assert((await staff.getByRole("columnheader", { name: "기말 금액" }).count()) === 0, "직원에게 금액 열이 보임");
  const fin = await loginDirect("finance");
  await fin.goto(`${BASE}/inventory/month-stock`);
  await fin.getByRole("heading", { name: "월별 재고 현황" }).waitFor();
  await fin.getByLabel("월", { exact: true }).selectOption({ index: 0 }); // 이번 달(마감 전 잠정)
  await fin.getByRole("button", { name: "조회" }).click();
  await fin.getByText("마감 전 잠정 값입니다").waitFor();
  await fin.getByRole("columnheader", { name: "기말 금액" }).waitFor();
  await shot(fin, "82-month-stock");
}, { needs: received });
await step("부재·대결 설정(B29): 결재자는 사유·대결자·기간으로 부재를 등록하고, 직원에게는 화면이 없다(lead·staff)", async () => {
  const lead = await loginDirect("lead");
  await lead.goto(`${BASE}/approvals/absence`);
  await lead.getByRole("heading", { name: "부재·대결 설정" }).waitFor();
  await lead.getByLabel("부재 사유").selectOption("vacation");
  await lead.getByLabel("대결자").selectOption({ index: 1 });
  const fmt = (d) => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
  await lead.getByLabel("부재 시작").fill(fmt(new Date(Date.now() + 60000)));
  await lead.getByLabel("부재 종료").fill(fmt(new Date(Date.now() + 2 * 86400000)));
  await lead.getByRole("button", { name: "부재 등록" }).click();
  await lead.getByRole("cell", { name: /부재 · 휴가/ }).waitFor();
  await shot(lead, "86-absence");
  const staff = await loginDirect("staff");
  await staff.goto(`${BASE}/approvals/absence`);
  await staff.getByText("권한이 없습니다", { exact: false }).first().waitFor();
});
await step("문서 검색(B29): 조건 없이 검색하면 안내가 나오고, 검색어로 내 문서를 찾으며 결과 표와 건수가 보인다(staff)", async () => {
  const page = await loginDirect("staff");
  await page.goto(`${BASE}/docs/search`);
  await page.getByRole("heading", { name: "문서 검색" }).waitFor();
  await page.getByRole("button", { name: "검색" }).click();
  await page.getByText("검색어나 조건을 하나 이상 입력하세요.").waitFor();
  await page.getByLabel("검색어").fill("볼트");
  await page.getByRole("button", { name: "검색" }).click();
  await page.getByRole("columnheader", { name: "문서 번호" }).waitFor();
  await page.getByText(/검색 결과 \d+건|1–\d+ \/ 전체 \d+건/).waitFor();
  await shot(page, "87-doc-search");
}, { needs: received });
await step("통합 검색(B39): GNB 검색에 볼트를 넣고 Enter 하면 /search?q= 로 가서 구매요청 절에 PR-000001 이 보이고 사람 절은 결과 행이나 빈 상태 문구를 보인다(staff)", async () => {
  const page = await loginDirect("staff");
  await page.goto(`${BASE}/`);
  const box = page.getByRole("searchbox", { name: "통합 검색" });
  await box.fill("볼트");
  await box.press("Enter");
  await page.waitForURL(/\/search\?q=/);
  assert(decodeURIComponent(new URL(page.url()).searchParams.get("q") ?? "") === "볼트", `검색어가 주소에 실리지 않음: ${page.url()}`);
  const pr = page.getByRole("region", { name: "구매요청" });
  await pr.getByText("PR-000001").first().waitFor();
  // 모든 역할이 org:read 를 가지므로 직원에게도 사람 절이 보인다. 권한이 없을 때 절이 빠지는 분기는 vitest(web/src/search/search.test.tsx)가 덮는다.
  const people = page.getByRole("region", { name: "사람" });
  await people.waitFor();
  await people.getByText(/검색 결과가 없습니다|\S/).first().waitFor();
  const peopleText = await people.innerText();
  assert(/검색 결과가 없습니다/.test(peopleText) || (await people.getByRole("row").count()) + (await people.getByRole("listitem").count()) > 0, `사람 절에 결과 행도 빈 상태 문구도 없음: ${peopleText.slice(0, 120)}`);
  await shot(page, "87b-search");
}, { needs: received });
await step("재무(B31): 재무는 계정과목과 전표를 보고 차대 합계를 확인하며 입력하고, 직원에게는 재무 메뉴가 없다(finance·staff)", async () => {
  const fin = await loginDirect("finance");
  await fin.goto(`${BASE}/finance/accounts`);
  await fin.getByRole("heading", { name: "계정과목" }).waitFor();
  await fin.getByRole("button", { name: "계정 추가" }).waitFor();
  await fin.getByText("외상매입금").first().waitFor();
  await shot(fin, "88-finance-accounts");
  await fin.goto(`${BASE}/finance/journals/new`);
  await fin.getByRole("heading", { name: "새 전표 입력" }).waitFor();
  await fin.getByLabel("1번째 줄 계정").selectOption("1210");
  await fin.getByLabel("1번째 줄 금액").fill("110000");
  await fin.getByLabel("2번째 줄 계정").selectOption("2110");
  await fin.getByLabel("2번째 줄 금액").fill("110000");
  await fin.getByText("차대 일치").waitFor();
  await shot(fin, "89-finance-journal-new");
  await fin.goto(`${BASE}/finance/journals`);
  await fin.getByRole("heading", { name: "전표" }).first().waitFor();
  await fin.goto(`${BASE}/finance/journal-queue`);
  await fin.getByRole("heading", { name: "전표 대기" }).waitFor();
  await fin.getByText(/전표 대기 건이 없습니다|불일치 확정|규칙 계정 사용 불가/).first().waitFor();
  await shot(fin, "90-finance-journal-queue");
  await fin.goto(`${BASE}/finance/payables`);
  await fin.getByRole("heading", { name: "매입 채무" }).waitFor();
  await fin.getByText(/미지급 채무가 없습니다|계정별 원장 외상매입금 잔액과 일치|계정별 원장 잔액/).first().waitFor();
  await shot(fin, "91-finance-payables");
  await fin.goto(`${BASE}/finance/trial-balance`);
  await fin.getByRole("heading", { name: "시산표" }).waitFor();
  await fin.getByText(/차변·대변 합계 일치|이 달에는 승인된 전표가 없습니다/).first().waitFor();
  await shot(fin, "92-finance-trial-balance");
  await fin.goto(`${BASE}/finance/ledger?account=2110`);
  await fin.getByRole("heading", { name: "계정별 원장" }).waitFor();
  await fin.getByText("이월 잔액").waitFor();
  await shot(fin, "93-finance-ledger");
  await fin.goto(`${BASE}/finance/payments`);
  await fin.getByRole("heading", { name: "지급 요청" }).first().waitFor();
  await fin.getByText(/조건에 맞는 지급 요청이 없습니다|지급 합계/).first().waitFor();
  await shot(fin, "94-finance-payments");
  await fin.goto(`${BASE}/finance/payments/new`);
  await fin.getByRole("heading", { name: "새 지급 요청" }).waitFor();
  await shot(fin, "95-finance-payment-new");
  await fin.goto(`${BASE}/finance/payment-calendar`);
  await fin.getByRole("heading", { name: "지급 일정표" }).waitFor();
  await shot(fin, "96-finance-payment-calendar");
  await fin.goto(`${BASE}/finance/fund-daily`);
  await fin.getByRole("heading", { name: "자금 일보" }).waitFor();
  await fin.getByText(/이월 잔액|이 기간에는 입출금이 없습니다/).first().waitFor();
  await shot(fin, "97-finance-fund-daily");
  await fin.goto(`${BASE}/finance/bank-accounts`);
  await fin.getByRole("heading", { name: "자금 계좌" }).waitFor();
  await fin.getByText(/등록된 자금 계좌가 없습니다|국민은행/).first().waitFor();
  await shot(fin, "98-finance-bank-accounts");
  await fin.goto(`${BASE}/finance/cards`);
  await fin.getByRole("heading", { name: "법인카드 관리" }).waitFor();
  await fin.getByText(/등록된 카드가 없습니다|카드 등록/).first().waitFor();
  await fin.getByRole("heading", { name: "카드 내역 업로드" }).waitFor();
  await shot(fin, "99-finance-cards");
  const staffMe = await (await (await session("staff")).context.request.get(`${BASE}/api/me`)).json();
  await api("finance", "POST", "/api/finance/cards", { alias: "스모크 법인카드", issuer: "format_a", last4: "4321", holderId: staffMe.user.id });
  await fin.reload();
  await fin.getByRole("heading", { name: "법인카드 관리" }).waitFor();
  await fin.getByRole("button", { name: "소지자 변경" }).first().click();
  await fin.getByText(/이후 업로드되는 내역부터 적용됩니다/).waitFor();
  await fin.getByLabel("새 소지자(이름 검색)").waitFor();
  await fin.getByRole("heading", { name: "소지자 변경 이력" }).waitFor();
  await shot(fin, "99b-finance-card-holder-change");
  await fin.goto(`${BASE}/finance/cards/mine`);
  await fin.getByRole("heading", { name: "내 카드 내역" }).waitFor();
  await fin.getByText(/소명할 내역이 없습니다|소명하기/).first().waitFor();
  await shot(fin, "100-finance-cards-mine");
  const adm = await loginDirect("admin");
  await adm.goto(`${BASE}/admin/company`);
  await adm.getByRole("heading", { name: "회사 설정" }).waitFor();
  await adm.getByLabel("회사명").waitFor();
  await adm.getByRole("heading", { name: "로고·직인" }).waitFor();
  await adm.getByRole("heading", { name: "부가세율" }).waitFor();
  await adm.getByRole("heading", { name: "시간대·영업일 달력" }).waitFor();
  await shot(adm, "101-admin-company");
  // 화면에 영문 코드·형식 문구가 새지 않고(시간대·통화·날짜 형식·이미지 형식), 파일 선택 버튼이 한국어이며, 폭 390 에서 넘치지 않는다.
  const bodyText = await adm.locator("main, body").first().innerText();
  for (const bad of ["Asia/Seoul", "KRW", "YYYY-MM-DD", "JPEG", "Choose File", "No file chosen"]) assert(!bodyText.includes(bad), `회사 설정 화면에 영문 표기 "${bad}" 가 보임`);
  await adm.getByRole("button", { name: "직인 파일 선택" }).waitFor();
  await narrowCheck(adm, "101b-admin-company-narrow", async () => {
    await adm.getByRole("heading", { name: "로고·직인" }).waitFor();
  });
  await adm.goto(`${BASE}/admin/import/reconcile`);
  await adm.getByRole("heading", { name: "기초 데이터 대조" }).waitFor();
  await adm.getByRole("table", { name: "기초 데이터 대조" }).waitFor();
  await shot(adm, "102-import-reconcile");
  await fin.goto(`${BASE}/finance/vat-purchase`);
  await fin.getByRole("heading", { name: "부가세 매입 집계" }).waitFor();
  await fin.getByRole("heading", { name: "합계와 검산" }).waitFor();
  await shot(fin, "101-finance-vat-purchase");
  const staff = await loginDirect("staff");
  await staff.goto(`${BASE}/`);
  await staff.getByRole("heading", { name: "홈" }).waitFor();
  assert((await staff.getByRole("link", { name: "전표", exact: true }).count()) === 0, "직원에게 전표 메뉴가 보임");
}, { needs: received });
await step("목록 내보내기(B28): 구매요청 목록의 XLSX·CSV 가 내려받아지고 머리글이 화면과 같으며, 예산·입고 이력·문서함에도 단추가 있다(staff·finance)", async () => {
  const staff = await loginDirect("staff");
  await staff.goto(`${BASE}/purchase-requests`);
  await staff.getByRole("heading", { name: "구매요청" }).first().waitFor();
  await staff.locator("tbody tr").first().waitFor();
  const [xl] = await Promise.all([staff.waitForEvent("download"), staff.getByRole("button", { name: "엑셀(XLSX)" }).click()]);
  assert(/^구매요청_\d{8}_\d{4}\.xlsx$/.test(xl.suggestedFilename()), `구매요청 파일 이름이 이상함: ${xl.suggestedFilename()}`);
  const bytes = readFileSync(await xl.path());
  assert(bytes.length > 500 && bytes.subarray(0, 2).toString() === "PK", "XLSX 파일이 zip 형식이 아님");
  const [dl] = await Promise.all([staff.waitForEvent("download"), staff.getByRole("button", { name: "CSV" }).click()]);
  const csv = readFileSync(await dl.path(), "utf8");
  assert(csv.startsWith("﻿번호,제목,요청자,소속,금액(원),상태,작성일"), `CSV 머리글이 이상함: ${csv.slice(0, 50)}`);
  assert(csv.split("\r\n").filter(Boolean).length >= 2, "구매요청 파일에 행이 없음");
  await shot(staff, "83-export-purchase-list");
  await staff.goto(`${BASE}/docs`);
  await staff.getByRole("heading", { name: "전자결재 문서함" }).waitFor();
  await staff.getByRole("button", { name: "CSV" }).waitFor();
  const fin = await loginDirect("finance");
  await fin.goto(`${BASE}/budget`);
  await fin.getByRole("heading", { name: "예산 현황" }).waitFor();
  await fin.getByRole("button", { name: "CSV" }).waitFor();
  await fin.goto(`${BASE}/receipts`);
  await fin.getByRole("heading", { name: "입고 이력" }).waitFor();
  await fin.getByRole("button", { name: "CSV" }).first().waitFor();
}, { needs: received });
await step("업무 보고서(B28): 구매 실적 조회·내보내기, 결재 소요 시간, 거래처 탭이 보이고 직원에게는 메뉴가 없으며 결재자에게는 거래처 탭이 없다(finance·staff·lead)", async () => {
  const fin = await loginDirect("finance");
  await fin.goto(`${BASE}/reports`);
  await fin.getByRole("heading", { name: "보고서" }).waitFor();
  await fin.getByRole("link", { name: "거래처별 실적·평가" }).waitFor();
  await fin.getByRole("columnheader", { name: "집행률" }).waitFor();
  await fin.getByText("계산 기준").first().waitFor();
  await shot(fin, "84-report-purchase");
  const [xl] = await Promise.all([fin.waitForEvent("download"), fin.getByRole("button", { name: "엑셀(XLSX)" }).click()]);
  assert(/^구매실적_\d{8}_\d{4}\.xlsx$/.test(xl.suggestedFilename()), `구매 실적 파일 이름이 이상함: ${xl.suggestedFilename()}`);
  assert(readFileSync(await xl.path()).subarray(0, 2).toString() === "PK", "구매 실적 XLSX 가 zip 형식이 아님");
  await fin.getByRole("link", { name: "결재 소요 시간" }).click();
  await fin.getByRole("heading", { name: "지연 상위 건" }).waitFor();
  await fin.getByText("계산 기준").first().click(); // 접힌 설명을 연다
  await fin.getByText("주말·공휴일을 빼지 않은 실제 경과 시간입니다", { exact: false }).waitFor();
  await shot(fin, "85-report-approvals");
  await fin.getByRole("link", { name: "거래처별 실적·평가" }).click();
  await fin.getByRole("button", { name: "조회" }).click();
  await fin.getByRole("columnheader", { name: "발주 금액" }).waitFor();
  const staff = await loginDirect("staff");
  await staff.goto(`${BASE}/`);
  await staff.getByRole("heading", { name: "홈" }).waitFor();
  assert((await staff.getByRole("link", { name: "보고서", exact: true }).count()) === 0, "직원에게 보고서 메뉴가 보임");
  const lead = await loginDirect("lead");
  await lead.goto(`${BASE}/reports/purchase`);
  await lead.getByRole("columnheader", { name: "집행률" }).or(lead.getByText("이 기간에 집계할 구매가 없습니다")).first().waitFor();
  assert((await lead.getByRole("link", { name: "거래처별 실적·평가" }).count()) === 0, "결재자에게 거래처 탭이 보임");
}, { needs: received });
await step("재고 실사(finance): 실사 표를 만들어 실제 수량을 넣고 차이 없음으로 종료한다", async () => {
  const page = await loginDirect("finance");
  await page.goto(`${BASE}/inventory/counts`);
  await page.getByRole("heading", { name: "재고 실사" }).waitFor();
  await page.getByLabel("실사 창고 선택").selectOption({ label: "본사 창고" });
  await page.getByRole("button", { name: "실사 표 만들기" }).click();
  const row = page.getByRole("row", { name: /볼트 M8/ });
  await row.waitFor();
  const system = (await row.getByRole("cell").nth(2).innerText()).trim();
  await row.getByRole("textbox").fill(system.replace(/,/g, ""));
  await shot(page, "65-stock-count");
  await page.getByRole("button", { name: "차이 확인으로" }).click();
  await page.getByText("차이가 없어 조정할 내용이 없습니다.").waitFor();
  await page.getByRole("button", { name: "차이 없음으로 종료" }).click();
  await page.getByRole("status").filter({ hasText: "차이가 없어 실사를 종료했습니다" }).waitFor();
  await page.getByRole("cell", { name: "차이 없음 종료" }).first().waitFor();
}, { needs: received });

console.log("\n--- 구매 기능 깊이(B25 단계 1): 부서 예산·초과 시 재무 단계 ---");
await step("예산 현황(finance): 구매팀 월 예산을 등록하면 표에 사용률 배지가 보인다", async () => {
  const page = await loginDirect("finance");
  await page.goto(`${BASE}/budget`);
  await page.getByRole("heading", { name: "예산 현황" }).waitFor();
  await page.locator("#bd-dept").selectOption({ label: "구매팀" });
  await page.locator("#bd-amount").fill("12,000,000"); // 쉼표가 든 금액도 받는다
  await page.locator("#bd-reason").fill("스모크 시험 예산");
  await page.getByRole("button", { name: "저장", exact: true }).click();
  await page.getByRole("status").filter({ hasText: /예산을 저장했습니다/ }).waitFor();
  await page.getByRole("table").first().getByRole("row", { name: /구매팀 .*12,000,000원.*한도 근접/ }).waitFor();
  await page.getByRole("cell", { name: "신규 등록" }).first().waitFor();
  await shot(page, "70-budget");
}, { needs: received });
await step("예산 초과 요청(staff→lead): 작성 화면에 초과 안내와 재무 단계 추가가 보이고 상세에 예산 초과 배지가 붙는다", async () => {
  const page = await loginDirect("staff");
  await page.goto(`${BASE}/purchase-requests/new`);
  await page.getByRole("heading", { name: "새 구매요청" }).waitFor();
  await page.getByLabel("제목").fill("예산 초과 시험");
  await page.getByLabel("거래처").selectOption({ label: "한빛상사" });
  await page.getByLabel("1번째 줄 품목").selectOption({ label: "볼트 M8 (BOLT-M8)" });
  await page.getByLabel("1번째 줄 수량").fill("1");
  await page.getByLabel("1번째 줄 단가").fill("3000000");
  await page.getByLabel("희망 납기 (선택)").fill("2099-12-31");
  const card = page.getByRole("complementary", { name: "예산 확인" });
  await card.getByText(/예산을 .*원 넘습니다/).waitFor();
  const preview = page.getByRole("complementary", { name: "결재선 미리보기" });
  await preview.getByText("예산 초과로 추가").waitFor();
  await preview.getByText("예산 초과로 재무 단계를 더했습니다.").waitFor();
  await shot(page, "71-budget-check-over");
  await page.getByRole("button", { name: "저장 후 상신" }).click();
  await page.waitForURL(/\/purchase-requests\/\d+$/);
  await page.getByText("예산 초과 요청").first().waitFor();
  await page.getByText("2099-12-31").waitFor();
  await page.getByText(/이 요청이 .*원 초과했습니다\. 예산 초과로 재무 단계가 더해졌습니다/).waitFor();
  await shot(page, "72-budget-over-detail");
  const id = page.url().split("/").at(-1);
  const lead = await loginDirect("lead");
  await lead.goto(`${BASE}/approvals/${id}`);
  await lead.getByLabel(/의견/).fill("예산 시험 반려");
  await lead.getByRole("button", { name: "반려", exact: true }).click(); // 뒤 단계에 영향이 없도록 반려로 끝낸다
  await lead.getByRole("status").filter({ hasText: "결재했습니다" }).waitFor();
}, { needs: received });
await step("예산 현황 권한: 감사는 표를 보되 수정 폼이 없고 직원 메뉴에는 예산 현황이 없다", async () => {
  const aud = await loginDirect("auditor");
  await aud.goto(`${BASE}/budget`);
  await aud.getByRole("heading", { name: "예산 현황" }).waitFor();
  assert((await aud.getByRole("region", { name: "예산 등록·수정" }).count()) === 0, "감사에게 예산 수정 폼이 보임");
  const staff = await loginDirect("staff");
  await staff.goto(`${BASE}/`);
  assert((await staff.getByRole("link", { name: "예산 현황" }).count()) === 0, "직원에게 예산 현황 메뉴가 보임");
  // 뒤 단계의 구매 흐름이 예산 초과로 바뀌지 않도록 예산을 크게 되돌린다(수정 이력에 남는다).
  await api("finance", "PUT", "/api/budgets", { departmentId: (await api("finance", "GET", "/api/stock-issues/departments")).find((d) => d.name === "구매팀").id, month: new Date(Date.now() + 9 * 3600 * 1000).toISOString().slice(0, 7), amount: 500000000, reason: "스모크 시험 뒤 복원" });
}, { needs: received });

console.log("\n--- 구매 기능 깊이(B25 단계 2): 견적서 첨부·거래처별 비교 ---");
await step("견적 비교(staff): 견적서 3건을 올리면 AI 가 값을 읽고 비교 표에 최저가가 나오며, 최저가가 아닌 거래처를 고르면 사유를 요구한다", async () => {
  await api("finance", "POST", "/api/vendors", { name: "대한오피스", businessNo: "111-22-33333", accountNo: "110-000-111111", phone: "02-111-2222", email: "office@daehan.example" });
  await api("finance", "POST", "/api/vendors", { name: "스마트유통", businessNo: "444-55-66666", accountNo: "110-000-222222", phone: "02-333-4444", email: "sales@smart.example" });
  const quote = (supplier, no, p1, p2, days) =>
    `견적서\n공급자: ${supplier}\n사업자번호: ${no}\n품목: A4 복사용지 75g | 수량 100 | 단가 ${p1} | 금액 ${p1 * 100}\n품목: 토너 카트리지 검정 | 수량 30 | 단가 ${p2} | 금액 ${p2 * 30}\n합계: ${p1 * 100 + p2 * 30}\n납기: 발주 후 ${days}일\n`;
  const page = await loginDirect("staff");
  await page.goto(`${BASE}/purchase-requests/new`);
  await page.getByRole("heading", { name: "새 구매요청" }).waitFor();
  await page.getByLabel("제목").fill("사무용품 견적 비교");
  await page.getByLabel("거래처").selectOption({ label: "한빛상사" });
  await page.getByLabel("1번째 줄 품목").selectOption({ label: "볼트 M8 (BOLT-M8)" });
  await page.getByLabel("1번째 줄 수량").fill("1");
  await page.getByLabel("1번째 줄 단가").fill("4670000");
  await page.getByRole("button", { name: "임시저장" }).click();
  await page.waitForURL(/\/purchase-requests\/\d+$/);
  const id = page.url().split("/").at(-1);
  await page.goto(`${BASE}/purchase-requests/${id}/edit`);
  await page.getByRole("link", { name: "견적 비교·견적서 올리기" }).click(); // 저장된 작성 중 요청의 수정 화면에서 견적 화면으로 간다
  await page.waitForURL(/\/quotes$/);
  await page.getByRole("heading", { name: "견적 비교" }).waitFor();
  await page.getByText("첨부된 견적서가 없습니다.").first().waitFor();
  for (const [name, body] of [
    ["한빛상사_견적서.txt", quote("한빛상사", "123-45-67890", 21500, 84000, 7)],
    ["대한오피스_견적서.txt", quote("대한오피스", "111-22-33333", 20000, 79000, 10)],
    ["스마트유통_견적서.txt", quote("스마트유통", "444-55-66666", 22000, 90000, 3)],
  ]) {
    await page.getByLabel("견적서 파일 선택").setInputFiles({ name, mimeType: "text/plain", buffer: Buffer.from(body, "utf8") });
    await page.getByRole("status").filter({ hasText: `${name} 파일에서 값을 읽었습니다` }).waitFor();
  }
  const row = (v) => page.getByRole("row", { name: new RegExp(`${v} 견적`) });
  await row("대한오피스").getByText("최저가", { exact: true }).waitFor();
  await row("스마트유통").getByText(/최저가보다 \+530,000원/).waitFor();
  await row("한빛상사").getByText("AI 읽음").waitFor();
  await shot(page, "73-quote-compare");
  await row("한빛상사").getByRole("button", { name: "이 거래처 선택" }).click();
  await page.getByRole("region", { name: "선택 사유" }).getByText(/선택한 한빛상사는 최저가 대한오피스보다 300,000원 비쌉니다\./).waitFor();
  await page.locator("#quote-reason").fill("기존 거래 관계와 납품 안정성 때문");
  await page.getByRole("button", { name: "사유 저장" }).click();
  await page.getByRole("status").filter({ hasText: "선택 사유를 저장했습니다" }).waitFor();
  await shot(page, "74-quote-reason");
  await page.goto(`${BASE}/purchase-requests/${id}`);
  const card = page.getByRole("complementary", { name: "견적 비교" });
  await card.getByText("견적서 3건").waitFor();
  await card.getByText("기존 거래 관계와 납품 안정성 때문").waitFor();
  await page.getByRole("button", { name: "상신", exact: true }).click();
  await page.getByText("견적 선택 사유: 기존 거래 관계와 납품 안정성 때문").waitFor();
  await shot(page, "75-quote-submitted");
  const lead = await loginDirect("lead");
  await lead.goto(`${BASE}/approvals/${id}`);
  await lead.getByRole("complementary", { name: "견적 비교" }).getByText("기존 거래 관계와 납품 안정성 때문").waitFor();
  await lead.getByLabel(/의견/).fill("견적 시험 반려");
  await lead.getByRole("button", { name: "반려", exact: true }).click(); // 뒤 단계에 영향이 없도록 반려로 끝낸다
  await lead.getByRole("status").filter({ hasText: "결재했습니다" }).waitFor();
}, { needs: received });

console.log("\n--- 구매 기능 깊이(B25 단계 3): 발주서 출력·거래처 평가 ---");
await step("발주서(finance): 입고가 끝난 발주의 발주서에 공급가액·부가세(10%)·합계와 결재란이 나오고, 직원에게는 사업자번호가 가려진다", async () => {
  const pos = await api("finance", "GET", "/api/purchase-orders?status=received");
  assert(Array.isArray(pos) && pos.length > 0, "입고 완료 발주가 없음");
  const page = await loginDirect("finance");
  await page.goto(`${BASE}/purchase-requests/${pos[0].prId}`);
  await clickToUrl(page, page.getByRole("link", { name: "발주서", exact: true }), /\/purchase-requests\/\d+\/po$/, "발주서 링크");
  await page.getByRole("heading", { name: "발 주 서" }).waitFor();
  await page.getByRole("table", { name: "결재란" }).first().waitFor();
  const total = pos[0].totalAmount;
  const vat = Math.floor(total / 10);
  await page.getByRole("row", { name: /공급가액/ }).getByText(`${total.toLocaleString("ko-KR")}원`).waitFor();
  await page.getByRole("row", { name: /부가세\(10%\)/ }).getByText(`${vat.toLocaleString("ko-KR")}원`).waitFor();
  await page.getByRole("row", { name: /합계/ }).getByText(`${(total + vat).toLocaleString("ko-KR")}원`).last().waitFor();
  await page.getByRole("table", { name: "공급자" }).getByText("123-45-67890").waitFor(); // 재무는 전체 값
  assert((await page.getByText(/일부만 보입니다/).count()) === 0, "재무에게 마스킹 안내가 보임");
  await shot(page, "76-po-print");
  const staff = await loginDirect("staff");
  await staff.goto(`${BASE}/purchase-requests/${pos[0].prId}/po`);
  await staff.getByRole("heading", { name: "발 주 서" }).waitFor();
  await staff.getByText(/사업자번호와 연락처는 일부만 보입니다/).waitFor();
  assert((await staff.getByText("123-45-67890").count()) === 0, "직원에게 사업자번호 전체가 보임");
  await shot(staff, "77-po-print-masked");
}, { needs: received });
await step("거래처 평가(finance): 기간을 조회하면 거래처별 납기 준수·판정 불일치가 나오고, 직원에게는 메뉴가 없다", async () => {
  const page = await loginDirect("finance");
  await page.goto(`${BASE}/vendors/rating`);
  await page.getByRole("heading", { name: "거래처 평가" }).waitFor();
  await page.getByLabel("시작일").fill("2020-01-01");
  await page.getByRole("button", { name: "조회" }).click();
  await page.getByRole("button", { name: "한빛상사" }).waitFor();
  await page.getByRole("columnheader", { name: "납기 준수율" }).waitFor();
  await page.getByRole("button", { name: "한빛상사" }).click();
  await page.getByRole("region", { name: "한빛상사 상세" }).waitFor();
  await shot(page, "78-vendor-rating");
  const aud = await loginDirect("auditor");
  await aud.goto(`${BASE}/vendors/rating`);
  await aud.getByRole("heading", { name: "거래처 평가" }).waitFor();
  const staff = await loginDirect("staff");
  await staff.goto(`${BASE}/`);
  assert((await staff.getByRole("link", { name: "거래처 평가" }).count()) === 0, "직원에게 거래처 평가 메뉴가 보임");
}, { needs: received });

console.log("\n--- 문서 검증(근거 강조) ---");
await step("문서 검증: 판정이 일치로 나오고 항목을 누르면 원문이 강조된다", async () => {
  const page = await loginDirect("finance");
  const pos = await api("finance", "GET", "/api/purchase-orders?status=received");
  assert(Array.isArray(pos) && pos.length > 0, "입고 완료 발주가 없음");
  const doc = "거래명세서\n공급자: 한빛상사\n사업자번호: 123-45-67890\n품목: 볼트 M8 | 수량 10 | 단가 1000000 | 금액 10000000\n합계: 10000000\n";
  await page.goto(`${BASE}/documents`);
  await page.getByRole("heading", { name: "문서 검증" }).waitFor();
  await page.getByLabel("발주 번호").fill(String(pos[0].id));
  await page.getByLabel("또는 문서 내용 붙여 넣기").fill(doc);
  await page.getByRole("button", { name: "판정 요청" }).click();
  const verdict = page.getByRole("region", { name: "판정 결과" });
  await verdict.getByTestId("outcome").waitFor();
  assert((await verdict.getByTestId("outcome").innerText()) === "일치", "판정이 일치가 아님");
  await shot(page, "11-document-verdict");
  const pick = verdict.locator("button[aria-pressed]");
  await pick.nth(1).waitFor().catch(() => {});
  assert((await pick.count()) >= 2, "근거를 누를 수 있는 항목이 2개 미만");
  await pick.nth(0).click();
  const mark = page.getByTestId("source-highlight");
  await mark.waitFor();
  const first = (await mark.innerText()).trim();
  assert(first.length > 0, "강조된 원문이 비어 있음");
  assert((await page.getByTestId("source-text").innerText()).includes(first), "강조 구간이 원문에 없음");
  await shot(page, "12-document-highlight-1");
  await pick.nth(1).click();
  await page.waitForFunction((prev) => document.querySelector('[data-testid="source-highlight"]')?.textContent?.trim() !== prev, first);
  await shot(page, "13-document-highlight-2");
}, { needs: received });

console.log("\n--- 조직도 기반 결재선: 올림·재지정·알림(B21) ---");
// 팀장이 올린 요청은 본인이 구매1팀 팀장이라 1단계가 부서장으로 올라간다. 미리보기 문구 → 상신 → 부서장 결재 → 상세의 "상신 당시 조직".
let liftedId = null;
const lifted = await step("팀장 요청: 결재선 미리보기에 올림 문구, 상신, 부서장 결재, 상세에 상신 당시 조직(lead→head)", async () => {
  const page = await loginDirect("lead");
  await page.goto(`${BASE}/purchase-requests/new`);
  await page.getByRole("heading", { name: "새 구매요청" }).waitFor();
  await page.getByLabel("제목").fill("올림 시험");
  await page.getByLabel("거래처").selectOption({ label: "한빛상사" });
  await page.getByLabel("1번째 줄 품목").selectOption({ label: "볼트 M8 (BOLT-M8)" });
  await page.getByLabel("1번째 줄 수량").fill("1");
  await page.getByLabel("1번째 줄 단가").fill("500000");
  const preview = page.getByRole("complementary", { name: "결재선 미리보기" });
  await preview.getByText(/요청자 본인이 구매1팀 팀장이라 한 단계 위로 올렸습니다\./).first().waitFor();
  await preview.getByRole("row", { name: /1 팀장 구매부서장 .*구매팀/ }).waitFor();
  await shot(page, "40-line-preview-lifted");
  await page.getByRole("button", { name: "저장 후 상신" }).click();
  await page.waitForURL(/\/purchase-requests\/\d+$/);
  liftedId = page.url().split("/").at(-1);
  const head = await loginDirect("head");
  await head.goto(`${BASE}/approvals/${liftedId}`);
  await head.getByText(/상신 당시 조직: .*구매1팀/).waitFor();
  await head.getByLabel(/의견/).fill("올림 반려");
  await head.getByRole("button", { name: "반려", exact: true }).click(); // 발주를 만들지 않도록 반려로 끝낸다
  await head.getByRole("status").filter({ hasText: "결재했습니다" }).waitFor();
  await head.getByText(/상신 당시 조직: .*구매1팀/).waitFor();
  await shot(head, "41-lifted-decided");
}, { needs: received });

// admin 이 결재자를 바꾼다: 직원 요청의 1단계(팀장)를 부서장으로 → 대화상자(재직 확인) → 부서장이 결재 → 요청자 알림.
let reassignId = null;
const reassigned = await step("결재자 변경: admin 이 팀장 단계를 부서장으로 바꾸고, 새 결재자가 결재하며 요청자에게 알림이 간다", async () => {
  const created = await api("staff", "POST", "/api/purchase-requests", { vendorId: (await api("staff", "GET", "/api/vendors"))[0].id, title: "재지정 시험", reason: "스모크",
    lines: [{ itemId: (await api("staff", "GET", "/api/items?limit=5"))[0].id, quantity: 1, unitPrice: 300000 }] });
  reassignId = String(created.id);
  await api("staff", "POST", `/api/purchase-requests/${reassignId}/submit`);
  const admin = await loginDirect("admin");
  await admin.goto(`${BASE}/purchase-requests/${reassignId}`);
  await admin.getByRole("button", { name: "결재자 변경" }).click();
  const dialog = admin.locator('[role="dialog"], [role="alertdialog"]');
  await dialog.getByText(/현재 결재자 구매팀장님은 재직 중이며 결재할 수 있습니다/).waitFor();
  const run = dialog.getByRole("button", { name: "결재자 변경" });
  const headOption = dialog.getByLabel("새 결재자", { exact: true }).locator("option").filter({ hasText: "구매부서장 · 부서장" });
  await headOption.first().waitFor({ state: "attached" });
  await dialog.getByLabel("새 결재자", { exact: true }).selectOption(await headOption.first().getAttribute("value"));
  await dialog.getByLabel("구분").selectOption({ label: "장기 부재" });
  await dialog.getByLabel("사유").fill("스모크: 장기 부재");
  assert(await run.isDisabled(), "재직 확인 전에는 실행 버튼이 꺼져 있어야 함");
  await dialog.getByLabel("그래도 바꿉니다").check();
  await shot(admin, "42-reassign-dialog");
  await run.click();
  await admin.getByRole("status").filter({ hasText: "결재자를 구매팀장님에서 구매부서장님으로 바꿨습니다" }).waitFor();
  await admin.getByText(/결재자 변경: 구매팀장 → 구매부서장/).first().waitFor();
  await admin.getByRole("complementary", { name: "결재 정보" }).getByRole("listitem").filter({ hasText: /결재자 변경/ }).first().waitFor(); // 우측 패널 이력의 동작
  await shot(admin, "43-reassigned");
  const head = await loginDirect("head");
  await head.goto(`${BASE}/approvals`);
  await head.getByRole("link", { name: "재지정 시험" }).waitFor();
  const staff = await loginDirect("staff");
  await staff.goto(`${BASE}/`);
  await staff.locator('[data-home-card="notifications"]').getByText(/안 읽은 알림 \d+건/).waitFor();
  await staff.goto(`${BASE}/notifications`);
  await staff.getByText(/결재자가 구매팀장님에서 구매부서장님으로 바뀌었습니다/).waitFor();
  await shot(staff, "44-notifications");
  await staff.getByRole("button", { name: "읽음" }).first().click();
  // 다른 흐름의 알림(결재 완료 등)이 함께 쌓여 있을 수 있어 개수가 0 이 될 때까지 기다린다.
  for (let i = 0; i < 50 && (await staff.getByText("안 읽음", { exact: true }).count()) > 0; i++) await staff.waitForTimeout(100);
  assert((await staff.getByText("안 읽음", { exact: true }).count()) === 0, "모두 읽음 처리 뒤에도 안 읽음이 남음");
}, { needs: lifted });

// 결재 권한을 잃은 조직장(퇴사와 같은 "결재할 수 없음")의 건: 요청자에게 결재자 없음이 보이고, admin 이 재직 확인 없이 결재자를 바꾼다(AC-149).
let stalledId = null;
const stalledReassigned = await step("결재 권한을 잃은 팀장의 건: admin 이 결재자를 바꾼다(재직 확인 없음, 상태 배지 결재할 수 없음)", async () => {
  await loginDirect("lead");
  await loginDirect("staff");
  await loginDirect("admin");
  const leadId = (await api("lead", "GET", "/api/me")).user.id;
  const created = await api("staff", "POST", "/api/purchase-requests", { vendorId: (await api("staff", "GET", "/api/vendors"))[0].id, title: "권한 상실 시험", reason: "스모크",
    lines: [{ itemId: (await api("staff", "GET", "/api/items?limit=5"))[0].id, quantity: 1, unitPrice: 200000 }] });
  stalledId = String(created.id);
  await api("staff", "POST", `/api/purchase-requests/${stalledId}/submit`);
  await api("admin", "PATCH", `/api/admin/users/${leadId}`, { role: "staff" });
  try {
    const staffPage = await loginDirect("staff");
    await staffPage.goto(`${BASE}/purchase-requests/${stalledId}`);
    await staffPage.getByText(/결재자가 퇴사했거나 결재 권한을 잃었을 수 있습니다/).waitFor();
    const admin = await loginDirect("admin");
    await admin.goto(`${BASE}/purchase-requests/${stalledId}`);
    await admin.getByRole("button", { name: "결재자 변경" }).click();
    const dialog = admin.locator('[role="dialog"], [role="alertdialog"]');
    await dialog.getByText("결재할 수 없음", { exact: true }).waitFor();
    assert((await dialog.getByText(/재직 중이며 결재할 수 있습니다/).count()) === 0, "결재할 수 없는 결재자에게 재직 확인 경고가 보임");
    const select = dialog.getByLabel("새 결재자", { exact: true });
    const first = select.locator("option:not([value=''])").first();
    await first.waitFor({ state: "attached" });
    await select.selectOption(await first.getAttribute("value"));
    await dialog.getByLabel("구분").selectOption({ label: "퇴사" });
    await dialog.getByLabel("사유").fill("스모크: 결재 권한 상실");
    await shot(admin, "45-reassign-stalled");
    await dialog.getByRole("button", { name: "결재자 변경" }).click();
    await admin.getByRole("status").filter({ hasText: /결재자를 .*님에서 .*님으로 바꿨습니다/ }).waitFor();
  } finally {
    await api("admin", "PATCH", `/api/admin/users/${leadId}`, { role: "manager" });
  }
}, { needs: reassigned });

// 부서장은 하위 팀(구매1팀) 직원의 건을 목록에서 보고 소속 칸에 상신 당시 조직이 나온다(AC-110).
await step("부서장이 하위 팀 직원의 건을 목록에서 본다(소속 열에 구매1팀)", async () => {
  const page = await loginDirect("head");
  await page.goto(`${BASE}/purchase-requests`);
  // 소속 열은 표에서 빠져 우측 패널로 갔다: 행 라디오로 고르면 "선택한 요청" 패널 요약에 소속이 나온다.
  assert((await page.getByRole("columnheader", { name: "소속" }).count()) === 0, "목록에 소속 열이 남아 있음");
  await page.getByRole("row", { name: /재지정 시험/ }).getByRole("radio").check();
  const sel = page.getByRole("complementary", { name: "선택한 요청" });
  await sel.getByRole("term").filter({ hasText: "소속" }).waitFor();
  await sel.getByText("구매1팀").waitFor();
  await shot(page, "46-head-list-subunit");
}, { needs: reassigned });

console.log("\n--- 결재 양식 문서(B23): 기안 → 결재선 편집(합의) → 상신 → 결재·합의 → 완료 ---");
let docId = null;
const drafted = await step("일반 기안서: 작성 화면에서 합의자를 더해 상신하면 번호가 규칙대로 붙는다(staff)", async () => {
  const page = await loginDirect("staff");
  await page.goto(`${BASE}/docs/new`);
  await page.getByRole("link", { name: "일반 기안서" }).click();
  await page.getByRole("heading", { name: "일반 기안서 작성" }).waitFor();
  await page.getByLabel("제목 *").fill("기안 시험");
  await page.getByLabel("본문 *").fill("협조 부탁드립니다");
  const line = page.getByRole("complementary", { name: "결재선" });
  await line.getByRole("row").nth(1).waitFor();
  await line.getByLabel("사람", { exact: true }).fill("재무");
  await line.getByRole("button", { name: "찾기" }).click();
  await line.getByRole("button", { name: "재무 담당", exact: true }).click();
  await line.getByRole("button", { name: "결재선에 더하기" }).click();
  await line.getByRole("row", { name: /합의 .*재무 담당/ }).waitFor();
  await shot(page, "51-doc-form-line-edit");
  await page.getByRole("button", { name: "저장 후 상신" }).click();
  await page.waitForURL(/\/docs\/\d+$/);
  docId = page.url().split("/").at(-1);
  await page.getByText(/구매팀-\d{4}-\d{4}/).first().waitFor();
  await page.getByRole("cell", { name: "합의", exact: true }).first().waitFor();
  await shot(page, "52-doc-view-submitted");
}, { needs: reassigned });
await step("일반 기안서: 팀장·부서장이 승인하고 합의자가 동의하면 결재 완료가 되고 이력에 남는다", async () => {
  for (const who of ["lead", "head"]) {
    const p = await loginDirect(who);
    await p.goto(`${BASE}/docs/${docId}`);
    await p.getByLabel(/의견/).fill("확인했습니다");
    await p.getByRole("button", { name: "승인", exact: true }).click();
    await p.getByRole("status").filter({ hasText: "처리했습니다" }).waitFor();
  }
  const fin = await loginDirect("finance");
  await fin.goto(`${BASE}/docs/${docId}`);
  await fin.getByRole("button", { name: "동의", exact: true }).click();
  await fin.getByRole("status").filter({ hasText: "현재 상태는 결재 완료입니다" }).waitFor();
  await fin.getByRole("cell", { name: "합의 동의" }).first().waitFor();
  await shot(fin, "53-doc-view-done");
}, { needs: drafted });
await step("양식 관리(admin): 필드를 폼으로 편집하는 화면이 열리고 JSON 입력 칸이 없다", async () => {
  const page = await loginDirect("admin");
  await page.goto(`${BASE}/forms`);
  await page.getByRole("heading", { name: "결재 양식 관리" }).waitFor();
  await page.getByRole("button", { name: "일반 기안서 새 버전 만들기" }).click();
  await page.getByRole("heading", { name: /GEN 2판/ }).waitFor();
  await page.getByLabel("본문 서식 미리보기").waitFor();
  assert((await page.getByLabel(/JSON/i).count()) === 0, "JSON 입력 칸이 있음");
  await shot(page, "54-form-editor");
});

console.log("\n--- 전자결재 문서함·일괄 승인·상신 취소(B23 단계 2) ---");
async function submitDoc(page, title) {
  await page.goto(`${BASE}/docs/new/GEN`);
  await page.getByRole("heading", { name: "일반 기안서 작성" }).waitFor();
  await page.getByLabel("제목 *").fill(title);
  await page.getByLabel("본문 *").fill("문서함 시험 본문");
  await page.getByRole("complementary", { name: "결재선" }).getByRole("row").nth(1).waitFor();
  await page.getByRole("button", { name: "저장 후 상신" }).click();
  await page.waitForURL(/\/docs\/\d+$/);
  return page.url().split("/").at(-1);
}
await step("문서함: 기안한 문서가 결재자의 결재 대기함에 안 읽음으로 보이고 일괄 승인할 수 있다(staff → lead)", async () => {
  const staff = await loginDirect("staff");
  await submitDoc(staff, "문서함 시험");
  const lead = await loginDirect("lead");
  await lead.goto(`${BASE}/docs`);
  await lead.getByRole("heading", { name: "전자결재 문서함" }).waitFor();
  for (const n of ["기안함", "결재 대기함", "결재 진행함", "완료함", "참조함", "수신함", "반려함"]) {
    await lead.getByRole("tab", { name: new RegExp(n) }).waitFor();
  }
  await lead.getByRole("row", { name: /문서함 시험/ }).getByLabel("안 읽음").waitFor();
  await shot(lead, "57-doc-inbox-pending");
  await lead.getByLabel("문서함 시험 선택").check();
  await lead.getByRole("button", { name: "선택한 1건 일괄 승인" }).click();
  await lead.getByRole("status").filter({ hasText: "1건을 승인했습니다" }).waitFor();
  await lead.getByRole("tab", { name: /결재 진행함/ }).click();
  await lead.getByRole("row", { name: /문서함 시험/ }).waitFor();
}, { needs: drafted });
await step("상신 취소: 아무도 결재하지 않은 문서를 기안자가 취소하면 임시저장으로 돌아간다(staff)", async () => {
  const page = await loginDirect("staff");
  await submitDoc(page, "상신 취소 시험");
  await page.getByRole("button", { name: "상신 취소", exact: true }).click();
  await page.getByLabel("취소 사유(필수)").fill("내용을 고치려고");
  await page.getByRole("button", { name: "상신 취소하기" }).click();
  await page.getByRole("status").filter({ hasText: "상신을 취소했습니다" }).waitFor();
  await shot(page, "58-doc-cancel-submit");
}, { needs: drafted });

console.log("\n--- 전결 규정·출력 화면(B23 단계 3) ---");
await step("전결 규정(admin): 시드 규정이 표로 보이고 수정 칸에 값이 채워진다", async () => {
  const page = await loginDirect("admin");
  await page.goto(`${BASE}/authority`);
  await page.getByRole("heading", { name: "전결 규정" }).waitFor();
  const row = page.getByRole("row", { name: /지출 결의서.*부서장/ });
  await row.waitFor();
  await row.getByRole("button", { name: "수정" }).click();
  await page.getByRole("heading", { name: "규정 수정" }).waitFor();
  assert((await page.getByLabel("최소 금액(원, 이상)").inputValue()) === "10000000", "수정 칸에 최소 금액이 채워지지 않음");
  await shot(page, "59-authority-rules");
});
await step("출력 화면: 결재란·본문·전결 근거 줄이 보이고 인쇄하면 감사에 남긴다(finance)", async () => {
  const page = await loginDirect("finance");
  await page.addInitScript(() => {
    window.print = () => {
      window.__printed = true;
    };
  });
  await page.goto(`${BASE}/docs/${docId}/print`);
  await page.getByRole("heading", { name: "일반 기안서", exact: true }).waitFor();
  await page.getByRole("table", { name: "결재란" }).first().waitFor();
  await page.getByText("보존 기한:").waitFor();
  await shot(page, "60-doc-print");
  await page.getByRole("button", { name: "인쇄" }).click();
  await page.waitForFunction(() => window.__printed === true);
}, { needs: drafted });
await step("전결 근거: 지출 결의서 상세에서 근거 조항을 누르면 전결 규정 원문이 열린다(staff, 규정 검색 연결)", async () => {
  const page = await loginDirect("staff");
  const req = page.context().request;
  const me = await (await req.get(`${BASE}/api/me`)).json();
  const hdr = { "X-CSRF-Token": me.csrfToken, Origin: BASE };
  const up = await req.post(`${BASE}/api/approval-docs/attachments`, {
    headers: hdr,
    multipart: { file: { name: "receipt.pdf", mimeType: "application/pdf", buffer: Buffer.from("%PDF-1.4\nxxxxxxxxxxxxxxxxxxxx") } },
  });
  assert(up.status() === 201, `증빙 올리기 ${up.status()}`);
  const ev = (await up.json()).id;
  const created = await req.post(`${BASE}/api/approval-docs`, {
    headers: { ...hdr, "Content-Type": "application/json" },
    data: JSON.stringify({ formCode: "EXP", title: "전결 근거 시험 지출", fields: { spend_date: "2026-11-10", spend_kind: "출장", payee: "김가나", items: [{ name: "출장비", amount: 15000000 }], total: 15000000, evidence: [ev] } }),
  });
  assert(created.status() === 201, `지출 결의서 작성 ${created.status()}`);
  const id = (await created.json()).id;
  const sub = await req.post(`${BASE}/api/approval-docs/${id}/submit`, { headers: hdr });
  assert(sub.status() === 200, `상신 ${sub.status()}`);
  await page.goto(`${BASE}/docs/${id}`);
  await page.getByRole("heading", { name: "전결 근거 시험 지출" }).first().waitFor();
  await page.getByRole("button", { name: "제2조" }).click();
  await page.getByText(/부서장이 최종 결재/).first().waitFor();
  assert((await page.locator("blockquote").first().innerText()).includes("전결 규정 제2조"), "근거 조항 출처가 없음");
  await shot(page, "61-authority-clause");
}, { needs: drafted });

console.log("\n--- 역할별 홈·메뉴(권한 없는 항목은 그려지지 않는다) ---");
// 기대 표는 화면 소스가 아니라 spec 의 역할 권한에서 손으로 옮긴 것이다. 권한이 바뀌면 이 표도 함께 바꾼다.
const ROLE_VIEWS = {
  staff: {
    menu: ["홈", "구매요청", "전자결재", "결재함", "재고", "문서 검증", "조직도"],
    noMenu: ["결재 양식", "전결 규정", "입고", "AI 사용량", "AI 연결 사용 현황", "평가 이력", "감사 로그", "관리자 설정", "조직 관리", "인사 발령", "조직 일괄 반영"],
    cards: ["알림", "내 요청 진행", "재고 경보", "오늘 AI 사용량"],
    noCards: ["결재 대기", "판정 확정 대기"],
  },
  lead: {
    menu: ["홈", "구매요청", "전자결재", "결재함", "재고", "문서 검증", "조직도"],
    noMenu: ["결재 양식", "전결 규정", "입고", "AI 사용량", "AI 연결 사용 현황", "평가 이력", "감사 로그", "관리자 설정", "조직 관리", "인사 발령", "조직 일괄 반영"],
    cards: ["알림", "결재 대기", "내 요청 진행", "재고 경보", "오늘 AI 사용량"],
    noCards: ["판정 확정 대기"],
  },
  finance: {
    menu: ["홈", "구매요청", "전자결재", "결재함", "재고", "입고", "문서 검증", "조직도", "AI 사용량"],
    noMenu: ["결재 양식", "전결 규정", "AI 연결 사용 현황", "평가 이력", "감사 로그", "관리자 설정", "조직 관리", "인사 발령"],
    cards: ["알림", "결재 대기", "내 요청 진행", "재고 경보", "판정 확정 대기", "오늘 AI 사용량"],
    noCards: [],
  },
  auditor: {
    menu: ["홈", "구매요청", "전자결재", "결재함", "재고", "문서 검증", "조직도", "AI 사용량", "AI 연결 사용 현황", "평가 이력", "감사 로그"],
    noMenu: ["결재 양식", "전결 규정", "입고", "관리자 설정", "조직 관리", "인사 발령"],
    cards: ["알림", "재고 경보", "오늘 AI 사용량"],
    noCards: ["결재 대기", "내 요청 진행", "판정 확정 대기"],
  },
  admin: {
    menu: ["홈", "구매요청", "전자결재", "결재함", "재고", "입고", "문서 검증", "조직도", "AI 사용량", "AI 연결 사용 현황", "평가 이력", "감사 로그", "조직 관리", "인사 발령", "조직 일괄 반영", "관리자 설정", "결재 양식", "전결 규정"],
    noMenu: [],
    cards: ["알림", "재고 경보", "오늘 AI 사용량"],
    noCards: ["결재 대기", "내 요청 진행", "판정 확정 대기"],
  },
};
// B22: 결재·합의자 소멸 홈 카드는 관리자에게만 보인다.
for (const [who, v] of Object.entries(ROLE_VIEWS)) (who === "admin" ? v.cards : v.noCards).push("결재·합의자 소멸");
// B29: 보존·폐기 메뉴와 "보존 기한이 지난 문서" 홈 카드는 관리자·감사에게만 보인다(폐기는 관리자만 한다).
for (const [who, v] of Object.entries(ROLE_VIEWS)) {
  const sees = who === "admin" || who === "auditor";
  (sees ? v.menu : v.noMenu).push("보존·폐기");
  (sees ? v.cards : v.noCards).push("보존 기한이 지난 문서");
}
let n = 14;
for (const [who, v] of Object.entries(ROLE_VIEWS)) {
  await step(`역할별 홈·메뉴: ${who}`, async () => {
    const page = await loginDirect(who);
    await page.getByRole("heading", { name: "홈" }).waitFor();
    await settle(page);
    const menu = await menuLabels(page);
    const cards = await cardTitles(page);
    for (const m of v.menu) assert(menu.includes(m), `메뉴에 '${m}' 이 없음(실제: ${menu.join(", ")})`);
    for (const m of v.noMenu) assert(!menu.includes(m), `권한 없는 메뉴 '${m}' 이 그려짐`);
    // 알림은 GNB 종 아이콘, 내 계정·개인 토큰·로그아웃은 GNB 사용자 메뉴로 간다(SNB 에는 없다).
    await page.locator('.gnb a[href="/notifications"]').waitFor();
    await page.locator(".gnb-user").click();
    for (const name of ["내 계정", "개인 토큰", "로그아웃"]) await page.getByRole("menuitem", { name }).waitFor();
    await page.keyboard.press("Escape");
    for (const c of v.cards) assert(cards.includes(c), `카드 '${c}' 가 없음(실제: ${cards.join(", ")})`);
    for (const c of v.noCards) assert(!cards.includes(c), `권한 없는 카드 '${c}' 가 그려짐`);
    await shot(page, `${n}-home-${who}`);
  });
  n += 1;
}

await step("권한 없는 화면 직접 접근: staff 는 감사 로그·관리자 설정을 볼 수 없다", async () => {
  const page = await loginDirect("staff");
  for (const path of ["/audit-logs", "/admin/settings", "/receipts", "/mcp-usage", "/org/manage", "/org/appointments"]) {
    await page.goto(`${BASE}${path}`);
    await page.getByRole("alert").filter({ hasText: "이 화면을 볼 권한이 없습니다" }).waitFor();
    assert((await page.getByRole("heading", { name: /감사 로그|관리자 설정/ }).count()) === 0, `${path} 에서 화면 본문이 그려짐`);
  }
  await shot(page, "19-staff-forbidden-screen");
});

console.log("\n--- 감사 로그 화면 ---");
await step("감사 로그: 한국어 라벨로 기록이 보이고 변조 검증이 돈다(auditor)", async () => {
  const page = await loginDirect("auditor");
  await page.goto(`${BASE}/audit-logs`);
  await page.getByRole("heading", { name: "감사 로그" }).waitFor();
  const rows = page.locator("tbody tr");
  await rows.first().waitFor();
  assert((await rows.count()) >= 10, `기록이 10건 미만(${await rows.count()})`);
  const text = await page.locator("tbody").innerText();
  assert(/구매요청/.test(text) && /상신|결재|승인/.test(text), "구매요청·결재 기록이 한국어로 보이지 않음");
  assert(!/purchase_request\.|session\.login|approval\.decide/.test(text), "내부 동작 코드가 그대로 노출됨");
  // 내부 코드·해시가 값으로 새지 않는다: 역할·직책 코드, 규정 검색 대상 related, 16진 해시, 번호만 있는 사용자 칸.
  for (const leak of [/team_lead/, /dept_head/, /finance/, /\brelated\b/, /[0-9a-f]{12,}/i]) {
    assert(!leak.test(text), `감사 로그에 내부 값이 그대로 보임: ${leak}`);
  }
  assert(!/\d{7,}/.test(text), "금액이 천 단위 구분 없이 보임");
  for (const li of await page.locator("tbody tr td ul li").allInnerTexts()) {
    const m = /^[^:]+: (.+) → (.+)$/.exec(li.trim());
    assert(!m || m[1] !== m[2], `값이 바뀌지 않은 항목이 화살표로 보임: ${li}`);
  }
  const userCells = await page.locator("tbody tr td:nth-child(3)").allInnerTexts();
  assert(userCells.every((c) => !/^#\d+$/.test(c.trim())), "사용자 칸에 번호만 보이는 행이 있음");
  assert(userCells.some((c) => /구매직원 \(직원\)/.test(c)), "사용자 칸에 이름과 역할이 보이지 않음");
  await shot(page, "20-audit-log");
  await page.getByRole("button", { name: "변조 여부 검증" }).click();
  const status = page.getByRole("status").filter({ hasText: /변조/ });
  await status.waitFor();
  const msg = await status.innerText();
  assert(/변조가 없습니다|변조는 없습니다/.test(msg) && !/감지되었습니다/.test(msg), `검증 결과가 정상이 아님: ${msg}`);
  await page.getByTestId("verify-level").waitFor();
  await shot(page, "21-audit-verify");
});
await step("AI 연결 사용 현황 화면이 열린다(auditor): 성공·권한 거부 호출을 만든 뒤 표가 채워진다", async () => {
  // 개인 토큰을 발급해 /mcp 로 도구를 몇 번 호출한다: 성공(품목 검색·재고 조회)과 권한 거부(직원의 결재 시도).
  const issued = await api("staff", "POST", "/api/tokens", { name: "스모크 AI 연결", ttlDays: 1 });
  const { context: staffCtx } = await session("staff");
  const callTool = async (name, args) => {
    const res = await staffCtx.request.post(`${BASE}/mcp`, {
      headers: { Authorization: `Bearer ${issued.token}`, "Content-Type": "application/json", Accept: "application/json, text/event-stream" },
      data: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/call", params: { name, arguments: args } }),
    });
    assert(res.ok(), `/mcp ${name} → HTTP ${res.status()}`);
    return res.text();
  };
  for (let i = 0; i < 3; i++) assert(!/"isError":\s*true/.test(await callTool("search_items", { query: "볼트" })), "품목 검색 호출이 실패함");
  assert(!/"isError":\s*true/.test(await callTool("get_stock", { item_id: 1 })), "재고 조회 호출이 실패함");
  assert(/"isError":\s*true/.test(await callTool("approve_or_reject", { pr_id: 1, approve: true })), "직원의 결재 호출이 거부되지 않음");
  const page = await loginDirect("auditor");
  await page.goto(`${BASE}/mcp-usage`);
  await page.getByRole("heading", { name: "AI 연결 사용 현황" }).waitFor();
  await settle(page);
  assert((await page.getByRole("alert").count()) === 0, "사용 현황 화면에 오류 알림이 있음");
  await page.locator('table[aria-label="도구별 사용량"] tbody tr').first().waitFor();
  const body = await page.locator("main").innerText();
  assert(/품목 검색/.test(body), "사용 현황 표에 호출한 도구(품목 검색)가 없음");
  await shot(page, "22-mcp-usage");
});
await step("감사 로그 화면 권한: finance 에게는 메뉴가 없고 직접 접근도 막힌다", async () => {
  const page = await loginDirect("finance");
  await page.goto(`${BASE}/audit-logs`);
  await page.getByRole("alert").filter({ hasText: "이 화면을 볼 권한이 없습니다" }).waitFor();
});

console.log("\n--- 조직도(조직 신설 → 예약 전보 → 기준일 조회) ---");
await step("조직도: 관리자가 팀을 신설하고 직원을 내일자로 전보하면 오늘 기준과 내일 기준 조직도가 다르게 보인다(admin)", async () => {
  const page = await loginDirect("admin");
  const tomorrow = new Date(Date.now() + 24 * 60 * 60 * 1000).toLocaleDateString("sv-SE", { timeZone: "Asia/Seoul" });
  // 트리에서 조직 이름 버튼(펼침·접기 버튼이 아니라)을 누른다.
  const select = async (name) => {
    await page.locator(".org-select", { hasText: new RegExp(`^${name}( |$)`) }).first().click();
  };
  // 1) 조직 관리: 경영지원팀 아래에 팀 신설(오늘)
  await page.goto(`${BASE}/org/manage`);
  await page.getByRole("heading", { name: "조직 관리" }).waitFor();
  await select("경영지원팀");
  await page.getByRole("button", { name: "하위 조직 신설" }).click();
  const form = page.getByRole("form", { name: "조직 신설" });
  await form.getByLabel("조직 코드").fill("SMOKE-T1");
  await form.getByLabel("조직 이름").fill("스모크팀");
  assert((await form.getByRole("button", { name: "등록" }).isDisabled()) === true, "미리보기 전에 등록 버튼이 켜져 있음");
  await form.getByRole("button", { name: "미리보기" }).click();
  await form.getByText(/새로 만들어집니다/).waitFor();
  await form.getByRole("button", { name: "등록" }).click();
  await page.getByRole("status").filter({ hasText: /발령을 등록했습니다/ }).waitFor();
  await shot(page, "40-org-manage-created");
  // 2) 인사 발령: 구매직원을 스모크팀으로 내일자 전보(예약)
  await page.goto(`${BASE}/org/appointments`);
  await page.getByRole("heading", { name: "인사 발령" }).waitFor();
  await page.getByLabel("종류").first().selectOption({ label: "전보" });
  const tf = page.getByRole("form", { name: "전보" });
  await tf.getByPlaceholder("이름·조직·직책으로 찾기").fill("구매직원");
  await tf.getByRole("button", { name: "찾기" }).click();
  await tf.getByRole("button", { name: "구매직원" }).first().click();
  const opt = await tf.locator("select#cf-to option", { hasText: "스모크팀" }).first().getAttribute("value");
  await tf.locator("select#cf-to").selectOption(opt);
  await tf.getByLabel("효력일").fill(tomorrow);
  await tf.getByRole("button", { name: "미리보기" }).click();
  await tf.getByText(/주소속이/).waitFor();
  await tf.getByRole("button", { name: "등록" }).click();
  await page.getByRole("status").filter({ hasText: "예약 발령으로 등록했습니다." }).waitFor();
  await page.getByText("스모크팀(으)로 전보").first().waitFor();
  await shot(page, "41-org-appointment-scheduled");
  // 3) 조직도: 오늘 기준은 구매팀 소속, 내일 기준은 스모크팀 소속
  await page.goto(`${BASE}/org`);
  await page.getByRole("heading", { name: "조직도" }).waitFor();
  await select("구매팀");
  await page.getByRole("button", { name: "구매직원" }).first().waitFor();
  await shot(page, "42-org-chart-today");
  // 폭 1280 에서 구성원 표가 가로로 밀리지 않는다(휴대전화·내선 열까지 보임).
  const wrapOverflow = await page.locator(".org-members").first().evaluate((t) => t.parentElement.scrollWidth - t.parentElement.clientWidth);
  assert(wrapOverflow <= 1, `구성원 표가 가로 스크롤됨(${wrapOverflow}px)`);
  // 선택해 둔 조직이 없던 기준일(조직이 아예 없는 날짜)로 가면 안내 문구 하나만 나온다.
  await page.getByLabel("기준일").fill("2015-12-31");
  await page.getByText("이 날짜에는 없던 조직입니다. 조직을 다시 선택하세요.").waitFor();
  const mainText = await page.locator("main").innerText();
  assert(!/조직도를 불러오지 못했습니다|이 조직에는 구성원이 없습니다/.test(mainText), "없던 조직 안내와 함께 오류·빈 목록 문구가 나옴");
  await shot(page, "42b-org-chart-no-unit-on-date");
  await page.getByRole("button", { name: "오늘로 돌아가기" }).click();
  await page.getByText("이 날짜에는 없던 조직입니다. 조직을 다시 선택하세요.").waitFor({ state: "detached" });
  await select("구매팀");
  await page.getByLabel("기준일").fill(tomorrow);
  await page.getByText(/기준 조직도입니다\(예약된 발령 반영\)/).waitFor();
  await page.getByRole("button", { name: "경영지원팀 펼치기" }).click();
  await select("스모크팀");
  await page.getByRole("button", { name: "구매직원" }).first().waitFor();
  await shot(page, "43-org-chart-tomorrow");
  await select("구매팀");
  // 목록이 구매팀 구성원으로 바뀔 때까지 기다린다(내일 기준이면 구매직원은 더 이상 없다. 남아 있으면 시간 초과로 실패).
  await page.getByRole("heading", { name: "구매팀 구성원" }).waitFor();
  await page.getByRole("button", { name: "구매직원" }).first().waitFor({ state: "detached" });
  await page.getByRole("button", { name: "오늘로 돌아가기" }).click();
  await page.getByText(/기준 조직도입니다/).waitFor({ state: "detached" });
});
await step("인사 발령: 휴직 입력은 민감 정보 안내와 구분·예정 복직일을 받고 미리보기는 결재 영향을 보인다. 등록하지 않는다(admin)", async () => {
  const page = await loginDirect("admin");
  await page.goto(`${BASE}/org/appointments`);
  await page.getByRole("heading", { name: "인사 발령" }).waitFor();
  await page.getByLabel("종류").first().selectOption({ label: "휴직" });
  const af = page.getByRole("form", { name: "휴직" });
  await af.getByPlaceholder("이름·조직·직책으로 찾기").fill("구매직원");
  await af.getByRole("button", { name: "찾기" }).click();
  await af.getByRole("button", { name: "구매직원" }).first().click();
  await af.getByText(/병명·진단 내용은 적지 마세요/).waitFor();
  await af.getByLabel("휴직 구분").selectOption({ label: "육아" });
  await af.getByLabel(/휴직 사유/).fill("시험 사유");
  await af.getByRole("button", { name: "미리보기" }).click();
  await af.getByText(/휴직 상태가 됩니다/).waitFor();
  assert((await af.getByRole("button", { name: "등록" }).isDisabled()) === false, "미리보기 뒤에 등록 버튼이 켜지지 않음");
  assert(!(await page.locator("main").innerText()).includes("시험 사유"), "미리보기에 휴직 사유 원문이 나옴");
  await shot(page, "41b-org-absence-preview");
});
await step("조직 일괄 반영: 파일을 올려 미리보기를 보고 반영하면 조직도에 나타난다(admin)", async () => {
  const page = await loginDirect("admin");
  await page.goto(`${BASE}/org/import`);
  await page.getByRole("heading", { name: "조직 일괄 반영" }).waitFor();
  const today = new Date().toLocaleDateString("sv-SE", { timeZone: "Asia/Seoul" });
  const units = `조직코드,조직명,종류,상위조직코드,조직장사번\nSMOKE-IMP,일괄시험팀,팀,DEP-PU,E990\n`;
  const people = `사번,이름,이메일,직위,주소속조직코드,권한역할,입사일\nE990,이일괄,imp990@pl.test,과장,SMOKE-IMP,결재자,${today}\n`;
  // 오류 미리보기: 없는 상위 조직 코드
  await page.getByLabel("조직 파일").setInputFiles({ name: "units.csv", mimeType: "text/csv", buffer: Buffer.from(units.replace("DEP-PU", "NOPE-9")) });
  await page.getByRole("button", { name: "미리보기" }).click();
  await page.getByText(/오류 \d+개가 있어 반영할 수 없습니다/).waitFor();
  await page.getByText(/조직 2행 상위조직코드: 없는 조직 코드입니다/).waitFor();
  assert(await page.getByRole("button", { name: "이 내용으로 반영" }).isDisabled(), "오류가 있는데 반영 버튼이 켜져 있음");
  await shot(page, "46-org-import-errors");
  await page.getByRole("button", { name: "다시 선택" }).click();
  // 정상 미리보기
  await page.getByLabel("조직 파일").setInputFiles({ name: "units.csv", mimeType: "text/csv", buffer: Buffer.from(units) });
  await page.getByLabel("사람 파일").setInputFiles({ name: "people.csv", mimeType: "text/csv", buffer: Buffer.from(people) });
  await page.getByRole("button", { name: "미리보기" }).click();
  await page.getByText(/아직 아무것도 반영되지 않았습니다/).waitFor();
  await page.getByRole("list", { name: "건수" }).waitFor();
  await page.getByRole("cell", { name: "이일괄(E990)" }).first().waitFor();
  await shot(page, "47-org-import-preview");
  await page.getByRole("button", { name: "이 내용으로 반영" }).click();
  await page.getByText(/반영했습니다. 변경 \d+건이 반영되었습니다/).waitFor();
  await shot(page, "48-org-import-result");
  await page.getByRole("link", { name: "조직도에서 보기" }).click();
  await page.getByRole("heading", { name: "조직도" }).waitFor();
  await page.getByPlaceholder("이름·조직·직책").fill("이일괄");
  await page.getByRole("button", { name: "찾기" }).first().click();
  await page.getByRole("button", { name: "이일괄" }).first().waitFor();
});
await step("조직 관리 점검 탭: 결재·합의자가 사라진 진행 중 건 목록이 맨 위에 보인다(admin)", async () => {
  const page = await loginDirect("admin");
  await page.goto(`${BASE}/org/manage?tab=check`);
  await page.getByRole("heading", { name: /결재·합의자가 사라진 진행 중 건/ }).waitFor();
  await page.getByRole("heading", { name: "조직 정합성" }).waitFor();
  const list = page.getByRole("region", { name: "결재·합의자 소멸" });
  // 목록 데이터가 늦게 올 수 있으므로 건수 또는 빈 상태 문구 둘 중 하나가 나타날 때까지 기다린다(세지 않는다).
  await list
    .getByText(/^\d+건$/)
    .or(list.getByText("결재·합의자가 사라진 진행 중 건이 없습니다."))
    .first()
    .waitFor()
    .catch(() => {
      throw new Error("목록도 빈 상태도 보이지 않음");
    });
  await shot(page, "62-org-orphans");
});
await step("내보내기: 감사 로그 CSV 와 조직 구성원 XLSX 가 내려받아지고 연락처는 기본으로 가려진다(auditor·admin)", async () => {
  const aud = await loginDirect("auditor");
  await aud.goto(`${BASE}/audit-logs`);
  await aud.getByRole("heading", { name: "감사 로그" }).waitFor();
  await aud.locator("tbody tr").first().waitFor();
  const [dl] = await Promise.all([aud.waitForEvent("download"), aud.getByRole("button", { name: "CSV" }).click()]);
  assert(/^감사 로그_\d{8}_\d{4}\.csv$/.test(dl.suggestedFilename()), `감사 로그 파일 이름이 이상함: ${dl.suggestedFilename()}`);
  const csv = readFileSync(await dl.path(), "utf8");
  assert(csv.startsWith("\uFEFF번호,일시,사용자,경로,동작,대상,결과,변경 내용"), `CSV 머리글(BOM 포함)이 이상함: ${csv.slice(0, 40)}`);
  assert(!/session\.login|purchase_request\./.test(csv), "감사 로그 파일에 내부 동작 코드가 그대로 있음");
  assert(csv.split("\r\n").length > 3, "감사 로그 파일에 행이 없음");

  const adm = await loginDirect("admin");
  await adm.goto(`${BASE}/org`);
  await adm.getByRole("heading", { name: "조직도" }).waitFor();
  await adm.locator(".org-tree-box .org-select").first().click();
  await adm.locator("table.org-members tbody tr").first().waitFor();
  assert((await adm.getByLabel("원문 포함(민감 정보)").count()) === 1, "관리자에게 원문 포함 선택이 보이지 않음");
  const [xl] = await Promise.all([adm.waitForEvent("download"), adm.getByRole("button", { name: "엑셀(XLSX)" }).click()]);
  assert(/^조직 구성원_\d{8}_\d{4}\.xlsx$/.test(xl.suggestedFilename()), `조직 구성원 파일 이름이 이상함: ${xl.suggestedFilename()}`);
  const bytes = readFileSync(await xl.path());
  assert(bytes.length > 500 && bytes.subarray(0, 2).toString() === "PK", "XLSX 파일이 zip 형식이 아님");
  await shot(adm, "74-export-org-people");
  // 직원에게는 원문 포함 선택이 없다
  const stf = await loginDirect("staff");
  await stf.goto(`${BASE}/org`);
  await stf.getByRole("heading", { name: "조직도" }).waitFor();
  await stf.locator(".org-tree-box .org-select").first().click();
  await stf.locator("table.org-members tbody tr").first().waitFor();
  assert((await stf.getByLabel("원문 포함(민감 정보)").count()) === 0, "직원에게 원문 포함 선택이 보임");
});
await step("보존·폐기: 관리자는 선택·폐기 영역이 있는 화면을, 감사는 목록만 본다(admin·auditor)", async () => {
  const adm = await loginDirect("admin");
  await adm.goto(`${BASE}/admin/retention`);
  await adm.getByRole("heading", { name: "보존·폐기" }).waitFor();
  await adm.getByText("폐기할 문서가 없습니다.").waitFor(); // 개발 데이터에는 보존 기한이 지난 문서가 없다(자동 폐기도 없다)
  assert((await adm.getByRole("tab", { name: "폐기 보류" }).count()) === 1, "폐기 보류 탭이 없음");
  await adm.getByRole("tab", { name: "폐기 보류" }).click();
  await adm.getByText("폐기 보류 중인 문서가 없습니다.").waitFor();
  await shot(adm, "75-retention");
  const aud = await loginDirect("auditor");
  await aud.goto(`${BASE}/admin/retention`);
  await aud.getByRole("heading", { name: "보존·폐기" }).waitFor();
  assert((await aud.getByRole("button", { name: /폐기 승인/ }).count()) === 0, "감사에게 폐기 승인 단추가 보임");
  const stf = await loginDirect("staff");
  await stf.goto(`${BASE}/admin/retention`);
  assert((await stf.getByRole("heading", { name: "보존·폐기" }).count()) === 0, "직원에게 보존·폐기 화면이 그려짐");
});
await step("조직도: 직원은 연락처가 가려져 보이고 이력 표는 없다(staff)", async () => {
  const page = await loginDirect("staff");
  await page.goto(`${BASE}/org`);
  await page.getByRole("heading", { name: "조직도" }).waitFor();
  await page.getByPlaceholder("이름·조직·직책").fill("구매팀장");
  await page.getByRole("button", { name: "찾기" }).first().click();
  const lead = page.getByRole("button", { name: "구매팀장" }).first();
  await lead.waitFor();
  const rowText = await page.locator("tbody tr").first().innerText();
  assert(!/lead@pl\.test/.test(rowText), "직원에게 이메일 원문이 보임");
  await lead.click();
  await page.getByLabel("사람 카드").waitFor();
  assert((await page.getByText("발령 이력").count()) === 0, "이력 권한이 없는데 발령 이력이 보임");
  await shot(page, "44-org-person-card-staff");
});

console.log("\n--- 좁은 화면(폭 390) ---");
/** 폭 390 으로 줄여 페이지 가로 넘침이 없는지 보고 스크린샷을 남긴 뒤 원래 크기로 돌린다. */
async function narrowCheck(page, name, ready) {
  await page.setViewportSize({ width: 390, height: 844 });
  try {
    await ready();
    await settle(page);
    const { sw, cw } = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, cw: document.documentElement.clientWidth }));
    await shot(page, name);
    assert(sw <= cw, `페이지가 가로로 넘침(scrollWidth ${sw} > clientWidth ${cw})`);
  } finally {
    await page.setViewportSize({ width: 1280, height: 900 });
  }
}
console.log("\n--- 로그인 설정(B30) ---");
await step("로그인 설정(admin): 방식 카드 3장·정책이 보이고 비밀 입력칸이 없으며, 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("admin");
  await page.goto(`${BASE}/admin/auth`);
  await page.getByRole("heading", { name: "로그인 설정" }).waitFor();
  for (const t of ["구글 워크스페이스", "마이크로소프트 Entra ID", "SAML 2.0"]) await page.getByRole("heading", { name: t }).waitFor();
  await page.getByRole("heading", { name: "이메일 로그인과 정책" }).waitFor();
  assert((await page.locator('input[type="password"]').count()) === 0, "설정 화면에 비밀번호·비밀값 입력칸이 있음");
  await shot(page, "29-admin-auth");
  await narrowCheck(page, "29b-admin-auth-narrow", async () => {
    await page.getByRole("heading", { name: "로그인 설정" }).waitFor();
  });
});
await step("로그인 설정: 직원에게는 메뉴가 없고 주소로 열면 권한 없음 안내다(staff)", async () => {
  const page = await loginDirect("staff");
  assert(!(await menuLabels(page)).includes("로그인 설정"), "직원에게 로그인 설정 메뉴가 보임");
  await page.goto(`${BASE}/admin/auth`);
  await page.getByRole("alert").waitFor();
});

console.log("\n--- 역할 관리(B33) ---");
await step("역할 관리(admin): 기본 템플릿 5개와 권한 선택 화면이 보이고 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("admin");
  await page.goto(`${BASE}/admin/roles`);
  await page.getByRole("heading", { name: "역할 관리" }).waitFor();
  for (const n of ["관리자", "재무", "결재자", "직원", "감사"]) await page.getByRole("button", { name: `${n} 복제` }).waitFor();
  assert((await page.getByText("기본 템플릿").count()) >= 5, "기본 템플릿 표시가 5개 미만");
  await page.getByRole("button", { name: "새 역할 만들기" }).click();
  await page.getByLabel("이름").fill("화면 시험 역할");
  await page.getByRole("heading", { name: "새 역할" }).waitFor();
  assert((await page.getByText("위험").count()) > 0, "위험 권한 표시가 없음");
  await shot(page, "31-roles");
  await narrowCheck(page, "31b-roles-narrow", async () => {
    await page.getByRole("heading", { name: "역할 관리" }).waitFor();
  });
});
await step("역할 부여(admin): 역할의 부여 화면과 새로 부여 폼이 보이고 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("admin");
  await page.goto(`${BASE}/admin/roles`);
  await page.getByRole("heading", { name: "역할 관리" }).waitFor();
  await page.getByRole("button", { name: "직원 보기·편집" }).click();
  await page.getByRole("heading", { name: "'직원' 부여" }).waitFor();
  await page.getByRole("heading", { name: "새로 부여" }).waitFor();
  await shot(page, "31c-role-grants");
  await narrowCheck(page, "31d-role-grants-narrow", async () => {
    await page.getByRole("heading", { name: "'직원' 부여" }).waitFor();
  });
});
await step("권한 분리 점검(admin): 충돌 쌍 점검 화면이 열린다", async () => {
  const page = await loginDirect("admin");
  await page.goto(`${BASE}/admin/roles/separation`);
  await page.getByRole("heading", { name: "권한 분리 점검" }).waitFor();
  await shot(page, "32-separation");
  await narrowCheck(page, "32b-separation-narrow", async () => {
    await page.getByRole("heading", { name: "권한 분리 점검" }).waitFor();
  });
});
await step("역할 관리: 직원에게는 메뉴가 없고 주소로 열면 권한 없음 안내다(staff)", async () => {
  const page = await loginDirect("staff");
  assert(!(await menuLabels(page)).includes("역할 관리"), "직원에게 역할 관리 메뉴가 보임");
  await page.goto(`${BASE}/admin/roles`);
  await page.getByRole("alert").waitFor();
});
await step("내 계정: 내 권한과 출처가 보인다(staff)", async () => {
  const page = await loginDirect("staff");
  await page.goto(`${BASE}/account`);
  await page.getByRole("heading", { name: "내 권한" }).waitFor();
  await shot(page, "33-account-access");
  await narrowCheck(page, "33b-account-access-narrow", async () => {
    await page.getByRole("heading", { name: "내 권한" }).waitFor();
  });
  await page.getByText(/역할: 직원/).waitFor();
});

console.log("\n--- 기초 데이터 가져오기(B35) ---");
await step("기초 데이터 가져오기(admin): 메뉴·대상 카드 7개·금액성 대상 비활성·이력 빈 상태, 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("admin");
  const menu = await menuLabels(page);
  assert(menu.includes("기초 데이터 가져오기") && menu.includes("가져오기 이력"), `관리자 메뉴: ${menu.join(",")}`);
  await page.goto(`${BASE}/admin/import`);
  await page.getByRole("heading", { name: "기초 데이터 가져오기" }).waitFor();
  await page.locator("[data-import-target]").first().waitFor();
  assert((await page.locator("[data-import-target]").count()) === 7, "대상 카드가 7개가 아님");
  assert(await page.getByRole("button", { name: "기초 재고 가져오기" }).isDisabled(), "관리자에게 기초 재고 가져오기가 열려 있음");
  assert((await page.getByText("이 대상을 가져올 권한이 없습니다.").count()) >= 3, "금액성 대상의 권한 없음 사유가 없음");
  await shot(page, "102-import-hub");
  await narrowCheck(page, "102b-import-hub-narrow", async () => {
    await page.getByRole("heading", { name: "기초 데이터 가져오기" }).waitFor();
  });
  await page.goto(`${BASE}/admin/import/history`);
  await page.getByRole("heading", { name: "가져오기 이력" }).waitFor();
  await page.getByText("조건에 맞는 가져오기 묶음이 없습니다.").waitFor();
  await shot(page, "103-import-history");
  await narrowCheck(page, "103b-import-history-narrow", async () => {
    await page.getByRole("heading", { name: "가져오기 이력" }).waitFor();
  });
});
await step("기초 데이터 가져오기(finance): 금액성 대상도 카드에 보인다", async () => {
  const page = await loginDirect("finance");
  await page.goto(`${BASE}/admin/import`);
  await page.getByRole("heading", { name: "기초 데이터 가져오기" }).waitFor();
  await page.locator("[data-import-target]").first().waitFor();
  assert((await page.getByText("이 대상을 가져올 권한이 없습니다.").count()) === 0, "재무에게 권한 없음 사유가 보임");
});
await step("기초 데이터 가져오기 메뉴 표: 직원·결재자에게는 없고 감사에게는 이력만 있다", async () => {
  for (const who of ["staff", "lead"]) {
    const page = await loginDirect(who);
    const menu = await menuLabels(page);
    assert(!menu.includes("기초 데이터 가져오기") && !menu.includes("가져오기 이력"), `${who} 메뉴에 가져오기가 보임: ${menu.join(",")}`);
    await page.goto(`${BASE}/admin/import`);
    await page.getByRole("alert").waitFor();
  }
  const aud = await loginDirect("auditor");
  const menu = await menuLabels(aud);
  assert(menu.includes("가져오기 이력") && !menu.includes("기초 데이터 가져오기"), `감사 메뉴: ${menu.join(",")}`);
});

console.log("\n--- 알림 채널(B36) ---");
await step("알림 채널(admin): 메뉴·메일 상태 카드·큐 요약·발송 기록 빈 상태, 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("admin");
  const menu = await menuLabels(page);
  assert(menu.includes("알림 채널") && menu.includes("알림 발송 기록"), `관리자 메뉴: ${menu.join(",")}`);
  await page.goto(`${BASE}/admin/notifications`);
  await page.getByRole("heading", { name: "알림 채널" }).waitFor();
  await page.getByRole("heading", { name: "메일" }).waitFor();
  await page.getByRole("heading", { name: "발송 큐" }).waitFor();
  assert((await page.getByText("설정 안 됨").count()) >= 1, "메일 서버 미설정 상태가 보이지 않음");
  assert(await page.getByRole("button", { name: "시험 메일 보내기" }).isDisabled(), "메일 미설정인데 시험 메일 버튼이 열려 있음");
  await shot(page, "104-notify-admin");
  await narrowCheck(page, "104b-notify-admin-narrow", async () => {
    await page.getByRole("heading", { name: "발송 큐" }).waitFor();
  });
  await page.goto(`${BASE}/admin/notifications/deliveries`);
  await page.getByRole("heading", { name: "알림 발송 기록" }).waitFor();
  await page.locator("table, .empty").first().waitFor();
  await shot(page, "105-notify-deliveries");
  await narrowCheck(page, "105b-notify-deliveries-narrow", async () => {
    await page.getByRole("heading", { name: "알림 발송 기록" }).waitFor();
  });
});
await step("웹훅 대상(admin): 빈 상태·등록 양식(주소는 가려진 입력)·카카오워크 항목·허용 호스트 안내, 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("admin");
  await page.goto(`${BASE}/admin/notifications`);
  await page.getByRole("heading", { name: "슬랙·팀즈·카카오워크" }).waitFor();
  await page.getByText("등록된 웹훅 대상이 없습니다.").waitFor();
  await page.getByRole("button", { name: "웹훅 대상 추가" }).click();
  const url = page.getByLabel("웹훅 주소");
  await url.waitFor();
  assert((await url.getAttribute("type")) === "password", "웹훅 주소 입력이 가려져 있지 않음");
  assert((await page.getByText(/허용 호스트: hooks\.slack\.com/).count()) === 1, "허용 호스트 안내가 없음");
  await page.getByLabel("종류").selectOption("kakaowork");
  await page.getByLabel("앱 키").waitFor();
  await page.getByLabel("대화방 ID").waitFor();
  await shot(page, "106-notify-targets");
  await narrowCheck(page, "106b-notify-targets-narrow", async () => {
    await page.getByRole("heading", { name: "슬랙·팀즈·카카오워크" }).waitFor();
  });
});
await step("내 알림 설정(staff): 앱 안 알림은 끌 수 없다는 안내·종류별 메일 선택·묶음 받기, 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("staff");
  await page.goto(`${BASE}/notifications`);
  await page.getByRole("link", { name: "내 알림 설정" }).click();
  await page.getByRole("heading", { name: "내 알림 설정" }).waitFor();
  await page.getByText(/앱 안 알림은 항상 받습니다/).waitFor();
  await page.getByRole("heading", { name: "업무 시간 외 묶음 받기" }).waitFor();
  const sel = page.getByLabel("결재 요청 메일");
  await sel.waitFor();
  await sel.selectOption("off");
  await page.getByText("결재 요청 설정을 저장했습니다.").waitFor();
  await sel.selectOption("default");
  await page.getByText("결재 요청 설정을 저장했습니다.").waitFor();
  assert((await page.getByText("메일 서버가 설정되어 있지 않아").count()) >= 1, "메일 서버 미설정 안내가 없음");
  await shot(page, "107-notify-prefs");
  await narrowCheck(page, "107b-notify-prefs-narrow", async () => {
    await page.getByRole("heading", { name: "내 알림 설정" }).waitFor();
  });
});
await step("수신 기본값과 설정(admin): 종류별 기본값·필수·설정값 양식이 보인다, 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("admin");
  await page.goto(`${BASE}/admin/notifications`);
  await page.getByRole("heading", { name: "수신 기본값과 설정" }).waitFor();
  await page.getByLabel("결재 요청 메일 기본값").waitFor();
  await page.getByLabel("업무 시작 시각").waitFor();
  assert((await page.getByRole("button", { name: "설정 저장" }).isDisabled()), "바뀐 값이 없는데 설정 저장 단추가 열려 있음");
  await shot(page, "108-notify-defaults");
  await narrowCheck(page, "108b-notify-defaults-narrow", async () => {
    await page.getByRole("heading", { name: "수신 기본값과 설정" }).waitFor();
  });
});
await step("알림 채널 메뉴 표: 직원·결재자·재무에게는 없고 감사에게는 발송 기록만 있다", async () => {
  for (const who of ["staff", "lead", "finance"]) {
    const page = await loginDirect(who);
    const menu = await menuLabels(page);
    assert(!menu.includes("알림 채널") && !menu.includes("알림 발송 기록"), `${who} 메뉴에 알림 채널이 보임: ${menu.join(",")}`);
    await page.goto(`${BASE}/admin/notifications`);
    await page.getByRole("alert").waitFor();
  }
  const aud = await loginDirect("auditor");
  const menu = await menuLabels(aud);
  assert(menu.includes("알림 발송 기록") && !menu.includes("알림 채널"), `감사 메뉴: ${menu.join(",")}`);
  await aud.goto(`${BASE}/admin/notifications/deliveries`);
  await aud.getByRole("heading", { name: "알림 발송 기록" }).waitFor();
  assert((await aud.getByRole("button", { name: "다시 보내기" }).count()) === 0, "감사에게 다시 보내기가 보임");
});

console.log("\n--- 도입 점검표·백업 상태(B38) ---");
await step("도입 점검표(admin): 21개 항목이 상태·근거와 함께 보이고 홈에 요약 카드가 있으며, 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("admin");
  assert((await menuLabels(page)).includes("도입 점검표"), "관리자 메뉴에 도입 점검표가 없음");
  assert((await cardTitles(page)).includes("도입 점검"), "홈에 도입 점검 카드가 없음");
  await page.goto(`${BASE}/admin/onboarding`);
  await page.getByRole("heading", { name: "도입 점검표", exact: true }).waitFor();
  await page.getByRole("heading", { name: /^21\. 백업/ }).waitFor();
  await page.getByRole("heading", { name: /개인정보 기록·권한 분리/ }).waitFor();
  assert((await page.locator("ol.onboarding-list > li").count()) === 21, "점검 항목이 21개가 아님");
  const text = await page.locator("main").innerText();
  assert(!/PL_[A-Z_]+|argon2|secret/i.test(text), "점검표 화면에 비밀·환경변수 이름이 보임");
  await shot(page, "70-admin-onboarding");
  await narrowCheck(page, "70b-admin-onboarding-narrow", async () => {
    await page.getByRole("heading", { name: "도입 점검표", exact: true }).waitFor();
  });
});
await step("도입 점검표 해당 없음(admin): 사유를 적어 표시하면 상태가 바뀌고 풀면 돌아온다", async () => {
  const page = await loginDirect("admin");
  await page.goto(`${BASE}/admin/onboarding`);
  const row = page.locator("li", { has: page.getByRole("heading", { name: /기초 데이터/ }) });
  await row.getByRole("button", { name: "해당 없음으로 표시" }).click();
  await row.getByLabel("해당 없음 사유", { exact: true }).fill("새로 시작하는 회사라 가져올 데이터가 없음");
  await row.getByRole("button", { name: "표시", exact: true }).click();
  await row.getByText("해당 없음 사유: 새로 시작하는 회사라 가져올 데이터가 없음").waitFor();
  await row.getByRole("button", { name: "해당 없음 풀기" }).click();
  await row.getByRole("button", { name: "해당 없음으로 표시" }).waitFor();
});
console.log("\n--- 계정 동기화(B40) ---");
await step("계정 동기화(admin): 토큰 원문은 발급 직후 한 번만 보이고 폐기하면 상태가 바뀐다. 기록 탭이 있고 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("admin");
  assert((await menuLabels(page)).includes("계정 동기화"), "관리자 메뉴에 계정 동기화가 없음");
  await page.goto(`${BASE}/admin/scim`);
  await page.getByRole("heading", { name: "계정 동기화(SCIM)" }).waitFor();
  await page.getByTestId("scim-endpoint").waitFor();
  await page.getByRole("textbox", { name: "이름" }).fill("스모크 연동");
  await page.getByRole("button", { name: "토큰 발급" }).click();
  const fresh = await page.getByTestId("scim-fresh-token").innerText();
  assert(/^pls_[A-Za-z0-9_-]{20,}$/.test(fresh.trim()), `발급한 토큰 원문 형식: ${fresh}`);
  await shot(page, "72-admin-scim");
  await page.getByRole("button", { name: "저장했습니다" }).click();
  assert((await page.getByTestId("scim-fresh-token").count()) === 0, "저장했습니다 뒤에도 토큰 원문이 보임");
  await page.reload();
  await page.getByTestId("scim-endpoint").waitFor();
  assert(!(await page.locator("main").innerText()).includes(fresh.trim()), "새로 불러온 화면에 토큰 원문이 있음");
  const row = page.locator("tr", { hasText: "스모크 연동" });
  await row.getByText("사용 중").waitFor();
  await row.getByRole("button", { name: "폐기" }).click();
  await page.locator('[role="dialog"], [role="alertdialog"]').getByRole("button", { name: "폐기" }).click();
  await page.locator("tr", { hasText: "스모크 연동" }).getByText("폐기됨").waitFor();
  await page.getByRole("tab", { name: "동기화 기록" }).click();
  await page.getByText("동기화 기록이 없습니다.").waitFor();
  await narrowCheck(page, "72b-admin-scim-narrow", async () => {
    await page.getByRole("heading", { name: "계정 동기화(SCIM)" }).waitFor();
  });
  await page.getByRole("tab", { name: "연결·토큰" }).click();
  await narrowCheck(page, "72c-admin-scim-tokens-narrow", async () => {
    await page.getByTestId("scim-endpoint").waitFor();
  });
});
await step("계정 동기화 그룹 매핑(admin): 연동 방식을 고르면 매핑을 만들고 지울 수 있고, 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("admin");
  await page.goto(`${BASE}/admin/scim`);
  await page.getByTestId("scim-endpoint").waitFor();
  await page.getByRole("tab", { name: "그룹 매핑" }).click();
  await page.getByText(/그룹 연동이 꺼져 있습니다/).waitFor();
  await page.getByRole("tab", { name: "연결·토큰" }).click();
  await page.getByTestId("scim-endpoint").waitFor();
  await page.getByLabel("그룹 연동").selectOption("unit");
  await page.getByRole("button", { name: "설정 저장" }).click();
  await page.getByText("설정을 저장했습니다.").waitFor();
  await page.getByRole("tab", { name: "그룹 매핑" }).click();
  await page.getByRole("heading", { name: "그룹 연결" }).waitFor();
  await page.getByLabel("그룹 식별자").fill("smoke-grp");
  await page.getByLabel("표시 이름").fill("스모크 그룹");
  await page.getByLabel("연결할 조직").selectOption({ index: 1 });
  await page.getByRole("button", { name: "연결", exact: true }).click();
  await page.locator("tr", { hasText: "스모크 그룹" }).waitFor();
  await shot(page, "73-admin-scim-groups");
  await narrowCheck(page, "73b-admin-scim-groups-narrow", async () => {
    await page.getByRole("table", { name: "그룹 매핑" }).waitFor();
  });
  await page.locator("tr", { hasText: "스모크 그룹" }).getByRole("button", { name: "삭제" }).click();
  await page.locator('[role="dialog"], [role="alertdialog"]').getByRole("button", { name: "삭제" }).click();
  await page.getByText("연결한 그룹이 없습니다.").waitFor();
  await page.getByRole("tab", { name: "연결·토큰" }).click();
  await page.getByLabel("그룹 연동").selectOption("off");
  await page.getByRole("button", { name: "설정 저장" }).click();
  await page.getByText("설정을 저장했습니다.").waitFor();
});
await step("계정 동기화(auditor): 조회만 되고 발급·폐기 단추가 없으며, 직원·재무에게는 메뉴가 없다", async () => {
  const aud = await loginDirect("auditor");
  assert((await menuLabels(aud)).includes("계정 동기화"), "감사 메뉴에 계정 동기화가 없음");
  await aud.goto(`${BASE}/admin/scim`);
  await aud.getByTestId("scim-endpoint").waitFor();
  assert((await aud.getByRole("button", { name: "토큰 발급" }).count()) === 0, "감사에게 토큰 발급이 보임");
  assert((await aud.getByRole("button", { name: "설정 저장" }).count()) === 0, "감사에게 설정 저장이 보임");
  for (const who of ["staff", "finance"]) {
    const p = await loginDirect(who);
    assert(!(await menuLabels(p)).includes("계정 동기화"), `${who} 메뉴에 계정 동기화가 보임`);
    await p.goto(`${BASE}/admin/scim`);
    await p.getByRole("alert").waitFor();
  }
});
console.log("\n--- 개인정보 조회 기록(B41) ---");
await step("개인정보 조회 기록(auditor): 관리자의 원문 포함 내보내기가 기록되고 정보주체·통계·무결성 확인이 보이며, 폭 390 에서 넘치지 않는다", async () => {
  const admin = await loginDirect("admin");
  const status = await admin.evaluate(async () => (await fetch("/api/org/people/export?format=csv&raw=1", { credentials: "same-origin" })).status);
  assert(status === 200, `원문 포함 내보내기 ${status}`);
  const aud = await loginDirect("auditor");
  assert((await menuLabels(aud)).includes("개인정보 조회 기록"), "감사 메뉴에 개인정보 조회 기록이 없음");
  await aud.goto(`${BASE}/admin/privacy-log`);
  await aud.getByRole("heading", { name: "개인정보 조회 기록" }).waitFor();
  const table = aud.getByRole("table", { name: "조회 기록" });
  await table.waitFor();
  await table.getByText("조직 구성원 내보내기").first().waitFor();
  await aud.getByText(/법무 확인/).waitFor();
  await table.getByRole("button", { name: "정보주체 보기" }).first().click();
  await aud.getByRole("list", { name: "정보주체" }).waitFor();
  const text = await aud.locator("main").innerText();
  assert(!/staff@pl\.test|010-\d{4}-\d{4}/.test(text), "조회 기록 화면에 원문이 보임");
  await shot(aud, "74-admin-privacy-log");
  await aud.getByRole("button", { name: "무결성 확인" }).click();
  await aud.getByText(/어긋난 곳이 없습니다/).waitFor();
  await aud.getByRole("tab", { name: "열람 통계" }).click();
  await aud.getByLabel("요약").waitFor();
  await aud.getByRole("tab", { name: "조회 기록" }).click();
  await narrowCheck(aud, "74b-admin-privacy-log-narrow", async () => {
    await aud.getByRole("table", { name: "조회 기록" }).waitFor();
  });
  // 관리자는 기록을 읽지 못한다(보관 설정만). 직원·재무에게는 메뉴가 없다.
  await admin.goto(`${BASE}/admin/privacy-log`);
  await admin.getByRole("alert").waitFor();
  for (const who of ["staff", "finance"]) {
    const p = await loginDirect(who);
    const m = await menuLabels(p);
    assert(!m.includes("개인정보 조회 기록") && !m.includes("조회 기록 보관 설정"), `${who} 메뉴에 조회 기록이 보임`);
  }
});
await step("조회 기록 보관 설정(admin): 보관 기간을 바꾸면 저장되고, 줄이면 확인을 거친다", async () => {
  const page = await loginDirect("admin");
  assert((await menuLabels(page)).includes("조회 기록 보관 설정"), "관리자 메뉴에 보관 설정이 없음");
  await page.goto(`${BASE}/admin/privacy-settings`);
  await page.getByRole("heading", { name: "조회 기록 보관 설정" }).waitFor();
  const input = page.getByLabel("보관 기간(일)");
  await input.waitFor();
  assert((await input.inputValue()) === "730", "기본 보관 기간이 730일이 아님");
  await input.fill("800");
  await page.getByRole("button", { name: "저장" }).click();
  await page.getByText(/보관 기간을 저장했습니다/).waitFor();
  await input.fill("400");
  await page.getByRole("button", { name: "저장" }).click();
  await page.locator('[role="dialog"], [role="alertdialog"]').getByRole("button", { name: "저장" }).click();
  await page.getByText(/보관 기간을 저장했습니다/).waitFor();
  await shot(page, "75-admin-privacy-settings");
  await narrowCheck(page, "75b-admin-privacy-settings-narrow", async () => {
    await page.getByRole("heading", { name: "조회 기록 보관 설정" }).waitFor();
  });
});
console.log("\n--- 정보주체 열람 요청·권한 분리(B41 2단계) ---");
await step("정보주체 열람 요청: 직원이 요청하고 다른 관리자가 승인하면 결과가 부서·역할로만 보인다", async () => {
  const staff = await loginDirect("staff");
  await staff.goto(`${BASE}/privacy/my-requests`);
  await staff.getByRole("heading", { name: "내 정보 열람 요청" }).waitFor();
  await staff.getByLabel("사유(선택)").fill("내 정보 확인");
  await staff.getByRole("button", { name: "요청하기" }).click();
  await staff.getByText(/요청을 접수했습니다/).waitFor();
  await staff.getByText("처리 대기").first().waitFor();
  await shot(staff, "76-my-privacy-request");
  const admin = await loginDirect("admin");
  await admin.goto(`${BASE}/admin/privacy/requests`);
  await admin.getByRole("heading", { name: "정보주체 열람 요청" }).waitFor();
  const row = admin.getByRole("table", { name: "열람 요청 목록" }).getByRole("row").filter({ hasText: "내 정보 확인" }).first();
  await row.getByRole("button", { name: "승인" }).click();
  await admin.getByText(/승인했습니다/).waitFor();
  await shot(admin, "76b-admin-privacy-requests");
  await staff.goto(`${BASE}/privacy/my-requests`);
  await staff.getByText("결과 있음").first().waitFor();
  await staff.getByRole("button", { name: "결과 보기" }).first().click();
  const result = staff.getByRole("table", { name: "내 정보 열람 결과" });
  await result.waitFor();
  const text = await result.innerText();
  assert(/관리자/.test(text), `결과에 열람자 역할이 없음: ${text}`);
  assert(!/admin@pl\.test|010-\d{4}-\d{4}/.test(text), "결과에 원문이 보임");
  await narrowCheck(staff, "76c-my-privacy-request-narrow", async () => {
    await staff.getByRole("heading", { name: "내 정보 열람 요청" }).waitFor();
  });
});
await step("권한 분리 규칙(admin·auditor): 관리자는 모드를 바꾸고 감사는 보기만 하며, 직원·재무에게는 메뉴가 없다", async () => {
  const admin = await loginDirect("admin");
  assert((await menuLabels(admin)).includes("권한 분리 규칙"), "관리자 메뉴에 권한 분리 규칙이 없음");
  await admin.goto(`${BASE}/admin/sod/rules`);
  await admin.getByRole("heading", { name: "권한 분리 규칙" }).waitFor();
  const table = admin.getByRole("table", { name: "권한 분리 규칙" });
  await table.waitFor();
  await table.getByText("잠김").first().waitFor();
  const select = table.getByLabel("지급 요청 작성자의 최종 승인 모드");
  await select.selectOption("block");
  await admin.getByText(/차단 모드로 바꿨습니다/).waitFor();
  await shot(admin, "77-admin-sod-rules");
  await select.selectOption("warn"); // 차단을 낮추면 확인을 거친다
  await admin.locator('[role="dialog"], [role="alertdialog"]').getByRole("button", { name: "바꾸기" }).click();
  await admin.getByText(/경고 모드로 바꿨습니다/).waitFor();
  await narrowCheck(admin, "77b-admin-sod-rules-narrow", async () => {
    await admin.getByRole("table", { name: "권한 분리 규칙" }).waitFor();
  });
  const aud = await loginDirect("auditor");
  const m = await menuLabels(aud);
  assert(m.includes("권한 분리 위반") && m.includes("권한 분리 예외"), `감사 메뉴: ${m.join(",")}`);
  await aud.goto(`${BASE}/admin/sod/rules`);
  await aud.getByRole("table", { name: "권한 분리 규칙" }).waitFor();
  assert((await aud.getByRole("table", { name: "권한 분리 규칙" }).getByRole("combobox").count()) === 0, "감사에게 모드 변경 선택이 보임");
  for (const who of ["staff", "finance"]) {
    const p = await loginDirect(who);
    const labels = await menuLabels(p);
    assert(!labels.some((l) => l.startsWith("권한 분리")), `${who} 메뉴에 권한 분리가 보임`);
    await p.goto(`${BASE}/admin/sod/rules`);
    await p.getByRole("alert").waitFor();
  }
});
await step("권한 분리 위반·예외(auditor·admin): 목록이 보이고 예외 요청 양식은 관리자에게만, 승인은 감사에게만 보인다", async () => {
  const aud = await loginDirect("auditor");
  await aud.goto(`${BASE}/admin/sod/violations`);
  await aud.getByRole("heading", { name: "권한 분리 위반" }).waitFor();
  await aud.getByLabel("결과").waitFor();
  const reportLink = aud.getByRole("link", { name: "엑셀로 내려받기" });
  await reportLink.waitFor();
  const href = await reportLink.getAttribute("href");
  assert(/^\/api\/audit\/privacy\/report\?month=\d{4}-\d{2}$/.test(href ?? ""), `보고서 링크: ${href}`);
  const rep = await aud.evaluate(async (h) => {
    const r = await fetch(h, { credentials: "same-origin" });
    const b = new Uint8Array(await r.arrayBuffer());
    return { status: r.status, magic: String.fromCharCode(b[0], b[1]) };
  }, href);
  assert(rep.status === 200 && rep.magic === "PK", `보고서 내려받기 ${JSON.stringify(rep)}`);
  await shot(aud, "78-admin-sod-violations");
  await aud.goto(`${BASE}/admin/sod/exceptions`);
  await aud.getByRole("heading", { name: "권한 분리 예외" }).waitFor();
  assert((await aud.getByRole("button", { name: "예외 요청" }).count()) === 0, "감사에게 예외 요청 양식이 보임");
  const admin = await loginDirect("admin");
  await admin.goto(`${BASE}/admin/sod/violations`);
  await admin.getByRole("heading", { name: "권한 분리 위반" }).waitFor();
  assert((await admin.getByRole("link", { name: "엑셀로 내려받기" }).count()) === 0, "관리자에게 보고서 내려받기가 보임");
  await admin.goto(`${BASE}/admin/sod/exceptions`);
  await admin.getByRole("button", { name: "예외 요청" }).waitFor();
  const options = await admin.getByLabel("규칙").locator("option").allInnerTexts();
  assert(!options.includes("지급 요청 작성자의 지급 처리"), "잠긴 규칙이 예외 요청 선택지에 있음");
  await shot(admin, "78b-admin-sod-exceptions");
  await narrowCheck(admin, "78c-admin-sod-exceptions-narrow", async () => {
    await admin.getByRole("heading", { name: "권한 분리 예외" }).waitFor();
  });
});
console.log("\n--- 연동 API 토큰(B42 1단계) ---");
await step("연동 관리(admin): API 를 켜고 토큰을 발급하면 원문이 한 번만 보이고, 그 토큰으로 변경 피드를 읽은 뒤 폐기하며, 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("admin");
  assert((await menuLabels(page)).includes("연동 관리"), "관리자 메뉴에 연동 관리가 없음");
  await page.goto(`${BASE}/admin/integrations`);
  await page.getByRole("heading", { name: "연동 관리" }).waitFor();
  await page.getByRole("button", { name: "켜기" }).click();
  await page.getByText(/연동 API 를 켰습니다/).waitFor();
  const form = page.getByRole("form", { name: "토큰 발급" });
  await form.getByLabel("이름", { exact: true }).fill("스모크 연동");
  await form.getByRole("checkbox", { name: /변경 피드 읽기/ }).check();
  await form.getByRole("button", { name: "토큰 발급" }).click();
  const fresh = page.getByTestId("api-fresh-token");
  await fresh.waitFor();
  const token = (await fresh.innerText()).trim();
  assert(/^pli_[A-Za-z0-9_-]{40,}$/.test(token), `토큰 형식: ${token.slice(0, 8)}`);
  await shot(page, "79-admin-integrations");
  const res = await page.evaluate(async (tok) => {
    const r = await fetch("/api/v1/changes/head", { headers: { Authorization: `Bearer ${tok}` } });
    return { status: r.status, body: await r.text() };
  }, token);
  assert(res.status === 200 && /next_cursor|cursor/.test(res.body), `변경 피드 위치 ${res.status} ${res.body.slice(0, 80)}`);
  await page.getByRole("button", { name: "저장했습니다" }).click();
  assert(!(await page.locator("main").innerText()).includes(token), "토큰 원문이 화면에 남아 있다");
  const table = page.getByRole("table", { name: "API 토큰" });
  const row = table.getByRole("row").filter({ hasText: "스모크 연동" });
  await row.waitFor();
  assert(/pli_/.test(await row.innerText()), "토큰 앞부분이 목록에 없다");
  await narrowCheck(page, "79b-admin-integrations-narrow", async () => {
    await page.getByRole("heading", { name: "연동 관리" }).waitFor();
  });
  await row.getByRole("button", { name: "폐기" }).click();
  await page.locator('[role="dialog"], [role="alertdialog"]').getByRole("button", { name: "폐기" }).click();
  await page.getByText(/토큰을 폐기했습니다/).waitFor();
  await table.getByRole("row").filter({ hasText: "스모크 연동" }).getByText("폐기", { exact: true }).waitFor();
  for (const who of ["finance", "auditor", "staff"]) {
    const p = await loginDirect(who);
    assert(!(await menuLabels(p)).includes("연동 관리"), `${who} 메뉴에 연동 관리가 보임`);
    await p.goto(`${BASE}/admin/integrations`);
    await p.getByRole("alert").waitFor();
  }
});
await step("연동 관리 쓰기 범위(admin): 쓰기 범위에 배지·안내가 보이고 발급 전에 확인을 거치며, 전표 작성 권한이 없으면 전표 초안은 고를 수 없다", async () => {
  const page = await loginDirect("admin");
  await page.goto(`${BASE}/admin/integrations`);
  const form = page.getByRole("form", { name: "토큰 발급" });
  await form.waitFor();
  const draft = form.getByRole("checkbox", { name: /전표 초안 제출/ });
  assert(await draft.isDisabled(), "전표 작성 권한이 없는 관리자가 전표 초안을 고를 수 있다");
  await form.getByLabel("이름", { exact: true }).fill("스모크 쓰기");
  const vendorsWrite = form.getByRole("checkbox", { name: /^거래처 가져오기/ });
  await vendorsWrite.check();
  await form.getByText(/화면 가져오기와 같은 규칙/).waitFor();
  await shot(page, "79c-admin-integrations-write");
  await form.getByRole("button", { name: "토큰 발급" }).click();
  const dialog = page.locator('[role="dialog"], [role="alertdialog"]');
  await dialog.getByText(/허용 출발지를 좁히고/).waitFor();
  await dialog.getByRole("button", { name: "발급" }).click();
  await page.getByTestId("api-fresh-token").waitFor();
  await page.getByRole("button", { name: "저장했습니다" }).click();
  const row = page.getByRole("table", { name: "API 토큰" }).getByRole("row").filter({ hasText: "스모크 쓰기" });
  await row.getByRole("button", { name: "폐기" }).click();
  await page.locator('[role="dialog"], [role="alertdialog"]').getByRole("button", { name: "폐기" }).click();
  await page.getByText(/토큰을 폐기했습니다/).waitFor();
});
await step("변경 웹훅(admin·auditor): 구독을 등록하면 서명 비밀이 한 번만 보이고, 수정·중지·비밀 재발급이 되며, 감사는 발송 기록만 보고, 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("admin");
  assert((await menuLabels(page)).includes("변경 웹훅"), "관리자 메뉴에 변경 웹훅이 없음");
  await page.goto(`${BASE}/admin/integrations/webhooks`);
  await page.getByRole("heading", { name: "변경 웹훅" }).waitFor();
  const form = page.getByRole("form", { name: "구독 양식" });
  await form.getByLabel("이름").fill("스모크 웹훅");
  await form.getByLabel("받을 주소(https)").fill("http://localhost:9/hook");
  await form.getByRole("checkbox", { name: "품목 변경" }).check();
  await form.getByRole("button", { name: "구독 등록" }).click();
  const secret = page.getByTestId("webhook-secret");
  await secret.waitFor();
  assert(/^whsec_[A-Za-z0-9_-]{40,}$/.test((await secret.innerText()).trim()), "서명 비밀 형식");
  await shot(page, "80-admin-webhooks");
  await page.getByRole("button", { name: "저장했습니다" }).click();
  // 시험 서버가 바깥 주소로 나가지 않게 등록하자마자 중지한다.
  const table = page.getByRole("table", { name: "웹훅 구독" });
  const row = table.getByRole("row").filter({ hasText: "스모크 웹훅" });
  await row.getByRole("button", { name: "중지" }).click();
  await page.getByText(/구독을 중지했습니다/).waitFor();
  assert(!/localhost:9\/hook/.test(await page.locator("main").innerText()), "전체 주소가 화면에 보인다");
  await row.getByRole("button", { name: "수정" }).click();
  await form.getByLabel("이름").fill("스모크 웹훅 2");
  await form.getByRole("button", { name: "수정 저장" }).click();
  await page.getByText(/구독을 수정했습니다/).waitFor();
  await table.getByRole("row").filter({ hasText: "스모크 웹훅 2" }).getByRole("button", { name: "비밀 재발급" }).click();
  await page.locator('[role="dialog"], [role="alertdialog"]').getByRole("button", { name: "재발급" }).click();
  await secret.waitFor();
  await page.getByRole("button", { name: "저장했습니다" }).click();
  await page.getByRole("table", { name: "웹훅 발송 기록" }).or(page.getByText("발송 기록이 없습니다.")).first().waitFor();
  await narrowCheck(page, "80b-admin-webhooks-narrow", async () => {
    await page.getByRole("heading", { name: "변경 웹훅" }).waitFor();
  });
  const aud = await loginDirect("auditor");
  assert((await menuLabels(aud)).includes("변경 웹훅"), "감사 메뉴에 변경 웹훅이 없음");
  await aud.goto(`${BASE}/admin/integrations/webhooks`);
  await aud.getByRole("heading", { name: "변경 웹훅" }).waitFor();
  assert((await aud.getByRole("form", { name: "구독 양식" }).count()) === 0, "감사에게 구독 양식이 보임");
  for (const who of ["finance", "staff"]) {
    const p = await loginDirect(who);
    assert(!(await menuLabels(p)).includes("변경 웹훅"), `${who} 메뉴에 변경 웹훅이 보임`);
    await p.goto(`${BASE}/admin/integrations/webhooks`);
    await p.getByRole("alert").waitFor();
  }
});
await step("법인 관리(B44 admin·finance): 기본 법인이 보이고 법인을 등록·수정·비활성화할 수 있으며, 재무에게는 메뉴가 없고, 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("admin");
  assert((await menuLabels(page)).includes("법인 관리"), "관리자 메뉴에 법인 관리가 없음");
  await page.goto(`${BASE}/admin/entities`);
  await page.getByRole("heading", { name: "법인 관리" }).waitFor();
  const table = page.getByRole("table", { name: "법인 목록" });
  await table.getByText("기본 법인").waitFor();
  const form = page.getByRole("form", { name: "법인 양식" });
  await form.getByLabel("법인 코드").fill("SMK");
  await form.getByLabel("법인 이름").fill("스모크 계열사");
  await form.getByLabel("사업자번호").fill("220-81-62517");
  await form.getByRole("button", { name: "등록", exact: true }).click();
  await page.getByText("법인을 등록했습니다.").waitFor();
  const row = table.getByRole("row").filter({ hasText: "스모크 계열사" });
  await row.getByRole("button", { name: "스모크 계열사 수정" }).click();
  await form.getByLabel("법인 이름").fill("스모크 계열사 2");
  await form.getByRole("button", { name: "저장", exact: true }).click();
  await page.getByText("법인 정보를 저장했습니다.").waitFor();
  await table.getByRole("row").filter({ hasText: "스모크 계열사 2" }).getByRole("button", { name: "스모크 계열사 2 비활성화" }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: "비활성화" }).click();
  await page.getByText(/비활성화했습니다/).waitFor();
  const body = await page.locator("main").innerText();
  assert(!/isDefault|rootUnitId|entity_id|\bown\b/.test(body), "법인 관리 화면에 내부 코드가 보임");
  await shot(page, "84-admin-entities");
  await narrowCheck(page, "84b-admin-entities-narrow", async () => {
    await page.getByRole("heading", { name: "법인 관리" }).waitFor();
  });
  const fin = await loginDirect("finance");
  assert(!(await menuLabels(fin)).includes("법인 관리"), "재무 메뉴에 법인 관리가 보임");
  await fin.goto(`${BASE}/admin/entities`);
  await fin.getByRole("alert").waitFor();
});
await step("LLM 공급자·라우팅(B45 admin·finance): 시험용 공급자를 등록·켜기·시험 연결하고, 라우팅 규칙을 등록하면 경로 확인에 보이며, 키·주소 원문이 없고, 재무에게는 메뉴가 없고, 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("admin");
  const labels = await menuLabels(page);
  assert(labels.includes("LLM 공급자") && labels.includes("LLM 라우팅"), "관리자 메뉴에 LLM 공급자·라우팅이 없음");
  await page.goto(`${BASE}/admin/llm/providers`);
  await page.getByRole("heading", { name: "LLM 공급자" }).waitFor();
  const form = page.getByRole("form", { name: "공급자 양식" });
  await form.getByLabel("종류").selectOption({ label: "모의(시험용)" });
  await form.getByLabel("공급자 코드").fill("smk-mock");
  await form.getByLabel("이름").fill("스모크 모의");
  await form.getByRole("button", { name: "등록", exact: true }).click();
  await page.getByText("공급자를 등록했습니다.").waitFor();
  const table = page.getByRole("table", { name: "공급자 목록" });
  await table.getByRole("button", { name: "스모크 모의 켜기" }).click();
  await page.getByText(/스모크 모의 을\(를\) 켰습니다/).waitFor();
  await table.getByRole("button", { name: "스모크 모의 시험 연결" }).click();
  await page.getByText(/스모크 모의: 연결됐습니다/).waitFor();
  const body = await page.locator("main").innerText();
  assert(!/secret|openai_compat|sk-|enc:v1/.test(body), "공급자 화면에 내부 값이 보임");
  await shot(page, "85-admin-llm-providers");
  await narrowCheck(page, "85b-admin-llm-providers-narrow", async () => {
    await page.getByRole("heading", { name: "LLM 공급자" }).waitFor();
  });
  await page.goto(`${BASE}/admin/llm/routes`);
  await page.getByRole("heading", { name: "LLM 라우팅" }).waitFor();
  const rf = page.getByRole("form", { name: "규칙 양식" });
  await rf.getByLabel("용도 패턴").fill("smoke.never");
  await rf.getByLabel("주 공급자").selectOption({ label: "스모크 모의" });
  await rf.getByRole("button", { name: "등록", exact: true }).click();
  await page.getByText("규칙을 등록했습니다.").waitFor();
  const sim = page.getByRole("form", { name: "경로 확인 양식" });
  await sim.getByLabel("용도").fill("smoke.never");
  await sim.getByRole("button", { name: "확인", exact: true }).click();
  await page.getByText(/smk-mock .*로 갑니다/).waitFor();
  await shot(page, "85c-admin-llm-routes");
  await page.getByRole("button", { name: "smoke.never 규칙 삭제" }).click();
  await page.getByText("규칙을 삭제했습니다.").waitFor();
  const fin = await loginDirect("finance");
  const fl = await menuLabels(fin);
  assert(!fl.includes("LLM 공급자") && !fl.includes("LLM 라우팅"), "재무 메뉴에 LLM 공급자·라우팅이 보임");
  await fin.goto(`${BASE}/admin/llm/providers`);
  await fin.getByRole("alert").waitFor();
});
await step("평가 세트(B46 admin·auditor·finance): 세트를 만들고 문항을 추가·CSV 로 가져와 게시하면 고칠 수 없고, 개인정보는 가려지며, 게시한 세트를 실행해(모의) 결과·실패 문항·기준선 지정이 되고, 감사는 읽기만 되고 재무에게는 메뉴가 없으며, 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("admin");
  assert((await menuLabels(page)).includes("평가 세트"), "관리자 메뉴에 평가 세트가 없음");
  await page.goto(`${BASE}/admin/eval/sets`);
  await page.getByRole("heading", { name: "평가 세트" }).waitFor();
  const form = page.getByRole("form", { name: "세트 양식" });
  await form.getByLabel("세트 코드").fill("smk-eval");
  await form.getByLabel("이름").fill("스모크 평가");
  await form.getByRole("button", { name: "만들기", exact: true }).click();
  await page.getByText("평가 세트를 만들었습니다.").waitFor();
  await page.getByRole("link", { name: "스모크 평가" }).click();
  await page.getByRole("heading", { name: "스모크 평가" }).waitFor();
  await page.getByRole("button", { name: "문항 추가" }).click();
  const iform = page.getByRole("form", { name: "문항 양식" });
  await iform.getByLabel("질문").fill("거래처 사업자번호 123-45-67890 로 문의해도 되나요?");
  await iform.getByLabel("기대 답").fill("가려서 안내합니다");
  await iform.getByLabel("채점 기준").selectOption({ label: "포함" });
  await iform.getByRole("button", { name: "저장", exact: true }).click();
  await page.getByText(/개인정보 1건을 가려서 저장했습니다/).waitFor();
  const body = await page.locator("main").innerText();
  assert(!body.includes("123-45-67890"), "문항 화면에 사업자번호 원문이 보임");
  await page.locator('input[type="file"]').setInputFiles({ name: "q.csv", mimeType: "text/csv", buffer: Buffer.from("문항,기대,채점\n연차는 며칠인가요?,15,숫자\n", "utf-8") });
  await page.getByText(/1개 문항을 가져왔습니다/).waitFor();
  await page.getByRole("button", { name: "이 초안 게시" }).click();
  await page.getByText(/버전을 게시했습니다/).waitFor();
  assert((await page.getByRole("button", { name: /문항 수정/ }).count()) === 0, "게시된 버전에 수정 단추가 있음");
  await page.getByText(/게시된 버전은 고칠 수 없습니다/).first().waitFor();
  const runForm = page.getByRole("form", { name: "세트 실행 양식" });
  assert(await runForm.getByRole("button", { name: "세트 실행" }).isDisabled(), "비용 확인 전에 세트 실행 단추가 켜져 있음");
  await runForm.getByLabel(/비용과 한도 사용을 확인했습니다/).check();
  await runForm.getByRole("button", { name: "세트 실행" }).click();
  const runTable = page.getByRole("table", { name: "실행 이력" });
  await runTable.getByText("기준 미달").waitFor();
  await runTable.getByRole("link", { name: /^#\d+$/ }).first().click();
  await page.getByRole("heading", { name: /^평가 실행 #\d+$/ }).waitFor();
  const failedRows = page.getByRole("table", { name: "실패 문항" }).locator("tbody tr");
  assert((await failedRows.count()) === 2, "모의 실행의 실패 문항이 2건이 아님");
  const runBody = await page.locator("main").innerText();
  assert(!runBody.includes("123-45-67890"), "실행 결과 화면에 사업자번호 원문이 보임");
  assert(!/\b(manual|running|failed|done)\b/.test(runBody), "실행 결과 화면에 내부 상태 코드가 보임");
  await page.getByRole("button", { name: "기준선으로 지정" }).click();
  await page.getByText(/기준선으로 지정했습니다/).waitFor();
  await page.getByRole("button", { name: "기준선 해제" }).waitFor();
  await shot(page, "87-eval-run");
  const runUrl = page.url();
  await page.goBack();
  await page.getByRole("heading", { name: "스모크 평가" }).waitFor();
  await shot(page, "86-eval-set");
  await narrowCheck(page, "86b-eval-set-narrow", async () => {
    await page.getByRole("heading", { name: "스모크 평가" }).waitFor();
  });
  const aud = await loginDirect("auditor");
  assert((await menuLabels(aud)).includes("평가 세트"), "감사 메뉴에 평가 세트가 없음");
  await aud.goto(`${BASE}/admin/eval/sets`);
  await aud.getByRole("table", { name: "평가 세트 목록" }).waitFor();
  assert((await aud.getByRole("form", { name: "세트 양식" }).count()) === 0, "감사에게 세트 만들기 양식이 보임");
  await aud.goto(runUrl);
  await aud.getByRole("heading", { name: /^평가 실행 #\d+$/ }).waitFor();
  assert((await aud.getByRole("button", { name: /기준선/ }).count()) === 0, "감사에게 기준선 단추가 보임");
  await aud.goto(runUrl.replace(/\/runs\/\d+$/, "/sets/smk-eval"));
  await aud.getByRole("table", { name: "실행 이력" }).waitFor();
  assert((await aud.getByRole("form", { name: "세트 실행 양식" }).count()) === 0, "감사에게 세트 실행 양식이 보임");
  const fin = await loginDirect("finance");
  assert(!(await menuLabels(fin)).includes("평가 세트"), "재무 메뉴에 평가 세트가 보임");
  await fin.goto(`${BASE}/admin/eval/sets`);
  await fin.getByRole("alert").waitFor();
  await fin.goto(runUrl);
  await fin.getByRole("alert").waitFor();
});
await step("저장 전 평가(B46 3단계 admin·auditor): 세트에 용도를 지정하고, 라우팅 규칙을 기준선보다 낮게 저장하려 하면 확인 칸이 뜨고 그래도 저장하면 저장되며, 폭 390 에서 넘치지 않는다", async () => {
  await api("admin", "POST", "/api/admin/eval/sets", { code: "smk-gate", name: "스모크 게이트", purpose: "text" });
  await api("admin", "POST", "/api/admin/eval/sets/smk-gate/items", { question: "게이트 시험 질문", expected: "게이트", grader: "contains" });
  await api("admin", "POST", "/api/admin/eval/sets/smk-gate/publish", { note: "v1" });
  const started = await api("admin", "POST", "/api/admin/eval/sets/smk-gate/runs", { confirmCost: true });
  let done = null;
  for (let i = 0; i < 100 && !done; i++) {
    const r = await api("admin", "GET", `/api/admin/eval/runs/${started.id}`);
    if (r.status !== "running") done = r;
    else await new Promise((res) => setTimeout(res, 200));
  }
  assert(done && done.status === "done" && done.passRatePct === 100, "기준선용 실행이 100% 로 끝나지 않음");
  await api("admin", "PUT", "/api/admin/eval/sets/smk-gate/baseline", { runId: started.id });
  await api("admin", "POST", "/api/admin/eval/sets/smk-gate/draft", {});
  await api("admin", "POST", "/api/admin/eval/sets/smk-gate/items", { question: "둘째 질문", expected: "응답에없는말", grader: "contains" });
  await api("admin", "POST", "/api/admin/eval/sets/smk-gate/publish", { note: "v2" });
  const page = await loginDirect("admin");
  await page.goto(`${BASE}/admin/eval/sets/smk-gate`);
  await page.getByRole("heading", { name: "스모크 게이트" }).waitFor();
  const gform = page.getByRole("form", { name: "저장 전 평가 양식" });
  await gform.getByLabel("용도 패턴").fill("smkgate.*");
  await gform.getByRole("button", { name: "지정 저장", exact: true }).click();
  await page.getByText("저장 전 평가 지정을 저장했습니다.").waitFor();
  await page.getByRole("table", { name: "저장 전 평가 지정" }).getByText("smkgate.*").waitFor();
  const setBody = await page.locator("main").innerText();
  assert(!/\b(blocked|confirm_cost|published)\b/.test(setBody), "세트 화면에 내부 상태 코드가 보임");
  await shot(page, "89-eval-gate");
  await narrowCheck(page, "89b-eval-gate-narrow", async () => {
    await page.getByRole("heading", { name: "스모크 게이트" }).waitFor();
  });
  await page.goto(`${BASE}/admin/llm/routes`);
  await page.getByRole("heading", { name: "LLM 라우팅" }).waitFor();
  const rf = page.getByRole("form", { name: "규칙 양식" });
  await rf.getByLabel("용도 패턴").fill("smkgate.x");
  await rf.getByLabel("주 공급자").selectOption({ label: "스모크 모의" });
  await rf.getByRole("button", { name: "등록", exact: true }).click();
  const group = page.getByRole("group", { name: "저장 전 평가 확인" });
  await group.getByText(/기준선\(100%\)보다 낮습니다\(50%, 치명 실패 0건\)/).waitFor();
  const gbody = await page.locator("main").innerText();
  assert(!/\{"error"|blocked|confirm_cost/.test(gbody), "확인 칸에 내부 값이 보임");
  assert((await page.getByRole("table", { name: "라우팅 규칙" }).getByText("smkgate.x").count()) === 0, "확인 전에 규칙이 저장됨");
  await shot(page, "89c-eval-gate-confirm");
  await narrowCheck(page, "89d-eval-gate-confirm-narrow", async () => {
    await page.getByRole("group", { name: "저장 전 평가 확인" }).waitFor();
  });
  await group.getByLabel(/무시 사유/).fill("스모크 시험");
  await group.getByRole("button", { name: "그래도 저장", exact: true }).click();
  await page.getByText(/규칙을 등록했습니다\. 기준선\(100%\)보다 낮습니다/).waitFor();
  await page.getByRole("button", { name: "smkgate.x 규칙 삭제" }).click();
  await page.getByText("규칙을 삭제했습니다.").waitFor();
  const aud = await loginDirect("auditor");
  await aud.goto(`${BASE}/admin/eval/sets/smk-gate`);
  await aud.getByRole("table", { name: "저장 전 평가 지정" }).waitFor();
  assert((await aud.getByRole("form", { name: "저장 전 평가 양식" }).count()) === 0, "감사에게 게이트 지정 양식이 보임");
  await api("admin", "PUT", "/api/admin/eval/gates", { purposePattern: "smkgate.*", setCode: "smk-gate", enabled: false });
});
await step("AI 도구 만들기(B48 1단계 admin·auditor·finance): 자원·조건·보여 줄 칸을 골라 시험 실행(내 권한 기준)한 뒤 도구로 만들고, 시험을 통과하면 켜고 한도를 정하며 버전이 보이고, 감사는 읽기만 되고 재무에게는 메뉴가 없으며, 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("admin");
  assert((await menuLabels(page)).includes("AI 도구 만들기"), "관리자 메뉴에 AI 도구 만들기가 없음");
  await page.goto(`${BASE}/admin/mcp-tools`);
  await page.getByRole("heading", { name: "AI 도구 만들기", exact: true }).waitFor();
  await page.getByRole("link", { name: "새 도구" }).click();
  const form = page.getByRole("form", { name: "도구 양식" });
  await form.getByLabel("도구 이름(화면용)").fill("스모크 구매요청");
  await form.getByLabel("도구 코드").fill("custom_smoke_prs");
  await form.getByLabel("AI 에게 보이는 설명").fill("구매요청을 상태로 거른다. 금액은 원 단위이다.");
  await form.getByLabel("대상 자원", { exact: true }).selectOption({ label: "구매요청" });
  await form.getByRole("button", { name: "조건 추가" }).click();
  await form.getByLabel("칸", { exact: true }).selectOption({ label: "상태" });
  await form.getByLabel("값", { exact: true }).selectOption({ label: "승인" });
  for (const l of ["번호", "제목", "상태", "합계(원)"]) await form.getByLabel(l, { exact: true }).check();
  await page.getByRole("button", { name: "시험 실행" }).click();
  await page.getByRole("table", { name: "시험 실행 결과" }).waitFor();
  await page.getByText(/내 권한·열람 범위로 본 결과/).waitFor();
  await page.getByText(/이 도구를 부를 수 있는 역할: .*관리자/).waitFor();
  await form.getByRole("button", { name: "도구 만들기" }).click();
  await page.getByRole("heading", { name: "스모크 구매요청" }).waitFor();
  await page.getByRole("table", { name: "버전 목록" }).getByText("현재").waitFor();
  const enableCard = page.getByRole("heading", { name: "켜기와 호출 한도" }).locator("xpath=ancestor::section[1]");
  assert(await enableCard.getByRole("button", { name: "켜기", exact: true }).isDisabled(), "시험 실행 전에 켜기 단추가 켜져 있음");
  await page.getByRole("button", { name: "시험 실행" }).click();
  await page.getByRole("table", { name: "시험 실행 결과" }).waitFor();
  await enableCard.getByText("성공", { exact: true }).waitFor();
  await enableCard.getByRole("button", { name: "켜기", exact: true }).click();
  await page.getByText(/도구를 켰습니다/).waitFor();
  await enableCard.getByLabel(/분당 호출 한도/).fill("5");
  await enableCard.getByRole("button", { name: "한도 저장" }).click();
  await page.getByText(/분당 호출 한도를 저장했습니다/).waitFor();
  const body = await page.locator("main").innerText();
  assert(!/\b(purchase_requests|approved)\b/.test(body.replace(/"[^"]*"/g, "")), "도구 화면에 영문 자원·상태 코드가 보임");
  await shot(page, "91-mcp-tool");
  await narrowCheck(page, "91b-mcp-tool-narrow", async () => {
    await page.getByRole("heading", { name: "스모크 구매요청" }).waitFor();
  });
  await page.goto(`${BASE}/admin/mcp-tools`);
  const row = page.getByRole("table", { name: "AI 도구 목록" }).locator("tr", { hasText: "스모크 구매요청" });
  await row.getByText("켜짐", { exact: true }).waitFor();
  await row.getByText("구매요청", { exact: true }).waitFor();
  await shot(page, "91c-mcp-tools");
  await page.getByRole("button", { name: "내보내기" }).waitFor();
  await page.locator('input[type="file"]').setInputFiles({
    name: "tools.json",
    mimeType: "application/json",
    buffer: Buffer.from(JSON.stringify({ format: "pl-mcp-tools/1", tools: [{ format: "pl-mcp-tool/1", name: "custom_smoke_imp", kind: "read", description: "가져온 도구이다.", resource: "purchase_requests", fields: ["no", "title"] }] })),
  });
  await page.getByRole("table", { name: "가져오기 결과" }).getByText("통과").waitFor();
  await page.getByText(/1개를 꺼진 도구로 만들었고 0개는 거부했습니다/).waitFor();
  await page.goto(`${BASE}/admin/mcp-tools/new`);
  const nf = page.getByRole("form", { name: "도구 양식" });
  await nf.getByLabel("도구 종류", { exact: true }).selectOption({ label: "초안 만들기(쓰기, 임시저장만)" });
  await nf.getByLabel("초안 종류", { exact: true }).selectOption({ label: "구매요청 초안(품목 한 줄)" });
  await nf.getByText(/결재선·승인자·상태·법인은 칸이 없어/).waitFor();
  await shot(page, "91d-mcp-tool-draft");
  await page.goto(`${BASE}/admin/mcp-tools`);
  const aud = await loginDirect("auditor");
  assert((await menuLabels(aud)).includes("AI 도구 만들기"), "감사 메뉴에 AI 도구 만들기가 없음");
  await aud.goto(`${BASE}/admin/mcp-tools`);
  await aud.getByRole("table", { name: "AI 도구 목록" }).waitFor();
  assert((await aud.getByRole("link", { name: "새 도구" }).count()) === 0, "감사에게 새 도구 단추가 보임");
  await aud.getByRole("link", { name: "스모크 구매요청" }).click();
  await aud.getByRole("heading", { name: "스모크 구매요청" }).waitFor();
  assert((await aud.getByRole("button", { name: "새 버전으로 저장" }).count()) === 0 && (await aud.getByRole("button", { name: "시험 실행" }).count()) === 0, "감사에게 저장·시험 단추가 보임");
  const fin = await loginDirect("finance");
  assert(!(await menuLabels(fin)).includes("AI 도구 만들기"), "재무 메뉴에 AI 도구 만들기가 보임");
  await fin.goto(`${BASE}/admin/mcp-tools`);
  await fin.getByRole("alert").waitFor();
});
await step("전자세금계산서(B43 admin·finance·staff): 대행사 설정(모의)을 저장·켜기·시험 연결하고, 매입 수기 등록·중복 거부·목록·상세가 한국어로만 나오며, 폭 390 에서 넘치지 않는다", async () => {
  // 매입 등록은 회사 사업자번호가 있어야 한다(없으면 시험용으로 채운다).
  const adm = await loginDirect("admin");
  const co = await api("admin", "GET", "/api/company");
  if (!co.businessNo) await api("admin", "PUT", "/api/company", { name: co.name || "에이엑스 주식회사", businessNo: "123-45-67890", representative: co.representative || "홍길동", address: co.address || "서울", phone: co.phone || "", version: co.version ?? 0 });
  assert((await menuLabels(adm)).includes("전자세금계산서 대행사"), "관리자 메뉴에 전자세금계산서 대행사가 없음");
  await adm.goto(`${BASE}/admin/tax-asp`);
  await adm.getByRole("heading", { name: "전자세금계산서 대행사" }).waitFor();
  await adm.locator("#asp-provider").selectOption({ label: "모의 대행사(시험용)" });
  await adm.getByRole("button", { name: "저장", exact: true }).click();
  await adm.getByText(/대행사 설정을 저장했습니다/).waitFor();
  await adm.getByRole("button", { name: "사용 켜기" }).click();
  await adm.getByText(/대행사 사용을 켰습니다/).waitFor();
  await adm.getByRole("button", { name: "시험 연결" }).click();
  await adm.getByText(/시험 연결에 성공했습니다/).waitFor();
  const asp = await adm.locator("main").innerText();
  assert(!/\bmock\b/.test(asp), "대행사 설정 화면에 내부 코드 mock 이 보임");
  await shot(adm, "81-admin-tax-asp");
  await narrowCheck(adm, "81b-admin-tax-asp-narrow", async () => {
    await adm.getByRole("heading", { name: "전자세금계산서 대행사" }).waitFor();
  });
  const fin = await loginDirect("finance");
  assert(!(await menuLabels(fin)).includes("전자세금계산서 대행사"), "재무 메뉴에 대행사 설정이 보임");
  await fin.goto(`${BASE}/admin/tax-asp`);
  await fin.getByRole("alert").waitFor();
  await fin.goto(`${BASE}/finance/tax-invoices/receive`);
  await fin.getByRole("heading", { name: "매입 세금계산서 수기 등록" }).waitFor();
  const approval = `${new Date().toISOString().slice(0, 10).replace(/-/g, "")}SMOKE${String(Date.now()).slice(-11)}`.slice(0, 24);
  const fillReceive = async () => {
    await fin.getByLabel("승인번호(24자리)").fill(approval);
    await fin.locator("#tr-supplier-biz").fill("220-81-62517");
    await fin.locator("#tr-supplier-name").fill("스모크 공급자");
    await fin.locator("#tr-supplier-rep").fill("김공급");
    await fin.getByLabel("공급가액").fill("100000");
    await fin.getByLabel("세액").fill("10000");
  };
  await fillReceive();
  await fin.getByRole("button", { name: "등록", exact: true }).click();
  await fin.waitForURL(/\/finance\/tax-invoices\/\d+$/);
  await fin.getByText("국세청 수신").first().waitFor();
  await shot(fin, "82-tax-invoice-detail");
  const detail = await fin.locator("main").innerText();
  assert(!/\b(accepted|issued|failed|canceled|draft|purchase|manual)\b/.test(detail), "상세에 내부 코드가 보임");
  await narrowCheck(fin, "82b-tax-invoice-detail-narrow", async () => {
    await fin.getByText("국세청 수신").first().waitFor();
  });
  // 같은 승인번호는 한국어 사유로 막힌다.
  await fin.goto(`${BASE}/finance/tax-invoices/receive`);
  await fin.getByRole("heading", { name: "매입 세금계산서 수기 등록" }).waitFor();
  await fillReceive();
  await fin.getByRole("button", { name: "등록", exact: true }).click();
  await fin.getByText(/이미 등록된 승인번호/).waitFor();
  await fin.goto(`${BASE}/finance/tax-invoices`);
  await fin.getByRole("heading", { name: "세금계산서" }).waitFor();
  await fin.getByText("스모크 공급자").first().waitFor();
  await shot(fin, "83-tax-invoices");
  assert(!/\b(accepted|issued|failed|canceled|draft|purchase|sale)\b/.test(await fin.locator("main").innerText()), "목록에 내부 코드가 보임");
  await narrowCheck(fin, "83b-tax-invoices-narrow", async () => {
    await fin.getByText("스모크 공급자").first().waitFor();
  });
  await fin.goto(`${BASE}/finance/tax-invoices/new`);
  await fin.getByRole("heading", { name: "매출 세금계산서 작성" }).waitFor();
  await narrowCheck(fin, "83c-tax-invoice-new-narrow", async () => {
    await fin.getByRole("heading", { name: "매출 세금계산서 작성" }).waitFor();
  });
  const staff = await loginDirect("staff");
  await staff.goto(`${BASE}/finance/tax-invoices`);
  await staff.getByRole("alert").waitFor();
});
await step("백업 상태(admin·auditor): 읽기 전용 화면이고 실행 단추가 없으며, 직원·재무에게는 메뉴가 없다", async () => {
  const page = await loginDirect("admin");
  await page.goto(`${BASE}/admin/backup`);
  await page.getByRole("heading", { name: "백업 상태" }).waitFor();
  assert((await page.locator("main button").count()) === 0, "백업 상태 화면에 단추가 있음");
  await shot(page, "71-admin-backup");
  const aud = await loginDirect("auditor");
  const menu = await menuLabels(aud);
  assert(menu.includes("백업 상태") && !menu.includes("도입 점검표"), `감사 메뉴: ${menu.join(",")}`);
  await aud.goto(`${BASE}/admin/backup`);
  await aud.getByRole("heading", { name: "백업 상태" }).waitFor();
  await aud.goto(`${BASE}/admin/onboarding`);
  await aud.getByRole("alert").waitFor();
  for (const who of ["staff", "finance"]) {
    const p = await loginDirect(who);
    const m = await menuLabels(p);
    assert(!m.includes("백업 상태") && !m.includes("도입 점검표"), `${who} 메뉴에 점검표·백업 상태가 보임`);
    await p.goto(`${BASE}/admin/backup`);
    await p.getByRole("alert").waitFor();
  }
});

await step("기초 데이터 가져오기(finance): 허브에서 양식을 내려받아 올리고 미리보기 → 반영까지 돌고, 반영한 계정과목·창고가 화면과 이력에 보인다(연결 시험)", async () => {
  const page = await loginDirect("finance");
  const targets = [
    { code: "account", name: "계정과목", seen: async () => { await page.goto(`${BASE}/finance/accounts`); await page.getByText("광고선전비").first().waitFor(); } },
    { code: "warehouse", name: "창고", seen: async () => { await page.goto(`${BASE}/inventory/transfers`); await page.locator("option", { hasText: "물류센터" }).first().waitFor({ state: "attached" }); } },
  ];
  for (const t of targets) {
    const tpl = await page.context().request.get(`${BASE}/api/imports/templates/${t.code}`);
    assert(tpl.ok(), `${t.name} 양식을 내려받지 못함: ${tpl.status()}`);
    await page.goto(`${BASE}/admin/import/${t.code}`);
    await page.getByRole("heading", { name: new RegExp(t.name) }).first().waitFor();
    await page.getByLabel("가져올 파일").setInputFiles({ name: `${t.code}.xlsx`, mimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", buffer: await tpl.body() });
    await page.getByRole("button", { name: "미리보기" }).click();
    await page.getByRole("button", { name: "반영하기" }).waitFor();
    assert(await page.getByRole("button", { name: "반영하기" }).isEnabled(), `${t.name} 미리보기가 반영할 수 있는 상태가 아님`);
    if (t.code === "account") await shot(page, "104-import-preview");
    await page.getByRole("button", { name: "반영하기" }).click();
    await page.locator('[role="dialog"], [role="alertdialog"]').getByRole("button", { name: "반영", exact: true }).click();
    await page.getByRole("heading", { name: "4. 반영 결과" }).waitFor();
    if (t.code === "account") await shot(page, "105-import-applied");
    await t.seen();
  }
  await page.goto(`${BASE}/admin/import/history`);
  await page.getByRole("heading", { name: "가져오기 이력" }).waitFor();
  await page.locator("tbody tr").first().waitFor();
  assert((await page.locator("tbody tr").count()) >= 2, "이력에 반영한 묶음 2건이 보이지 않음");
});

await step("좁은 화면: 홈이 가로로 넘치지 않고 메뉴(서랍)가 접혀 있다(staff)", async () => {
  const page = await loginDirect("staff");
  await narrowCheck(page, "30-narrow-home", async () => {
    await page.getByRole("heading", { name: "홈" }).waitFor();
    assert(!(await page.locator("#snb").isVisible()), "좁은 화면에서 메뉴 서랍이 접혀 있지 않음");
    assert(await page.getByRole("button", { name: "메뉴 열기" }).isVisible(), "좁은 화면에 햄버거가 없음");
  });
});
await step("좁은 화면: 메뉴를 펼치면 첫 항목에 포커스가 가고, Esc 로 닫히며, 모듈을 골라 항목을 누르면 이동하고 닫힌다(staff)", async () => {
  const page = await loginDirect("staff");
  await page.setViewportSize({ width: 390, height: 844 });
  try {
    await page.getByRole("heading", { name: "홈" }).waitFor();
    const drawer = page.locator("#snb");
    const toggle = page.getByRole("button", { name: "메뉴 열기" });
    await toggle.click();
    await drawer.waitFor({ state: "visible" });
    const focused = () => page.evaluate(() => document.activeElement?.textContent?.trim() ?? "");
    assert((await focused()) === "홈", `메뉴를 펼쳤는데 포커스가 첫 항목이 아님: ${await focused()}`);
    await shot(page, "31-narrow-home-menu-open");
    await page.keyboard.press("Escape");
    await drawer.waitFor({ state: "hidden" });
    assert(((await page.evaluate(() => document.activeElement?.getAttribute("aria-label"))) ?? "") === "메뉴 열기", "Esc 뒤 포커스가 햄버거로 돌아오지 않음");
    await toggle.click();
    await drawer.getByRole("button", { name: "구매", exact: true }).click(); // 모듈을 고르면 아래 하위 메뉴만 바뀐다(이동하지 않음)
    await drawer.getByRole("link", { name: "구매요청" }).click();
    await page.waitForURL(`${BASE}/purchase-requests`);
    assert(!(await drawer.isVisible()), "메뉴 이동 뒤에 서랍이 닫히지 않음");
  } finally {
    await page.setViewportSize({ width: 1280, height: 900 });
  }
});
await step("좁은 화면: 구매요청 목록이 가로로 넘치지 않는다(staff)", async () => {
  const page = await loginDirect("staff");
  await narrowCheck(page, "32-narrow-purchase-list", async () => {
    await page.goto(`${BASE}/purchase-requests`);
    await page.getByRole("heading", { name: "구매요청" }).first().waitFor();
  });
});
await step("좁은 화면: 구매 실적 보고서가 가로로 넘치지 않는다(finance)", async () => {
  const page = await loginDirect("finance");
  await narrowCheck(page, "34-narrow-report-purchase", async () => {
    await page.goto(`${BASE}/reports/purchase`);
    await page.getByRole("heading", { name: "보고서" }).waitFor();
  });
});
await step("좁은 화면: 감사 로그가 가로로 넘치지 않는다(auditor)", async () => {
  const page = await loginDirect("auditor");
  await narrowCheck(page, "33-narrow-audit-log", async () => {
    await page.goto(`${BASE}/audit-logs`);
    await page.locator("tbody tr").first().waitFor();
  });
});

await step("좁은 화면: 작성 화면(결재선 미리보기 포함)이 가로로 넘치지 않는다(staff)", async () => {
  const page = await loginDirect("staff");
  await narrowCheck(page, "47-narrow-purchase-new", async () => {
    await page.goto(`${BASE}/purchase-requests/new`);
    await page.getByRole("heading", { name: "새 구매요청" }).waitFor();
    await page.getByLabel("거래처").selectOption({ label: "한빛상사" });
    await page.getByLabel("1번째 줄 품목").selectOption({ label: "볼트 M8 (BOLT-M8)" });
    await page.getByLabel("1번째 줄 수량").fill("1");
    await page.getByLabel("1번째 줄 단가").fill("500000");
    await page.getByRole("complementary", { name: "결재선 미리보기" }).getByRole("row").nth(1).waitFor();
  });
}, { needs: reassigned });
await step("좁은 화면: 결재 화면이 가로로 넘치지 않는다(head)", async () => {
  const page = await loginDirect("head");
  await narrowCheck(page, "48-narrow-approval", async () => {
    await page.goto(`${BASE}/approvals/${reassignId}`);
    await page.getByRole("button", { name: "패널 열기" }).click(); // 좁은 화면에서는 결재 정보 패널이 접혀 있다
    await page.getByText(/상신 당시 조직/).waitFor();
  });
}, { needs: reassigned });
await step("좁은 화면: 알림 화면이 가로로 넘치지 않는다(staff)", async () => {
  const page = await loginDirect("staff");
  await narrowCheck(page, "49-narrow-notifications", async () => {
    await page.goto(`${BASE}/notifications`);
    await page.getByRole("heading", { name: "알림" }).first().waitFor();
  });
}, { needs: reassigned });
await step("좁은 화면: 결재자 변경 대화상자가 가로로 넘치지 않는다(admin)", async () => {
  const page = await loginDirect("admin");
  await narrowCheck(page, "50-narrow-reassign-dialog", async () => {
    await page.goto(`${BASE}/purchase-requests/${reassignId}`);
    await page.getByRole("button", { name: "패널 열기" }).click();
    await page.getByRole("button", { name: "결재자 변경" }).click();
    await page.locator('[role="dialog"], [role="alertdialog"]').waitFor();
  });
}, { needs: reassigned });

await step("좁은 화면: 결재 문서 작성 화면(결재선 편집 포함)이 가로로 넘치지 않는다(staff)", async () => {
  const page = await loginDirect("staff");
  await narrowCheck(page, "55-narrow-doc-form", async () => {
    await page.goto(`${BASE}/docs/new/GEN`);
    await page.getByRole("heading", { name: "일반 기안서 작성" }).waitFor();
    await page.getByRole("complementary", { name: "결재선" }).getByRole("row").nth(1).waitFor();
  });
}, { needs: drafted });
await step("좁은 화면: 결재 문서 상세가 가로로 넘치지 않는다(finance)", async () => {
  const page = await loginDirect("finance");
  await narrowCheck(page, "56-narrow-doc-view", async () => {
    await page.goto(`${BASE}/docs/${docId}`);
    await page.getByRole("heading", { name: /기안 시험/ }).waitFor();
  });
}, { needs: drafted });

await step("좁은 화면: 출력 화면이 가로로 넘치지 않는다(finance)", async () => {
  const page = await loginDirect("finance");
  await narrowCheck(page, "61-narrow-doc-print", async () => {
    await page.goto(`${BASE}/docs/${docId}/print`);
    await page.getByRole("table", { name: "결재란" }).first().waitFor();
  });
}, { needs: drafted });

await step("좁은 화면: 조직도가 가로로 넘치지 않는다(staff)", async () => {
  const page = await loginDirect("staff");
  await narrowCheck(page, "36-narrow-org", async () => {
    await page.goto(`${BASE}/org`);
    await page.getByRole("heading", { name: "조직도" }).waitFor();
    await page.getByRole("tree", { name: "조직 트리" }).waitFor();
  });
});
await step("좁은 화면: 인사 발령 목록은 행을 쌓아 보이고 표 안쪽 가로 스크롤이 없다(admin)", async () => {
  const page = await loginDirect("admin");
  await narrowCheck(page, "37-narrow-appointments", async () => {
    await page.goto(`${BASE}/org/appointments`);
    await page.getByRole("heading", { name: "인사 발령" }).waitFor();
    await page.locator(".stack-table tbody tr").first().waitFor();
    const inner = await page.locator(".stack-table").first().evaluate((t) => t.parentElement.scrollWidth - t.parentElement.clientWidth);
    assert(inner <= 1, `발령 목록 표가 안쪽에서 가로로 잘림(${inner}px)`);
  });
});
await step("좁은 화면: 거래처 평가 표는 행을 쌓아 보이고 표 안쪽 가로 스크롤이 없다(finance)", async () => {
  const page = await loginDirect("finance");
  await narrowCheck(page, "79-narrow-vendor-rating", async () => {
    await page.goto(`${BASE}/vendors/rating`);
    await page.getByRole("heading", { name: "거래처 평가" }).waitFor();
    await page.getByLabel("시작일").fill("2020-01-01");
    await page.getByRole("button", { name: "조회" }).click();
    await page.locator(".stack-table tbody tr").first().waitFor();
    const inner = await page.locator(".stack-table").first().evaluate((t) => t.parentElement.scrollWidth - t.parentElement.clientWidth);
    assert(inner <= 1, `평가 표가 안쪽에서 가로로 잘림(${inner}px)`);
  });
}, { needs: received });
await step("다법인(B44 admin·finance): 법인이 둘이어도 고를 수 있는 법인이 하나면 법인 선택기가 없고, 사업장 탭에서 지점을 등록하며, 화면에 내부 코드가 없고, 폭 390 에서 넘치지 않는다", async () => {
  const code = `SM${String(Date.now() % 10000).padStart(4, "0")}`;
  await api("admin", "POST", "/api/admin/entities", { code, name: "스모크 사업장 법인", businessNo: "", representative: "", address: "", phone: "", fiscalStartMonth: 1, rootUnitId: 0 });
  const page = await loginDirect("admin");
  assert((await page.locator("#gnb-entity-select").count()) === 0, "고를 수 있는 법인이 하나뿐인데 법인 선택기가 보임");
  await page.goto(`${BASE}/admin/entities`);
  await page.getByRole("heading", { name: "법인 관리" }).waitFor();
  await page.getByRole("tab", { name: "사업장" }).click();
  await page.getByRole("table", { name: "사업장 목록" }).waitFor();
  await page.locator("#site-entity").selectOption({ label: "스모크 사업장 법인" });
  const sites = page.getByRole("table", { name: "사업장 목록" });
  await sites.getByText("주사업장").first().waitFor(); // 법인을 만들면 주사업장이 자동으로 생긴다
  const form = page.getByRole("form", { name: "사업장 양식" });
  await form.getByLabel("종사업장번호").fill("0007");
  await form.getByLabel("사업장 이름").fill("부산 지점");
  await form.getByRole("button", { name: "등록", exact: true }).click();
  await page.getByText("사업장을 등록했습니다.").waitFor();
  await sites.getByRole("row").filter({ hasText: "부산 지점" }).waitFor();
  const body = await page.locator("main").innerText();
  assert(!/\b(head|branch|factory|own|consolidated)\b/.test(body), "사업장 탭에 내부 코드가 보임");
  await shot(page, "85-admin-entity-sites");
  await narrowCheck(page, "85b-admin-entity-sites-narrow", async () => {
    await page.getByRole("tab", { name: "사업장" }).waitFor();
  });
  const fin = await loginDirect("finance");
  await fin.goto(`${BASE}/inventory`);
  assert((await fin.getByRole("heading", { name: "거래처의 연결 법인" }).count()) === 0, "법인이 하나로 보이는 재무에게 거래처 연결 법인 패널이 보임");
});
await step("연결 재무 집계(B44 3단계 finance): 법인별 시산표·재무 요약·내부 거래 참고가 보이고 영문 분류 코드가 없으며 폭 390 에서 넘치지 않는다", async () => {
  const page = await loginDirect("finance");
  await page.goto(`${BASE}/finance/consolidated`);
  await page.getByRole("heading", { name: "연결 재무 집계" }).waitFor();
  await page.getByRole("heading", { name: "법인별 시산표" }).waitFor();
  await page.getByRole("heading", { name: "재무 요약" }).waitFor();
  await page.getByRole("heading", { name: "내부 거래 참고" }).waitFor();
  await page.getByText("내부 거래가 합계에 포함되어 있습니다").waitFor();
  const body = await page.locator("main").innerText();
  assert(!/\b(asset|liability|equity|revenue|expense|consolidated)\b/.test(body), "연결 재무 집계에 내부 코드가 보임");
  await shot(page, "86-finance-consolidated");
  await narrowCheck(page, "86b-finance-consolidated-narrow", async () => {
    await page.getByRole("heading", { name: "연결 재무 집계" }).waitFor();
  });
});
await step("사내 지식 검색(B47 staff·admin): 완료된 구매요청 본문을 내용 검색이 출처와 함께 찾고, 관리 화면에서 다시 색인하며, 영문 코드가 없고 폭 390 에서 넘치지 않는다", async () => {
  const word = `지식검색${String(Date.now() % 100000)}`;
  const vendors = await api("staff", "GET", "/api/vendors");
  const items = await api("staff", "GET", "/api/items?limit=5");
  const created = await api("staff", "POST", "/api/purchase-requests", { vendorId: vendors[0].id, title: `${word} 점검 장비`, reason: "스모크 지식 검색", lines: [{ itemId: items[0].id, quantity: 1, unitPrice: 100000 }] });
  await api("staff", "POST", `/api/purchase-requests/${created.id}/submit`);
  await api("lead", "POST", `/api/approvals/${created.id}/decision`, { approve: true, comment: "" });
  const after = await api("staff", "GET", `/api/purchase-requests/${created.id}`);
  if (after.status === "in_review" || after.status === "submitted") await api("head", "POST", `/api/approvals/${created.id}/decision`, { approve: true, comment: "" });
  const admin = await loginDirect("admin");
  await admin.goto(`${BASE}/admin/knowledge`);
  await admin.getByRole("heading", { name: "지식 색인" }).waitFor();
  await admin.getByRole("table", { name: "색인 현황" }).waitFor();
  await admin.getByRole("button", { name: "다시 색인" }).click();
  await admin.getByRole("dialog").getByRole("button", { name: "다시 색인" }).click();
  await admin.getByText(/다시 색인했습니다\. 처리 \d+건/).waitFor();
  const adminBody = await admin.locator("main").innerText();
  assert(!/\b(approval_doc|attachment|no_text|unreadable|unsupported)\b/.test(adminBody), "지식 색인 화면에 내부 코드가 보임");
  await shot(admin, "87-admin-knowledge");
  const staff = await loginDirect("staff");
  await staff.goto(`${BASE}/search?q=${encodeURIComponent(word)}`);
  const section = staff.getByRole("region", { name: "내용 검색" });
  await section.getByText(new RegExp(`결재 문서 .* 「${word} 점검 장비」 · 본문`)).waitFor();
  await section.getByRole("link", { name: "문서 열기" }).first().waitFor();
  const text = await section.innerText();
  assert(!/\b(approval_doc|attachment|regulation)\b/.test(text), "내용 검색에 내부 종류 코드가 보임");
  await shot(staff, "87b-search-knowledge");
  await narrowCheck(staff, "87c-search-knowledge-narrow", async () => {
    await staff.getByRole("region", { name: "내용 검색" }).waitFor();
  });
  await staff.goto(`${BASE}/search?q=${encodeURIComponent("전혀없는검색어문장")}`);
  await staff.getByRole("region", { name: "내용 검색" }).getByText("일치하는 내용을 찾지 못했습니다.").waitFor();
});
await step("사내 지식 업로드(B47 2단계 admin·staff): 규정 MD 를 올리면 조항 출처로 검색되고 원문 보기가 열리며, 스캔·형식 오류는 한국어로 거부되고, 폭 390 에서 넘치지 않는다", async () => {
  const word = `업로드규정${String(Date.now() % 100000)}`;
  const admin = await loginDirect("admin");
  await admin.goto(`${BASE}/admin/knowledge`);
  await admin.getByRole("heading", { name: "업로드 매뉴얼·규정" }).waitFor();
  const form = admin.getByRole("form", { name: "업로드 양식" });
  await form.getByLabel("제목").fill(`${word} 구매 기준`);
  await form.locator("#ku-file").setInputFiles({ name: "기준.hwp", mimeType: "application/octet-stream", buffer: Buffer.from("x") });
  await form.getByRole("button", { name: "올리기", exact: true }).click();
  await admin.getByText("PDF·DOCX·MD 파일만 올릴 수 있습니다.").waitFor();
  const md = `## 제1조 (대상)\n${word} 구매는 팀장 승인 후 진행한다.\n\n## 제2조 (예외)\n긴급 건은 사후 보고한다.\n`;
  await form.locator("#ku-file").setInputFiles({ name: "기준.md", mimeType: "text/markdown", buffer: Buffer.from(md, "utf8") });
  await form.getByRole("button", { name: "올리기", exact: true }).click();
  await admin.getByText("올렸습니다. 색인이 끝나는 몇 초 뒤부터 검색에서 찾을 수 있습니다.").waitFor();
  const table = admin.getByRole("table", { name: "업로드 매뉴얼·규정" });
  await table.getByText(`${word} 구매 기준`).waitFor();
  const adminBody = await admin.locator("main").innerText();
  assert(!/\b(regulation|manual|no_text|ready|pending)\b/.test(adminBody), "업로드 관리 화면에 내부 코드가 보임");
  await shot(admin, "88-admin-knowledge-uploads");
  await admin.getByRole("button", { name: "다시 색인" }).click();
  await admin.getByRole("alertdialog").or(admin.getByRole("dialog")).getByRole("button", { name: "다시 색인" }).click();
  await admin.getByText(/다시 색인했습니다\. 처리 \d+건/).waitFor();
  const staff = await loginDirect("staff");
  await staff.goto(`${BASE}/search?q=${encodeURIComponent(word)}`);
  const section = staff.getByRole("region", { name: "내용 검색" });
  await section.getByText(new RegExp(`사내 규정 「${word} 구매 기준」 제1조 \\(대상\\)`)).waitFor();
  await shot(staff, "88b-search-upload");
  await section.getByRole("link", { name: "문서 열기" }).first().click();
  await staff.getByRole("heading", { name: `${word} 구매 기준` }).waitFor();
  await staff.getByRole("heading", { name: "제1조 (대상)" }).waitFor();
  await staff.getByRole("button", { name: "원본 내려받기" }).waitFor();
  await shot(staff, "88c-knowledge-doc");
  await narrowCheck(staff, "88d-knowledge-doc-narrow", async () => {
    await staff.getByRole("heading", { name: `${word} 구매 기준` }).waitFor();
  });
});
await step("사내 지식 질문하기(B47 3단계 admin·staff): 근거 있는 질문은 답과 인용 번호·출처를 보이고, 근거 없는 질문은 찾지 못함 문구만 보이며, 폭 390 에서 넘치지 않는다", async () => {
  const word = `질문규정${String(Date.now() % 100000)}`;
  const admin = await loginDirect("admin");
  await admin.goto(`${BASE}/admin/knowledge`);
  await admin.getByRole("heading", { name: "업로드 매뉴얼·규정" }).waitFor();
  const form = admin.getByRole("form", { name: "업로드 양식" });
  await form.getByLabel("제목").fill(`${word} 휴가 기준`);
  const md = `## 제1조 (대상)\n${word} 휴가는 사흘 전에 신청한다.\n`;
  await form.locator("#ku-file").setInputFiles({ name: "휴가.md", mimeType: "text/markdown", buffer: Buffer.from(md, "utf8") });
  await form.getByRole("button", { name: "올리기", exact: true }).click();
  await admin.getByText("올렸습니다. 색인이 끝나는 몇 초 뒤부터 검색에서 찾을 수 있습니다.").waitFor();
  await admin.getByRole("button", { name: "다시 색인" }).click();
  await admin.getByRole("alertdialog").or(admin.getByRole("dialog")).getByRole("button", { name: "다시 색인" }).click();
  await admin.getByText(/다시 색인했습니다\. 처리 \d+건/).waitFor();
  const staff = await loginDirect("staff");
  await staff.goto(`${BASE}/search?q=${encodeURIComponent(`${word} 휴가 신청`)}`);
  const section = staff.getByRole("region", { name: "내용 검색" });
  await section.getByRole("button", { name: "질문하기" }).click();
  const card = staff.getByRole("region", { name: "답변" });
  await card.getByText(new RegExp(`${word} 휴가는 사흘 전에 신청한다.*\\[1\\]`)).waitFor();
  await card.getByRole("link", { name: new RegExp(`${word} 휴가 기준.*제1조`) }).waitFor();
  await card.getByText("근거 없는 내용은 답에서 뺐습니다.").waitFor();
  assert(!/\b(found|sources|knowledge\.answer)\b/.test(await card.innerText()), "답변 카드에 내부 이름이 보임");
  await shot(staff, "109-knowledge-answer");
  await narrowCheck(staff, "109b-knowledge-answer-narrow", async () => {
    await staff.getByRole("region", { name: "답변" }).waitFor();
  });
  await staff.goto(`${BASE}/search?q=${encodeURIComponent("전혀없는검색어문장")}`);
  await staff.getByRole("region", { name: "내용 검색" }).getByRole("button", { name: "질문하기" }).click();
  const none = staff.getByRole("region", { name: "답변" });
  await none.getByText("사내 문서에서 근거가 되는 내용을 찾지 못했습니다.").waitFor();
  assert((await none.getByRole("link").count()) === 0, "찾지 못했는데 출처 링크가 있음");
  await shot(staff, "109c-knowledge-answer-notfound");
  await narrowCheck(staff, "109d-knowledge-answer-notfound-narrow", async () => {
    await staff.getByRole("region", { name: "답변" }).waitFor();
  });
});
await step("부서별 AI 비용(B49 1단계 finance·lead·auditor·staff): 재무는 환율을 저장하고 집계를 보며, 팀장은 우리 부서 화면, 감사는 조회만, 직원은 메뉴가 없고, 폭 390 에서 넘치지 않는다", async () => {
  const fin = await loginDirect("finance");
  assert((await menuLabels(fin)).includes("AI 비용"), "재무 메뉴에 AI 비용이 없음");
  await fin.goto(`${BASE}/admin/ai-cost`);
  await fin.getByRole("heading", { name: "AI 비용", exact: true }).waitFor();
  await fin.getByRole("form", { name: "환율 입력" }).waitFor();
  await fin.getByLabel(/1달러당 원/).fill("1350.5");
  await fin.getByRole("button", { name: "환율 저장" }).click();
  await fin.getByText("환율을 저장했습니다.").waitFor();
  await fin.getByText(/이 달 환율: 1달러 = 1,350.5원/).waitFor();
  const text = await fin.locator("main").innerText();
  assert(!/docverify\.|knowledge\.answer|eval\.run|mcp\.select_tool/.test(text), "AI 비용 화면에 내부 용도 코드가 보임");
  await shot(fin, "110-ai-cost-admin");
  await narrowCheck(fin, "110b-ai-cost-admin-narrow", async () => {
    await fin.getByRole("heading", { name: "AI 비용", exact: true }).waitFor();
  });
  // 2단계: 기본 규칙 만들기 → 배부 미리보기(재무), 감사는 조회만.
  await fin.goto(`${BASE}/admin/ai-cost/rules`);
  await fin.getByRole("heading", { name: "AI 비용 배부 규칙" }).waitFor();
  const mk = fin.getByRole("button", { name: "기본 규칙 만들기" });
  await mk.or(fin.getByRole("table", { name: "규칙 버전" })).first().waitFor();
  if (await mk.count()) {
    await mk.click();
    await fin.getByText("기본 규칙을 만들었습니다.").waitFor();
  }
  await fin.getByRole("table", { name: "규칙 버전" }).waitFor();
  await fin.getByRole("button", { name: /버전 1 을 바탕으로 편집/ }).click();
  await fin.getByLabel("풀 평가·점검").waitFor();
  await fin.getByRole("heading", { name: "비용 전표의 비용 계정" }).waitFor();
  await fin.getByRole("form", { name: "비용 계정 설정" }).waitFor();
  const rulesText = await fin.locator("main").innerText();
  assert(!/headcount|usage|fixed|eval\.run/.test(rulesText), "규칙 화면에 내부 코드가 보임");
  await shot(fin, "110e-ai-cost-rules");
  await narrowCheck(fin, "110f-ai-cost-rules-narrow", async () => {
    await fin.getByRole("heading", { name: "AI 비용 배부 규칙" }).waitFor();
  });
  await fin.goto(`${BASE}/admin/ai-cost/preview`);
  await fin.getByRole("heading", { name: "AI 비용 배부 미리보기" }).waitFor();
  await fin.getByText(/규칙 버전 1|이 달에는 AI 사용 기록이 없습니다|환율이 필요합니다/).first().waitFor();
  const prevText = await fin.locator("main").innerText();
  assert(!/headcount|usage|fixed|eval\.run/.test(prevText), "미리보기 화면에 내부 코드가 보임");
  await fin.getByRole("heading", { name: "월 확정과 비용 전표" }).waitFor();
  await fin.getByRole("button", { name: "CSV 내려받기" }).waitFor();
  assert((await fin.getByRole("button", { name: "이 달 배부 확정" }).count()) + (await fin.getByText(/확정은 재무 담당이 합니다/).count()) >= 1, "확정 단추가 보이지 않음");
  await shot(fin, "110g-ai-cost-preview");
  await narrowCheck(fin, "110h-ai-cost-preview-narrow", async () => {
    await fin.getByRole("heading", { name: "AI 비용 배부 미리보기" }).waitFor();
  });
  // 3단계: 예산 화면 — 재무가 부서 예산을 정하면 목록과 우리 부서 화면에 반영된다.
  await fin.goto(`${BASE}/admin/ai-cost/budgets`);
  await fin.getByRole("heading", { name: "AI 비용 예산" }).waitFor();
  const budgets = fin.getByRole("table", { name: "부서별 AI 비용 예산" });
  await budgets.waitFor();
  const firstBudgetInput = budgets.getByRole("textbox").first();
  await firstBudgetInput.fill("500000");
  await budgets.getByRole("button", { name: "저장" }).first().click();
  await fin.getByText(/예산을 저장했습니다/).waitFor();
  await budgets.getByText("500,000원").first().waitFor();
  const budgetText = await fin.locator("main").innerText();
  assert(!/\bfx\b|entity|amountKrw/.test(budgetText), "예산 화면에 내부 코드가 보임");
  await shot(fin, "110i-ai-cost-budgets");
  await narrowCheck(fin, "110j-ai-cost-budgets-narrow", async () => {
    await fin.getByRole("heading", { name: "AI 비용 예산" }).waitFor();
  });
  const lead = await loginDirect("lead");
  assert((await menuLabels(lead)).includes("우리 부서 AI 비용"), "팀장 메뉴에 우리 부서 AI 비용이 없음");
  assert(!(await menuLabels(lead)).includes("AI 비용"), "팀장 메뉴에 전사 AI 비용이 보임");
  await lead.goto(`${BASE}/ai-cost/my`);
  await lead.getByRole("heading", { name: "우리 부서 AI 비용" }).waitFor();
  await lead.getByRole("table", { name: "월별 추이" }).waitFor();
  await lead.getByText(/예산 대비|AI 비용 예산이 없습니다/).first().waitFor();
  assert((await lead.getByRole("table", { name: "월별 추이" }).getByRole("row").count()) === 13, "월별 추이가 12개월이 아님");
  await shot(lead, "110c-ai-cost-my");
  await narrowCheck(lead, "110d-ai-cost-my-narrow", async () => {
    await lead.getByRole("table", { name: "월별 추이" }).waitFor();
  });
  await lead.goto(`${BASE}/admin/ai-cost`);
  await lead.getByRole("alert").waitFor();
  const aud = await loginDirect("auditor");
  assert((await menuLabels(aud)).includes("AI 비용"), "감사 메뉴에 AI 비용이 없음");
  await aud.goto(`${BASE}/admin/ai-cost`);
  await aud.getByRole("heading", { name: "AI 비용", exact: true }).waitFor();
  assert((await aud.getByRole("form", { name: "환율 입력" }).count()) === 0, "감사에게 환율 입력 양식이 보임");
  await aud.goto(`${BASE}/admin/ai-cost/rules`);
  await aud.getByRole("heading", { name: "AI 비용 배부 규칙" }).waitFor();
  assert((await aud.getByRole("button", { name: "기본 규칙 만들기" }).count()) === 0 && (await aud.getByRole("button", { name: "새 버전으로 저장" }).count()) === 0, "감사에게 규칙 저장 단추가 보임");
  const staff = await loginDirect("staff");
  const sl = await menuLabels(staff);
  assert(!sl.includes("AI 비용") && !sl.includes("우리 부서 AI 비용"), "직원 메뉴에 AI 비용이 보임");
});
await step("업무 모듈(B50 1단계 admin): 근태·휴가 카드와 켜기 전 점검표·노무 검토 안내가 보이고, 켜기·끄기가 한국어로 동작하며, 영문 코드가 없고, 폭 390 에서 넘치지 않는다", async () => {
  const admin = await loginDirect("admin");
  await admin.goto(`${BASE}/admin/modules`);
  await admin.getByRole("heading", { name: "업무 모듈" }).waitFor();
  const card = admin.getByRole("region", { name: "근태·휴가" });
  await card.getByText("켜기 전 점검표").waitFor();
  await card.getByText(/공휴일 등록/).first().waitFor();
  await card.getByText(/운영 전에 노무 검토가 필요합니다/).waitFor();
  const body = await card.innerText();
  assert(!/\b(attendance|manage_module|enabled)\b/.test(body), "모듈 화면에 영문 코드가 보임");
  await card.getByRole("button", { name: "「인사 담당」 역할 만들기" }).waitFor();
  const on = card.getByRole("button", { name: "켜기", exact: true });
  await on.waitFor();
  if (await on.isEnabled()) {
    await on.click();
    await card.getByText("켜짐", { exact: true }).waitFor();
    await shot(admin, "110-admin-modules-on");
    await card.getByRole("button", { name: "끄기", exact: true }).click();
    await admin.getByRole("alertdialog").getByRole("button", { name: "끄기" }).click();
    await card.getByText("꺼짐", { exact: true }).waitFor();
  } else {
    await card.getByText("필수 미비").first().waitFor();
    await card.getByText("필수 점검 항목이 모두 통과해야 켤 수 있습니다.").waitFor();
  }
  await shot(admin, "110b-admin-modules");
  await narrowCheck(admin, "110c-admin-modules-narrow", async () => {
    await admin.getByRole("heading", { name: "업무 모듈" }).waitFor();
  });
});
await step("근태·휴가 설정(B50 2단계 admin): 노무 검토 안내·연차 규칙 저장·규칙 이력·휴가 종류가 한국어로 보이고, 영문 코드가 없으며, 폭 390 에서 넘치지 않는다", async () => {
  const admin = await loginDirect("admin");
  await admin.goto(`${BASE}/admin/attendance`);
  await admin.getByRole("heading", { name: "근태·휴가 설정" }).waitFor();
  await admin.getByText(/운영 전에 노무 검토가 필요합니다/).waitFor();
  await admin.getByRole("button", { name: "규칙 저장" }).click();
  await admin.getByText(/연차 규칙을 저장했습니다/).waitFor();
  const hist = admin.getByRole("table", { name: "연차 규칙 이력" });
  await hist.getByText("입사일 기준").first().waitFor();
  await hist.getByText("적용 중").first().waitFor();
  const kinds = admin.getByRole("table", { name: "휴가 종류" });
  await kinds.getByText("연차", { exact: true }).first().waitFor();
  await kinds.getByText("병가").waitFor();
  const body = await admin.locator("main").innerText();
  assert(!/\b(hire|fiscal|annual|monthly|upfront|sick)\b/.test(body), "근태 설정 화면에 영문 코드가 보임");
  await shot(admin, "111-admin-attendance-settings");
  await narrowCheck(admin, "111b-admin-attendance-settings-narrow", async () => {
    await admin.getByRole("heading", { name: "근태·휴가 설정" }).waitFor();
  });
});
await step("내 근태(B50 5단계 staff·admin): 출근 단추가 서버 시각을 기록하고, 이번 주 기록·정정 요청 양식이 한국어로 보이며, 근태 관리(마감)가 열리고, 영문 코드가 없으며, 폭 390 에서 넘치지 않는다", async () => {
  // 모듈을 켠다(점검표가 막으면 이 단계는 건너뛴다: 모듈 화면 단계가 점검표 동작을 따로 본다).
  let on = true;
  try {
    const y = new Date().getFullYear();
    for (const d of [`${y}-12-25`, `${y + 1}-12-25`]) await api("admin", "POST", "/api/company/holidays", { items: [{ date: d, name: "성탄절" }] }).catch(() => {});
    await api("admin", "POST", "/api/admin/attendance/policies", { effectiveFrom: "2026-01-01", basis: "hire", workdayMinutes: 480, hourUnitMinutes: 60, attendanceRule: true, carryMaxMinutes: 0, carryValidMonths: 12, expireMonths: 12, firstYearMode: "monthly" }).catch(() => {});
    await api("admin", "PUT", "/api/admin/modules/attendance", { enabled: true });
  } catch {
    on = false;
    console.log("    (근태 모듈을 켤 수 없어 건너뜀)");
  }
  if (!on) return;
  try {
    const staff = await loginDirect("staff");
    await staff.goto(`${BASE}/attendance`);
    await staff.getByRole("heading", { name: "내 근태" }).waitFor();
    const clockIn = staff.getByRole("button", { name: "출근", exact: true });
    if (await clockIn.isEnabled()) {
      await clockIn.click();
      await staff.getByText("출근을 기록했습니다.").waitFor();
    }
    await staff.getByText(/^출근 \d\d:\d\d/).first().waitFor();
    await staff.getByRole("table", { name: "이번 주 출퇴근 기록" }).waitFor();
    await staff.getByRole("form", { name: "정정 요청 양식" }).waitFor();
    const body = await staff.locator("main").innerText();
    assert(!/\b(check_in|check_out|requested|applied|caution)\b/.test(body), "내 근태 화면에 영문 코드가 보임");
    await shot(staff, "112-attendance-me");
    await narrowCheck(staff, "112b-attendance-me-narrow", async () => {
      await staff.getByRole("heading", { name: "내 근태" }).waitFor();
    });
    await staff.goto(`${BASE}/attendance/calendar`);
    await staff.getByRole("heading", { name: "부서 달력" }).waitFor();
    await staff.getByRole("table", { name: "월 달력" }).or(staff.getByText("보여 줄 소속원이 없습니다.")).first().waitFor();
    await shot(staff, "113-attendance-calendar");
    await narrowCheck(staff, "113b-attendance-calendar-narrow", async () => {
      await staff.getByRole("heading", { name: "부서 달력" }).waitFor();
    });
    const lead = await loginDirect("lead");
    await lead.goto(`${BASE}/attendance/team`);
    await lead.getByRole("heading", { name: "팀 근태 현황" }).waitFor();
    await lead.getByRole("group", { name: "전체 인원" }).waitFor();
    const teamText = await lead.locator("main").innerText();
    assert(!/\b(over|caution|present)\b/.test(teamText), "팀 근태 화면에 영문 코드가 보임");
    await shot(lead, "114-attendance-team");
    await narrowCheck(lead, "114b-attendance-team-narrow", async () => {
      await lead.getByRole("heading", { name: "팀 근태 현황" }).waitFor();
    });
    const admin = await loginDirect("admin");
    await admin.goto(`${BASE}/attendance/admin`);
    // 관리자는 기본 역할에 인사 담당 권한이 없을 수 있다: 열리면 마감 양식을, 아니면 권한 안내를 확인한다.
    await admin.getByRole("heading", { name: "근태 관리" }).or(admin.getByText(/권한이 없습니다/)).first().waitFor();
  } finally {
    await api("admin", "PUT", "/api/admin/modules/attendance", { enabled: false }).catch(() => {});
  }
});
await step("좁은 화면: 문서 검증이 가로로 넘치지 않는다(finance)", async () => {
  const page = await loginDirect("finance");
  await narrowCheck(page, "34-narrow-documents", async () => {
    await page.goto(`${BASE}/documents`);
    await page.getByRole("heading", { name: "문서 검증" }).waitFor();
  });
});
await step("좁은 화면: AI 연결 사용 현황이 가로로 넘치지 않는다(auditor)", async () => {
  const page = await loginDirect("auditor");
  await narrowCheck(page, "35-narrow-mcp-usage", async () => {
    await page.goto(`${BASE}/mcp-usage`);
    await page.getByRole("heading", { name: "AI 연결 사용 현황" }).waitFor();
    await page.locator('table[aria-label="도구별 사용량"] tbody tr').first().waitFor();
  });
});

// ---------------------------------------------------------------------------
await browser.close();

console.log("\n--- 콘솔·네트워크 ---");
if (problems.length === 0) {
  pass("콘솔 오류·처리되지 않은 예외·의도하지 않은 4xx/5xx·요청 실패 없음");
} else {
  for (const p of [...new Set(problems)]) fail("브라우저 문제", p);
}

const failed = results.filter((r) => !r.ok);
console.log(`\n브라우저 스모크: 통과 ${results.length - failed.length} / 실패 ${failed.length}`);
process.exit(failed.length === 0 ? 0 : 1);
