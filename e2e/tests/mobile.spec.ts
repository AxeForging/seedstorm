// Phone widths: the stacked workspace keeps rail, canvas and action bar apart
// with a search active, and no page scrolls sideways.
import type { Locator, Page } from "@playwright/test";
import { DB, withPg } from "../support/db.helpers";
import { sel } from "../support/selectors";
import { connectPostgres, expect, openWorkspace, pgConnectionLabel, test } from "../support/test.fixture";

const ws = sel.workspace;
// t000_account references nothing, so rows can go straight in. Unbreakable
// 60-character labels make its row preview wider than a phone.
const WIDE_ROWS_TABLE = "t000_account";

test.beforeAll(async () => {
  await withPg(DB.wide, async (c) => {
    await c.query(`DELETE FROM ${WIDE_ROWS_TABLE} WHERE label LIKE 'e2e-wide-%'`);
    await c.query(`INSERT INTO ${WIDE_ROWS_TABLE} (label, amount) SELECT 'e2e-wide-' || repeat('w', 50) || g, g FROM generate_series(1, 3) g`);
  });
});

async function expectNoHorizontalScroll(page: Page) {
  const size = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, inner: window.innerWidth }));
  expect(size.scroll, `document is ${size.scroll}px wide in a ${size.inner}px viewport`).toBeLessThanOrEqual(size.inner);
}

async function box(locator: Locator) {
  await expect(locator).toBeVisible();
  const b = (await locator.boundingBox())!;
  return { ...b, name: await locator.getAttribute("data-testid") };
}

function overlap(a: { x: number; y: number; width: number; height: number }, b: typeof a) {
  const w = Math.min(a.x + a.width, b.x + b.width) - Math.max(a.x, b.x);
  const h = Math.min(a.y + a.height, b.y + b.height) - Math.max(a.y, b.y);
  return w > 0 && h > 0 ? w * h : 0;
}

for (const size of [{ width: 390, height: 844 }, { width: 320, height: 640 }]) {
  test.describe(`${size.width}x${size.height}`, () => {
    test.use({ viewport: size, isMobile: true, hasTouch: true });

    test("workspace with a search active stacks without overlap or sideways scroll", async ({ page }) => {
      await connectPostgres(page, DB.wide);
      await openWorkspace(page, 150);
      // The first match opens in the Table tab with its row preview.
      await page.getByTestId(ws.search).fill("account");
      const matchBar = page.getByTestId(ws.matchBar);
      await expect(page.getByTestId(ws.matchLabel)).toHaveText(/^1 of \d+ matches$/);
      await expect(page.getByTestId(ws.previewTable)).toContainText("e2e-wide-");

      const parts = [await box(page.getByTestId(ws.rail)), await box(page.getByTestId(ws.canvasWrap)), await box(page.getByTestId(ws.actionBar))];
      for (let i = 0; i < parts.length; i++) {
        expect(parts[i].x, `${parts[i].name} starts inside the screen`).toBeGreaterThanOrEqual(0);
        expect(parts[i].x + parts[i].width, `${parts[i].name} ends inside the screen`).toBeLessThanOrEqual(size.width);
        for (let j = i + 1; j < parts.length; j++) {
          expect(overlap(parts[i], parts[j]), `${parts[i].name} overlaps ${parts[j].name}`).toBe(0);
        }
      }
      await expectNoHorizontalScroll(page);

      await expect(matchBar).toBeVisible();
      await matchBar.scrollIntoViewIfNeeded();
      await expect(matchBar).toBeInViewport({ ratio: 1 });
      const bar = await box(matchBar);
      const canvas = parts[1];
      expect(bar.x).toBeGreaterThanOrEqual(canvas.x);
      expect(bar.x + bar.width).toBeLessThanOrEqual(canvas.x + canvas.width);
    });

    test("compare with a report and profiles do not scroll sideways", async ({ page }) => {
      await connectPostgres(page, DB.tgt);
      await connectPostgres(page, DB.src);
      await page.goto("/compare");
      await page.getByTestId(sel.compare.target).selectOption({ label: pgConnectionLabel(DB.tgt) });
      await page.getByTestId(sel.compare.run).click();
      await expect(page.getByTestId(sel.compare.gauge)).toHaveCount(2);
      await expectNoHorizontalScroll(page);

      await page.goto("/profiles");
      await expect(page.getByTestId(sel.profiles.name)).toBeVisible();
      await page.getByTestId(sel.profiles.ignoreInput).fill("*_audit");
      await page.getByTestId(sel.profiles.ignoreAdd).click();
      await expect(page.getByTestId(sel.profiles.ignoreItem)).toHaveCount(1);
      await expectNoHorizontalScroll(page);
    });
  });
}
