// Run with Node's test runner and Playwright on the module path; see README.md.
// omp's Advisor (#5019): the row says what the advisor runs, and its Edit
// opens a sheet the width of Codex's, whose first page fits 800×600 without
// scrolling. A WATCHDOG.yml with instructions alone (advisors null or
// missing) draws a roster of none, with no error; a roster with the switch
// off says omp runs none of it, on the row and under the switch. A pick is
// written at once with the version of the files the window read, and one
// refused as changed since (409) shows magpie's words in the reader's
// language and puts the pick back. An advisor's row opens its own page, and
// a project not picked yet can still be typed. In English and Chinese, at
// 800×600 and at 390px. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = [
  { value: "magpie/deepseek/pro", label: "DeepSeek Pro", ref: "deepseek/pro" },
  { value: "magpie/deepseek/flash", label: "DeepSeek Flash", ref: "deepseek/flash" },
];
const field = (key, label, value) => ({ key, label, value, options });
const SETTINGS = [
  { key: "advisor.enabled", kind: "bool", default: "false", label: "Enable Advisor", description: "Pair a second model." },
  { key: "advisor.reviewMode", kind: "enum", default: "turn", label: "Advisor Review Mode", description: "When it reviews.",
    options: [{ value: "turn", label: "Every turn" }, { value: "agent-end", label: "Agent end" }] },
  { key: "advisor.reviewInterval", kind: "number", default: "1", label: "Advisor Review Interval", description: "Turns between.", min: 1, max: 1000 },
];
// a view as agent.OmpAdvisorView sends it, its roster's advisors as given
const view = (watchdog, roster) => ({
  settings: SETTINGS, tools: ["read", "grep"], subagents: ["task"], projects: [],
  user: { config: "~/.omp/agent/config.yml", watchdogFile: "~/.omp/agent/WATCHDOG.yml", notesFile: "~/.omp/agent/WATCHDOG.md",
    settings: {}, subagents: {}, watchdog, version: "v1" },
  effective: { settings: {}, roster, rosters: roster.length ? ["~/.omp/agent/WATCHDOG.yml"] : [] },
  levels: { "magpie/deepseek/pro": ["low", "medium", "high", "max"], "magpie/deepseek/flash": ["high", "xhigh"] },
});
const BRIEF = { enabled: false, roster: "WATCHDOG.yml", count: 2, row: "Off · {roster}: {count} advisors", note: "advisor not enabled" };

function server(lang, at, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [{ id: "omp", name: "omp", path: "/test/config.yml", icon: "omp", wired: true, advisor: BRIEF,
      fields: [field("model", "model", "magpie/deepseek/pro"), field("advisor", "advisor", "")] }], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/agents/omp-advisor/omp") {
      if (req.method() === "GET") return route.fulfill({ json: at.view });
      posts.push(req.postDataJSON());
      return route.fulfill({ status: 409, json: { error: "The advisor's files changed since this window read them: close it and open it again",
        say: { key: "The advisor's files changed since this window read them: close it and open it again" } } });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/agents/cli") return route.fulfill({ json: { agents: {}, pending: false } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the words as i18n.js has them
const W = {
  en: { row: "Off · WATCHDOG.yml: 2 advisors", edit: "Edit", none: "None: omp runs one advisor",
    off: "Off, so omp runs none of WATCHDOG.yml's advisors", on: "On", offWord: "Off", done: "Done",
    paused: "Paused", thinking: "Thinking level", project: "In a project", type: "Type a folder…",
    stale: "The advisor's files changed since this window read them: close it and open it again" },
  zh: { row: "关闭 · WATCHDOG.yml 2 个顾问", edit: "编辑", none: "没有：omp 用顾问角色的模型",
    off: "关闭时 omp 不运行 WATCHDOG.yml 里的顾问", on: "开启", offWord: "关闭", done: "完成",
    paused: "已暂停", thinking: "思考强度", project: "某个项目", type: "输入文件夹路径…",
    stale: "顾问的文件在这个窗口读取之后被改过了：关掉窗口再打开一次" },
};

const omp = '.row.agent[data-id="omp"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [width, height] of [[800, 600], [390, 844]]) for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang} ${width}px: omp's Advisor row and sheet`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await browser.newPage({ viewport: { width, height } });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      const at = { view: view({ instructions: "Look closely.", advisors: null }, []) };
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, at, posts));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-omp-advisor.png`) });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/");
      await page.locator(omp).waitFor();
      await page.locator(`${omp} .ag-link`).click();

      // the row: a roster with the switch off runs nothing, and says so
      const edit = page.locator("button.ag-quiet", { hasText: w.edit });
      await edit.waitFor();
      assert.ok((await edit.locator("xpath=..").innerText()).includes(w.row), await edit.locator("xpath=..").innerText());

      // instructions alone, advisors null: a roster of none, no error
      const sheet = page.locator(".editor.omp-adv");
      const close = async () => {
        await sheet.locator(".bar button", { hasText: w.done }).click();
        await sheet.waitFor({ state: "detached" });
      };
      await edit.click();
      await sheet.waitFor();
      assert.ok((await sheet.locator(".oa-roster").innerText()).includes(w.none), await sheet.innerText());
      assert.equal(await sheet.locator(".oa-warn, .oa-err, .oa-off").count(), 0);
      assert.ok((await sheet.boundingBox()).width <= 600, "wider than Codex's sheet");
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");
      assert.deepEqual(errors, []);
      await close();

      // advisors missing altogether reads the same
      at.view = view({ instructions: "Look closely." }, []);
      await edit.click();
      await sheet.waitFor();
      assert.ok((await sheet.locator(".oa-roster").innerText()).includes(w.none));
      await close();
      assert.deepEqual(errors, []);

      // a roster, the switch off: a row an advisor, and the switch says omp
      // runs none of them
      const roster = [{ name: "quick", model: "magpie/deepseek/flash", tools: null, reviewInterval: 2 }, { name: "final", tools: null, reviewMode: "agent-end" }];
      at.view = view({ advisors: roster, instructions: "Look closely at deletions, history rewrites and config changes before they land." },
        roster.map((r) => ({ ...r, from: "~/.omp/agent/WATCHDOG.yml" })));
      await edit.click();
      await sheet.waitFor();
      assert.equal(await sheet.locator(".oa-adv").count(), 2);
      assert.ok((await sheet.locator('[data-key="advisor.enabled"] .sub.oa-off').innerText()).includes(w.off), await sheet.innerText());
      // the first page whole, without scrolling, in a 800×600 window
      if (width === 800) {
        const body = await sheet.locator(".ebody").evaluate((b) => [b.scrollHeight, b.clientHeight]);
        assert.ok(body[0] <= body[1], `the first page scrolls: ${body}`);
        const box = await sheet.boundingBox();
        assert.ok(box.y >= 0 && box.y + box.height <= height, `the sheet is taller than the window: ${JSON.stringify(box)}`);
      }
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "the page scrolls sideways");
      // a line and its value, the notes' long one too, stay one line
      for (const h of await sheet.locator(".oa-nav").evaluateAll((rs) => rs.map((r) => r.offsetHeight))) assert.ok(h <= 44, `a line wraps: ${h}px`);

      // a pick is written at once with the version read; a 409 shows
      // magpie's words translated, and the switch shows what is on disk
      const sw = sheet.locator('[data-key="advisor.enabled"] .segs');
      await sw.locator("button", { hasText: w.on }).click();
      await page.waitForFunction((s) => document.querySelector("#status")?.textContent.includes(s), w.stale);
      assert.equal(posts.length, 1);
      assert.deepEqual(posts[0], { settings: { "advisor.enabled": "true" }, version: "v1" });
      assert.equal(await sw.locator("button[aria-pressed=true]").innerText(), w.offWord);
      // a pick is drawn in place, not faded in
      const swap = () => page.locator("#modal .dialog").evaluate((d) => d.classList.contains("swap"));
      assert.equal(await swap(), false, "a pick flashes the sheet");

      // an advisor's line opens it out under it, in place; a pick there is
      // written at once, the rest of the roster as it was; the line folds it
      const quick = sheet.locator(".oa-adv").first();
      await quick.click();
      const body = sheet.locator(".oa-roster .oa-adv-body");
      await body.waitFor();
      assert.equal(await quick.getAttribute("aria-expanded"), "true");
      assert.equal(await sheet.locator(".oa-adv-body").count(), 1);
      assert.equal(await swap(), false, "opening an advisor flashes the sheet");
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "an advisor opened out scrolls sideways");
      if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-omp-advisor-open.png`) });
      // its thinking level: only those omp offers its model, and off
      await body.locator(".row.pref", { hasText: w.thinking }).locator(".proto.pick").click();
      assert.deepEqual(await page.locator(".proto-menu .pm-item").count(), 4);
      await page.keyboard.press("Escape");
      await body.locator(".segs button", { hasText: w.paused }).click();
      for (let k = 0; posts.length < 2 && k < 100; k++) await page.waitForTimeout(20);
      await sheet.locator(".oa-roster .oa-adv-body").waitFor();
      assert.equal(posts.length, 2);
      assert.equal(posts[1].watchdog.advisors[0].enabled, false);
      assert.deepEqual(posts[1].watchdog.advisors.slice(1), at.view.user.watchdog.advisors.slice(1));
      await quick.click();
      await body.waitFor({ state: "detached" });
      assert.equal(await sheet.locator(".oa-adv").first().getAttribute("aria-expanded"), "false");

      // in a project, none picked yet: the folder can still be typed
      await sheet.locator(".oa-where button", { hasText: w.project }).click();
      await sheet.locator(".ehead .proto.pick").click();
      await page.locator(".proto-menu button", { hasText: w.type }).click();
      await sheet.locator(".ebody input.words").waitFor();
      assert.deepEqual(errors, []);
    });
  }
}
