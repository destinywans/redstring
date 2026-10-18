import { test, expect } from "@playwright/test";
import { INSTANCES } from "./support/env";
import { statePath } from "./support/global-setup";
import { monitor } from "./support/monitor";
import { at, json, zh } from "./support/checks";

/**
 * Remaining parts of the sweep fixes (WAN-434): knowledge search hits without a vector score (PA-222) and the add
 * connector wizard / models page defaults (PA-224). Every read and write is pinned with page.route, so this runs on
 * any data and never writes.
 *
 *   npx playwright test -c e2e/playwright.config.ts phase2-fixes-rest-connectors.spec.ts
 */
const origin = INSTANCES.prod.origin;

test.use({ storageState: statePath("prod", "admin"), viewport: { width: 1440, height: 1000 } });

/**
 * PA-222 (WAN-581): in a hybrid search, a hit from a public web page or an upload has no vector index; the server says
 * vector=null with its corpus, and the page marks the score unknown instead of “vector 0.00”.
 */
test.describe("knowledge search hits without a vector score (PA-222)", () => {
  test("web and upload hits in a hybrid search don't print vector 0.00", async ({ page }) => {
    await page.addInitScript(zh);
    const m = monitor(page);
    const hits = [
      { title: "检查文档", snippet: "文档片段", keyword: 1, vector: 0.42, score: 0.65, corpus: "documents" },
      { title: "检查网页", snippet: "网页片段", keyword: 0.8, vector: null, score: 0.32, corpus: "public_web_sources" },
      { title: "检查上传", snippet: "上传片段", keyword: 0.6, vector: null, score: 0.24, corpus: "upload_sources" },
    ];
    await page.route(at("/knowledge/search"), (r) =>
      r.request().method() === "POST" ? json(r, 200, { hits, mode: "hybrid", degraded: false, corpora: [] }) : r.fallback(),
    );
    await page.goto(origin + "/knowledge-sources");
    await page.getByRole("textbox", { name: "检索问题" }).fill("检查", { timeout: 20_000 });
    await page.getByRole("button", { name: "检索", exact: true }).click();
    const doc = page.locator(".ret-hit", { hasText: "检查文档" });
    await expect(doc).toContainText("向量 0.42");
    await expect(doc.locator(".unk")).toHaveCount(0);
    for (const title of ["检查网页", "检查上传"]) {
      const hit = page.locator(".ret-hit", { hasText: title });
      await expect(hit).toContainText("关键词");
      await expect(hit).not.toContainText("向量 0.00");
      await expect(hit.locator(".unk")).toHaveCount(1);
      // Nothing failed: these sources have no vector index, so the mark doesn't say the source is unavailable.
      await expect(hit.locator(".unk")).not.toHaveAttribute("aria-label", /暂时不可用/);
    }
    expect(m.pageErrors).toEqual([]);
  });
});
