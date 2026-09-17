// Counts from one connection: snapshot them from the workspace, export the
// file, and hand it to Compare as a source to calibrate another database.
import { DB } from "../support/db.helpers";
import { SOURCE_ROWS } from "../support/databases.fixture";
import { sel } from "../support/selectors";
import { connectPostgres, expect, fmt, openWorkspace, pgConnectionLabel, test } from "../support/test.fixture";

const snap = sel.snapshot;
const cmp = sel.compare;

test("snapshot counts from the workspace and compare another database against them", async ({ page }) => {
  await connectPostgres(page, DB.tgt);
  await connectPostgres(page, DB.src);
  await openWorkspace(page, 2);

  await page.getByTestId(snap.open).click();
  const dialog = page.getByTestId(snap.dialog);
  await expect(dialog).toBeVisible({ timeout: 30_000 });
  await expect(dialog.getByTestId(snap.preview)).toContainText("kind: seedstorm.table-counts");
  await expect(dialog.getByTestId(snap.preview)).toContainText(new RegExp(`customers:\\s*\\n\\s+rows: ${SOURCE_ROWS.customers}\\b`));
  const [download] = await Promise.all([page.waitForEvent("download"), dialog.getByTestId(snap.download).click()]);
  expect(download.suggestedFilename()).toMatch(/-counts\.yaml$/);

  await dialog.getByTestId(snap.compare).click();
  await expect(page).toHaveURL(/\/compare\?source=snap%3A/);
  await expect(page.getByTestId(cmp.source)).toHaveValue(/^snap:/);
  await page.getByTestId(cmp.target).selectOption({ label: pgConnectionLabel(DB.tgt) });
  await page.getByTestId(cmp.run).click();
  const gauge = page.getByTestId(cmp.gauge).filter({ has: page.getByTestId(cmp.gaugeTable).getByText("customers", { exact: true }) });
  await expect(gauge.getByTestId(cmp.gaugeSourceRows)).toHaveText(fmt(SOURCE_ROWS.customers));

  await test.step("calibrate from a file opens the import with this database as target", async () => {
    await openWorkspace(page, 2);
    await page.getByTestId(snap.calibrate).click();
    await expect(page.getByTestId(cmp.importDialog)).toBeVisible();
    await expect(page.getByTestId(cmp.target).locator("option:checked")).toHaveText(pgConnectionLabel(DB.src));
  });
});
