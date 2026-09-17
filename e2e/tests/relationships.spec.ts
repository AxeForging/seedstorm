// Relationship shapes: measure children per parent on the workspace, carry
// them in a counts file, and compare them between two databases.
import { DB } from "../support/db.helpers";
import { sel } from "../support/selectors";
import { connectPostgres, expect, openWorkspace, pgConnectionLabel, test } from "../support/test.fixture";

const rel = sel.relationships;
const snap = sel.snapshot;
const cmp = sel.compare;

// orders.customer_id: 3,400 orders over 1,200 customers, dealt round robin →
// 1,000 customers with 3 orders and 200 with 2 (avg 2.83, p95 3, max 3). The
// key has no index on Postgres, so it is only scanned when asked.
test("analyze relationships, export them and compare them with another database", async ({ page }) => {
  await connectPostgres(page, DB.tgt);
  await connectPostgres(page, DB.src);
  await openWorkspace(page, 2);

  await test.step("an unindexed key is estimated unless included", async () => {
    await page.getByTestId(rel.open).click();
    await page.getByTestId(rel.dialog).getByTestId(rel.start).click();
    await expect(page.getByTestId(rel.status)).toContainText(/1 relationship measured · 1 estimated/, { timeout: 30_000 });
  });

  await test.step("an exact scan measures it", async () => {
    await page.getByTestId(rel.open).click();
    const dialog = page.getByTestId(rel.dialog);
    await dialog.getByTestId(rel.unindexed).check();
    await dialog.getByTestId(rel.start).click();
    await expect(page.getByTestId(rel.status)).toHaveText("1 relationship measured", { timeout: 30_000 });
  });

  await test.step("the counts file includes them only when asked", async () => {
    await page.getByTestId(snap.open).click();
    const dialog = page.getByTestId(snap.dialog);
    const preview = dialog.getByTestId(snap.preview);
    await expect(preview).toContainText("kind: seedstorm.table-counts", { timeout: 30_000 });
    await expect(preview).not.toContainText("relationships:");
    await dialog.getByTestId(snap.relationships).check();
    await expect(preview).toContainText("relationships:");
    await expect(preview).toContainText(/child: orders[\s\S]*max: 3/);
    await dialog.getByRole("button", { name: "Close" }).click();
  });

  await test.step("compare shows the drift and exports it", async () => {
    await page.goto("/compare");
    await page.getByTestId(cmp.source).selectOption({ label: pgConnectionLabel(DB.src) });
    await page.getByTestId(cmp.target).selectOption({ label: pgConnectionLabel(DB.tgt) });
    await page.getByTestId(cmp.run).click();
    await expect(page.getByTestId(cmp.results)).toBeVisible({ timeout: 30_000 });
    await expect(page.getByTestId(cmp.gauge).first()).toBeVisible();

    const section = page.getByTestId(rel.section);
    await section.getByTestId(rel.compareUnindexed).check();
    await section.getByTestId(rel.compareRun).click();
    const row = section.getByTestId(rel.row).filter({ hasText: "orders.customer_id" });
    await expect(row).toContainText("2.83 / 3 / 3 · 0%", { timeout: 30_000 });
    await expect(row).toHaveAttribute("data-status", "differs");
    await expect(section.getByTestId(rel.summary)).toContainText("1 relationship · 0 same · 1 differ");

    await page.getByTestId(cmp.exportOpen).click();
    const dialog = page.getByTestId(cmp.exportDialog);
    await expect(dialog.getByTestId(cmp.exportPreview)).toContainText("kind: seedstorm.table-counts");
    await expect(dialog.getByTestId(cmp.exportPreview)).not.toContainText("relationships:");
    await dialog.getByTestId(rel.exportInclude).check();
    await expect(dialog.getByTestId(cmp.exportPreview)).toContainText(/relationships:[\s\S]*max: 3/);
  });
});
