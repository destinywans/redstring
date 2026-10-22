import { test, expect, type Locator, type Page, type Request } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, en, json, toasts, zh } from "./support/checks";

/**
 * PA-128 (WAN-458): the postmortem detail page /postmortems/:id through draft → review → published. The postmortem and
 * every write to it are pinned with page.route, and any other write under /postmortems is aborted, so this runs on any
 * data and never writes. tools/acceptance/regress.py runs it in every regression.
 *
 *   npx playwright test -c e2e/playwright.config.ts postmortem-detail.spec.ts
 */
const origin = INSTANCES.prod.origin;
const ID = "pm-pa128-check";
const PATH = `/postmortems/${ID}`;
/**
 * Real incident IDs are "INC-" and 32 hex digits: 36 characters, the 32 digits with no place to break and about as
 * wide as the card in a monospace font, so they only fit by wrapping anywhere.
 */
const INCIDENT = "INC-c0ffee00a1284c4e8c4e0000000a1280";
const T0 = "2026-10-20T08:00:00Z";
const T1 = "2026-10-21T09:30:00Z";

type Lang = "zh" | "en";
const TITLES: Record<Lang, [string, string, string]> = { zh: ["事件摘要", "影响", "根因"], en: ["Incident summary", "Impact", "Root cause"] };
const postmortem = (patch: Record<string, unknown> = {}, lang: Lang = "zh") => ({
  id: ID,
  incident_id: INCIDENT,
  status: "draft",
  revision: 7,
  source_revision: "src-pa128-check",
  sections: [
    { key: "summary", title: TITLES[lang][0], text: "检查用：checkout-api 5xx 升高 18 分钟" },
    { key: "impact", title: TITLES[lang][1], text: "" },
    { key: "root", title: TITLES[lang][2], text: "" },
  ],
  actions: [{ text: "检查用：给连接池加告警", owner: "检查团队", due: "2026-10-31" }],
  authored_by_me: true,
  author: "检查作者",
  created_at: T0,
  updated_at: T0,
  incident: { id: INCIDENT, title: "检查用：checkout-api 5xx 升高", severity: "P2", started_at: "2026-10-19T02:00:00Z", closed_at: "2026-10-19T02:18:00Z", duration: "18m" },
  submitted: null,
  published: null,
  returned: null,
  ...patch,
});
const filled = (lang: Lang = "zh") =>
  postmortem({}, lang).sections.map((s) => ({ ...s, text: s.text || `检查用：${s.title}已填写` }));
const SUBMITTED = { by: "检查作者", at: T1 };
const IN_REVIEW_MINE = postmortem({ status: "review", sections: filled(), submitted: SUBMITTED });
const IN_REVIEW = postmortem({ status: "review", sections: filled(), submitted: SUBMITTED, authored_by_me: false });
const PUBLISHED = postmortem({ status: "published", sections: filled(), submitted: SUBMITTED, published: { by: "检查审核人", at: T1 }, authored_by_me: false });

/**
 * Pins GET /postmortems/{id} to `answer()` (read on every request, so a test can change what a re-read returns) and aborts
 * every other write under /postmortems, so nothing reaches the server even if the page sends something unexpected.
 * Returns the writes that were aborted.
 */
async function pin(page: Page, answer: () => [number, unknown]) {
  const aborted: string[] = [];
  await page.route(/\/api\/v1\/postmortems(\/|\?|$)/, (r) => {
    if (r.request().method() === "GET") return r.fallback();
    aborted.push(`${r.request().method()} ${new URL(r.request().url()).pathname}`);
    return r.abort();
  });
  await page.route(at(PATH), (r) => {
    if (r.request().method() !== "GET") return r.fallback();
    const [status, body] = answer();
    return json(r, status, body);
  });
  return aborted;
}

const heading = (page: Page) => page.locator(".pm-center h1");
const badge = (page: Page) => page.locator(".pm-center .v3-row", { has: page.locator("h1") }).locator(".v3-badge");
const rail = (page: Page) => page.locator(".pm-right");
const card = (page: Page) => page.locator(".pm-left .v3-card").first();
const section = (page: Page, key: string) => page.locator(`#pm-t-${key}`);

/** True when `inner`'s box lies inside `outer`'s (1px slack for rounding). */
async function inside(inner: Locator, outer: Locator) {
  const [a, b] = [await inner.boundingBox(), await outer.boundingBox()];
  if (!a || !b) return false;
  return a.x >= b.x - 1 && a.y >= b.y - 1 && a.x + a.width <= b.x + b.width + 1 && a.y + a.height <= b.y + b.height + 1;
}

/**
 * The incident ID's text stays inside the card and the card has no horizontal overflow. The text itself is measured: the
 * card is a flex column, so the ID's box is stretched to the card's width even when its text runs past it.
 */
async function fitsCard(page: Page) {
  const c = card(page);
  const id = c.getByText(INCIDENT, { exact: true });
  await expect(id).toBeVisible();
  const [text, box] = await id.evaluate((el) => {
    const range = document.createRange();
    range.selectNodeContents(el);
    const t = range.getBoundingClientRect();
    const b = el.closest(".v3-card")!.getBoundingClientRect();
    return [{ left: t.left, right: t.right }, { left: b.left, right: b.right }];
  });
  expect(text.left >= box.left && text.right <= box.right, `incident ID text ${JSON.stringify(text)} inside the card ${JSON.stringify(box)}`).toBe(true);
  expect(await c.evaluate((el) => el.scrollWidth <= el.clientWidth), "card has no horizontal overflow").toBe(true);
}

test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 }, permissions: ["clipboard-read", "clipboard-write"] });

/* ---------------- draft ---------------- */

test.describe("draft (PA-128)", () => {
  test("empty sections hold back review and are listed; an action item without an owner holds back saving", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    const aborted = await pin(page, () => [200, postmortem()]);
    await page.goto(origin + `/postmortems/${ID}`);
    await expect(heading(page)).toHaveText("复盘：检查用：checkout-api 5xx 升高", { timeout: 20_000 });
    await expect(badge(page)).toHaveText("草稿");
    const submit = rail(page).getByRole("button", { name: "提交审核" });
    const save = rail(page).getByRole("button", { name: "保存", exact: true });
    await expect(submit).toBeDisabled();
    await expect(save).toBeDisabled(); // nothing changed yet
    await expect(rail(page).getByText("还有 2 个章节没填写：影响、根因")).toBeVisible();
    await expect(page.locator(".pm-section").getByText("提交审核前需要填写")).toHaveCount(2);

    await section(page, "impact").fill("检查用：结账失败率 4%");
    await expect(rail(page).getByText("还有 1 个章节没填写：根因")).toBeVisible();
    await expect(submit).toBeDisabled();
    await section(page, "root").fill("检查用：连接池耗尽");
    await expect(rail(page).getByText(/个章节没填写/)).toHaveCount(0);
    await expect(submit).toBeEnabled();
    await expect(save).toBeEnabled();
    await expect(rail(page).getByText("有未保存的修改")).toBeVisible();

    // A new action item with an item and a due date but no owner.
    await page.getByRole("button", { name: "添加改进项" }).click();
    const row = page.locator(".pm-actions tbody tr").nth(1);
    await row.getByRole("textbox", { name: "事项" }).fill("检查用：补一条容量告警");
    await row.getByRole("textbox", { name: "期限" }).fill("2026-11-15");
    await expect(rail(page).getByText("每个改进项都要填事项、负责人和期限")).toBeVisible();
    await expect(submit).toBeDisabled();
    await expect(save).toBeDisabled();
    // Whitespace is not an owner either.
    await row.getByRole("textbox", { name: "负责人" }).fill("   ");
    await expect(rail(page).getByText("每个改进项都要填事项、负责人和期限")).toBeVisible();
    await row.getByRole("textbox", { name: "负责人" }).fill("检查团队");
    await expect(rail(page).getByText("每个改进项都要填事项、负责人和期限")).toHaveCount(0);
    await expect(submit).toBeEnabled();
    expect(aborted).toEqual([]);
    expect(m.pageErrors).toEqual([]);
  });

  for (const lang of ["zh", "en"] as const) {
    const t =
      lang === "zh"
        ? { save: "保存", conflict: "没有保存：这份复盘在你打开之后被别人改过。", copy: "复制我的修改", reload: "重新加载", unsaved: "有未保存的修改", copied: "已复制你的修改" }
        : { save: "Save", conflict: "Not saved: someone changed this postmortem after you opened it.", copy: "Copy my edits", reload: "Reload", unsaved: "Unsaved changes", copied: "Your edits are copied" };
    test(`save sends If-Match with the revision; a 412 keeps the edits, buttons inside the notice (${lang})`, async ({ page }) => {
      await page.addInitScript(lang === "zh" ? zh : en);
      const m = monitor(page);
      const aborted = await pin(page, () => [200, postmortem({}, lang)]);
      const patches: Request[] = [];
      await page.route(at(PATH), (r) => {
        if (r.request().method() !== "PATCH") return r.fallback();
        patches.push(r.request());
        return json(r, 412, { error: { code: "postmortem_revision_conflict", message: "The postmortem changed; reload it before saving" } });
      });
      await page.goto(origin + `/postmortems/${ID}`);
      await expect(section(page, "impact")).toBeVisible({ timeout: 20_000 });
      const edit = "检查用：结账失败率 4%，约 1200 笔订单";
      await section(page, "impact").fill(edit);
      await rail(page).getByRole("button", { name: t.save, exact: true }).click();

      const notice = page.locator(".f-callout", { hasText: t.conflict });
      await expect(notice).toBeVisible();
      expect(patches).toHaveLength(1);
      expect(patches[0].headers()["if-match"]).toBe('"7"');
      const sent = patches[0].postDataJSON();
      expect(sent.sections.find((s: { key: string }) => s.key === "impact").text).toBe(edit);
      expect(sent.actions).toEqual(postmortem().actions);
      expect(sent).not.toHaveProperty("status"); // a save, not a submit
      // The edits stay in the editor and are still unsaved; the server's English never shows.
      await expect(section(page, "impact")).toHaveValue(edit);
      await expect(rail(page).getByText(t.unsaved)).toBeVisible();
      expect((await toasts(page)).join("\n")).not.toContain("reload it before saving");
      // Both buttons sit inside the notice, under its text (they were pushed past it at 1440 wide).
      const copy = notice.getByRole("button", { name: t.copy });
      const reload = notice.getByRole("button", { name: t.reload });
      await expect(copy).toBeVisible();
      await expect(reload).toBeVisible();
      expect(await inside(copy, notice), "copy button inside the notice").toBe(true);
      expect(await inside(reload, notice), "reload button inside the notice").toBe(true);
      expect(await notice.evaluate((el) => el.scrollWidth <= el.clientWidth), "notice has no horizontal overflow").toBe(true);

      await copy.click();
      await expect.poll(() => toasts(page)).toContain(t.copied);
      expect(await page.evaluate(() => navigator.clipboard.readText())).toContain(edit);
      // Reload replaces the edits with the server's version and clears the notice.
      await reload.click();
      await expect(notice).toHaveCount(0);
      await expect(section(page, "impact")).toHaveValue("");
      await expect(rail(page).getByText(t.unsaved)).toHaveCount(0);
      expect(aborted).toEqual([]);
      expect(m.pageErrors).toEqual([]);
    });
  }
});

/* ---------------- in review ---------------- */

test.describe("in review (PA-128)", () => {
  test("the author gets no publish or return, only a link to send a reviewer", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    const aborted = await pin(page, () => [200, IN_REVIEW_MINE]);
    await page.goto(origin + `/postmortems/${ID}`);
    await expect(badge(page)).toHaveText("待审核", { timeout: 20_000 });
    await expect(page.locator(".pm-center textarea")).toHaveCount(0); // locked
    await expect(page.getByRole("button", { name: "发布", exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "退回修改" })).toHaveCount(0);
    const link = rail(page).getByRole("button", { name: "复制链接发给审核人" });
    await expect(link).toBeEnabled();
    await link.click();
    await expect.poll(() => toasts(page)).toContain("链接已复制");
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(origin + `/postmortems/${ID}`);
    expect(aborted).toEqual([]);
    expect(m.pageErrors).toEqual([]);
  });

  test("a reviewer publishes or returns it; the return needs a reason, sends {reason}, and the draft shows it on top", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    const REASON = "检查用：影响章节缺少用户侧数据";
    const RETURNED = postmortem({ sections: filled(), returned: { by: "检查审核人", at: T1, reason: REASON }, authored_by_me: false, revision: 8 });
    let returned = false;
    const aborted = await pin(page, () => [200, returned ? RETURNED : IN_REVIEW]);
    const posts: Request[] = [];
    await page.route(at(`${PATH}/return`), (r) => {
      if (r.request().method() !== "POST") return r.fallback();
      posts.push(r.request());
      returned = true;
      return json(r, 200, RETURNED);
    });
    await page.goto(origin + `/postmortems/${ID}`);
    await expect(badge(page)).toHaveText("待审核", { timeout: 20_000 });
    await expect(rail(page).getByRole("button", { name: "发布", exact: true })).toBeEnabled();
    await expect(rail(page).getByText("等你审核")).toBeVisible();
    await expect(rail(page).getByRole("button", { name: "复制链接发给审核人" })).toHaveCount(0);

    await rail(page).getByRole("button", { name: "退回修改" }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("退回给 检查作者 修改");
    const send = dialog.getByRole("button", { name: "退回", exact: true });
    await expect(send).toBeDisabled();
    await dialog.locator("#pm-return-reason").fill("   ");
    await expect(send).toBeDisabled(); // whitespace is not a reason
    await dialog.locator("#pm-return-reason").fill(`  ${REASON}\n`);
    await expect(send).toBeEnabled();
    await send.click();

    await expect(dialog).toHaveCount(0);
    expect(posts).toHaveLength(1);
    expect(posts[0].postDataJSON()).toEqual({ reason: REASON });
    await expect.poll(() => toasts(page)).toContain("已退回给 检查作者");
    await expect(badge(page)).toHaveText("草稿");
    const notice = page.locator(".f-callout", { hasText: "检查审核人" });
    await expect(notice).toContainText("退回");
    await expect(notice).toContainText(REASON);
    // On top: above the first section.
    const [n, s] = [await notice.boundingBox(), await page.locator(".pm-section").first().boundingBox()];
    expect(n && s && n.y + n.height <= s.y).toBe(true);
    await expect(rail(page).getByText("检查审核人 退回，等待修改")).toBeVisible();
    expect(aborted).toEqual([]); // publish was never sent
    expect(m.pageErrors).toEqual([]);
  });
});

/* ---------------- published ---------------- */

test.describe("published (PA-128)", () => {
  test("leads on to a runbook draft from this postmortem", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    const aborted = await pin(page, () => [200, PUBLISHED]);
    await page.goto(origin + `/postmortems/${ID}`);
    await expect(badge(page)).toHaveText("已发布", { timeout: 20_000 });
    await expect(page.locator(".pm-center textarea")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "发布", exact: true })).toHaveCount(0);
    const draft = rail(page).getByRole("link", { name: "从这份复盘起草 Runbook" });
    await expect(draft).toHaveAttribute("href", `/runbooks/new?source=postmortem&from=${ID}`);
    expect(aborted).toEqual([]);
    expect(m.pageErrors).toEqual([]);
  });
});

/* ---------------- incident card ---------------- */

test.describe("incident card (PA-128)", () => {
  test("a 36-character incident ID stays inside the card", async ({ page }) => {
    await page.addInitScript(zh);
    await pin(page, () => [200, postmortem()]);
    await page.goto(origin + `/postmortems/${ID}`);
    await expect(card(page).getByText("检查用：checkout-api 5xx 升高", { exact: true })).toBeVisible({ timeout: 20_000 });
    await fitsCard(page);
    await expect(card(page).getByRole("link", { name: "打开事件" })).toHaveAttribute("href", `/incidents/${INCIDENT}`);
  });

  for (const [lang, text, title] of [
    ["zh", "读不到这个事件保存时的解决记录，标题和时间未知。", `${INCIDENT} 复盘`],
    ["en", "The incident's saved resolution can't be read; its title and times are unknown.", `${INCIDENT} postmortem`],
  ] as const) {
    test(`without the incident's resolution the card says so (${lang})`, async ({ page }) => {
      await page.addInitScript(lang === "zh" ? zh : en);
      const m = monitor(page);
      await pin(page, () => [200, postmortem({ incident: null }, lang)]);
      await page.goto(origin + `/postmortems/${ID}`);
      await expect(card(page).getByText(text)).toBeVisible({ timeout: 20_000 });
      await expect(heading(page)).toHaveText(title);
      await fitsCard(page);
      expect(m.pageErrors).toEqual([]);
    });
  }
});

/* ---------------- not found ---------------- */

test.describe("not found (PA-128)", () => {
  for (const [lang, title, desc] of [
    ["zh", "找不到这份复盘", "还没发布的复盘只有作者和知识管理员能看到"],
    ["en", "Postmortem not found", "An unpublished postmortem is visible only to its author and knowledge managers"],
  ] as const) {
    test(`a 404 says the postmortem can't be found (${lang})`, async ({ page }) => {
      await page.addInitScript(lang === "zh" ? zh : en);
      const m = monitor(page);
      await pin(page, () => [404, { error: { code: "not_found", message: "Postmortem not found" } }]);
      await page.goto(origin + `/postmortems/${ID}`);
      await expect(page.getByText(desc)).toBeVisible({ timeout: 20_000 });
      await expect(page.getByText(title, { exact: true })).toBeVisible();
      await expect(page.locator(".pm-layout")).toHaveCount(0);
      expect(m.pageErrors).toEqual([]);
    });
  }
});
