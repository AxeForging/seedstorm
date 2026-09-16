// Seeding one deep table of the wide schema from the workspace with parallel
// writers: required parents lock in, progress streams, and the database ends
// with exactly the rows the run reports.
import { DB, pgCounts } from "../support/db.helpers";
import { WIDE } from "../support/databases.fixture";
import { sel } from "../support/selectors";
import { requiredClosure } from "../support/wide-schema.fixture";
import {
  connectPostgres, expect, fmt, nodeOnScreen, nodesWithClass, openWorkspace, test,
} from "../support/test.fixture";

const TABLE = "t038_ticket";
const ROWS = 20_000;
const ws = sel.workspace;

test("seed a deep table with 4 writers streams progress and writes every required parent", async ({ page }) => {
  test.setTimeout(180_000);
  const scope = [...requiredClosure(WIDE, TABLE)].sort();
  expect(scope.length, "fixture: the table needs several parents").toBeGreaterThanOrEqual(5);
  const before = await pgCounts(DB.wide, scope);

  await connectPostgres(page, DB.wide);
  await openWorkspace(page, 150);
  const canvas = page.getByTestId(ws.canvas);

  await test.step("clicking the table in the graph selects it and locks its parents", async () => {
    // Find it first: a single match is centred on the canvas.
    await page.getByTestId(ws.search).fill(TABLE);
    await expect(page.getByTestId(ws.searchCount)).toHaveText("1 match");
    await expect.poll(async () => {
      const at = await nodeOnScreen(page, TABLE);
      const box = (await canvas.boundingBox())!;
      return Math.abs(at.x - box.width / 2) < 4 && Math.abs(at.y - box.height / 2) < 4;
    }).toBe(true);
    await canvas.click({ position: await nodeOnScreen(page, TABLE) });
    await page.getByTestId(ws.tabSelected).click();

    const items = page.getByTestId(ws.selectedItem);
    await expect(items).toHaveCount(scope.length);
    const picked = items.filter({ has: page.getByTestId(ws.selectedTag).getByText("selected", { exact: true }) });
    const locked = items.filter({ has: page.getByTestId(ws.selectedTag).getByText("auto", { exact: true }) });
    await expect(picked.getByTestId(ws.selectedName)).toHaveText([TABLE]);
    await expect(locked.getByTestId(ws.selectedName)).toHaveText(scope.filter((t) => t !== TABLE));
    await expect(page.getByTestId(ws.autoCount)).toHaveText(String(scope.length - 1));
  });

  const total = ROWS * scope.length;
  await test.step("volume and writers", async () => {
    await page.getByTestId(ws.rows).fill(String(ROWS));
    await page.getByTestId(ws.tuningToggle).click();
    await page.getByTestId(ws.workers).fill("4");
    await expect(page.getByTestId(ws.tuningSummary)).toHaveText("4 writers");
    await expect(page.getByTestId(ws.run)).toHaveText(`Seed ${scope.length} tables`);
  });

  const count = page.getByTestId(sel.job.progressCount);

  await test.step("progress counts up to the planned total", async () => {
    // Record every value the counter shows, however fast the run is.
    await count.evaluate((el) => {
      const seen: string[] = ((window as any).__progress = []);
      new MutationObserver(() => {
        if (el.textContent && seen[seen.length - 1] !== el.textContent) seen.push(el.textContent);
      }).observe(el, { childList: true, characterData: true, subtree: true });
    });
    await page.getByTestId(ws.run).click();
    await expect(count).toHaveText(`${fmt(total)} / ${fmt(total)} rows`, { timeout: 120_000 });
    const seen: string[] = await page.evaluate(() => (window as any).__progress);
    const values = seen.map((text) => {
      const m = /^([\d,]+) \/ ([\d,]+) rows$/.exec(text);
      expect(m, `progress text ${text}`).not.toBeNull();
      expect(Number(m![2].replace(/,/g, "")), "planned total").toBe(total);
      return Number(m![1].replace(/,/g, ""));
    });
    expect(values.length, `progress updates: ${seen.join(" | ")}`).toBeGreaterThanOrEqual(3);
    expect(values.some((v) => v > 0 && v < total), "a part-way count was shown").toBe(true);
    expect(values, "the count never goes back").toEqual([...values].sort((a, b) => a - b));
  });

  await test.step("the run ends done with every table lit", async () => {
    const status = page.getByTestId(sel.job.status);
    await expect(status).toHaveText("done", { timeout: 60_000 });
    await expect.poll(() => nodesWithClass(page, "done")).toEqual(scope);
    await expect(page.getByTestId(sel.job.stat("rows")).getByTestId(sel.job.statValue)).toHaveText(`${(total / 1000).toFixed(1)}k`);
  });

  await test.step("the database holds exactly the rows the run reported", async () => {
    const after = await pgCounts(DB.wide, scope);
    for (const t of scope) expect(after[t] - before[t], t).toBe(ROWS);
    // Refreshed counts reach the rail too.
    const populated = Object.values(await pgCounts(DB.wide)).filter((n) => n > 0).length;
    await expect(page.getByTestId(ws.populatedCount)).toHaveText(String(populated));
  });
});
