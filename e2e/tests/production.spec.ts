// A saved connection marked production is never written from the UI until its
// label is typed back: cancelling writes nothing, typing it runs the seed.
import { DB, pg, pgCounts, withPg } from "../support/db.helpers";
import { sel } from "../support/selectors";
import { connectPostgres, expect, openWorkspace, test } from "../support/test.fixture";

const LABEL = "e2e-orders-prod";
const prod = sel.production;

test("seeding a production connection asks for its label first", async ({ page }) => {
  await withPg(DB.tgt, (c) => c.query("TRUNCATE orders, customers RESTART IDENTITY"));
  const saved = await page.request.post("/api/saved-connections", {
    headers: { "X-Seedstorm-Request": "1" },
    data: { label: LABEL, dbType: "postgres", host: pg.host, port: pg.port, dbName: DB.tgt, user: pg.user, password: pg.password, production: true },
  });
  expect(saved.ok()).toBe(true);
  const savedId = (await saved.json()).id as string;
  test.info().attach("saved connection", { body: savedId });

  try {
    await test.step("the saved list shows the production badge", async () => {
      await page.goto("/connect?mode=chooser");
      await expect(page.getByTestId(prod.badge).first()).toBeVisible();
    });

    // Connected ad hoc (a DSN, not the saved entry): it is still recognised.
    await connectPostgres(page, DB.tgt);
    await openWorkspace(page, 2);
    await page.getByTestId(sel.workspace.rows).fill("3");

    await test.step("cancelling the confirmation writes nothing", async () => {
      await page.getByTestId(sel.workspace.run).click();
      const dialog = page.getByTestId(prod.dialog);
      await expect(dialog).toBeVisible();
      await expect(dialog.getByTestId(prod.ok)).toBeDisabled();
      await dialog.getByRole("button", { name: "Cancel" }).click();
      await expect(dialog).toBeHidden();
      await expect(page.locator(".job-phase-log").last()).toContainText("Not written");
      expect(await pgCounts(DB.tgt, ["customers", "orders"])).toEqual({ customers: 0, orders: 0 });
    });

    await test.step("typing the label runs the seed", async () => {
      await page.getByTestId(sel.workspace.run).click();
      const dialog = page.getByTestId(prod.dialog);
      await dialog.getByTestId(prod.input).fill("e2e-orders");
      await expect(dialog.getByTestId(prod.ok)).toBeDisabled();
      await dialog.getByTestId(prod.input).fill(LABEL);
      await dialog.getByTestId(prod.ok).click();
      await expect(page.getByTestId(sel.job.status)).toHaveText("done", { timeout: 60_000 });
      expect(await pgCounts(DB.tgt, ["customers", "orders"])).toEqual({ customers: 3, orders: 3 });
    });
  } finally {
    await page.request.delete(`/api/saved-connections?id=${savedId}`, { headers: { "X-Seedstorm-Request": "1" } });
    await withPg(DB.tgt, (c) => c.query("TRUNCATE orders, customers RESTART IDENTITY"));
  }
});
