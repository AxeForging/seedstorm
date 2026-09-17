// Recommend reads the database's limits, asks for what SQL cannot see, and
// applies writers and generators with a reason for each.
import { DB } from "../support/db.helpers";
import { sel } from "../support/selectors";
import { connectPostgres, expect, openWorkspace, test } from "../support/test.fixture";

const ws = sel.workspace;
const tune = sel.tuning;

test("recommend writers for a small managed database and apply them", async ({ page }) => {
  await connectPostgres(page, DB.src);
  await openWorkspace(page, 2);
  await page.getByTestId(ws.tuningToggle).click();
  await page.getByTestId(tune.open).click();

  const dialog = page.getByTestId(tune.dialog);
  await expect(dialog).toBeVisible();
  await expect(dialog.getByTestId(tune.writers)).toBeVisible();
  await dialog.locator('input[name="vcpu"]').fill("1");
  await dialog.locator('input[name="memoryMB"]').fill("629");
  await dialog.locator('select[name="storage"]').selectOption("network-ssd");
  await dialog.locator('input[name="storageGB"]').fill("10");
  await dialog.locator('input[name="iops"]').fill("300");

  // 1 vCPU: two writers, one generator, and a reason naming the vCPU.
  await expect(dialog.getByTestId(tune.writers)).toHaveText("2");
  await expect(dialog.getByTestId(tune.generators)).toHaveText("1");
  await expect(dialog.getByTestId(tune.result)).toContainText("vCPU");
  await expect(dialog.getByTestId(tune.growth)).toContainText("GB");
  await expect(dialog.getByTestId(tune.result)).toContainText("depend on the database's load");

  await dialog.getByTestId(tune.apply).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByTestId(ws.workers)).toHaveValue("2");
  await expect(page.getByTestId(ws.tuningSummary)).toHaveText("2 writers");

  // The shape entered is remembered for this connection.
  await page.getByTestId(tune.open).click();
  await expect(page.getByTestId(tune.dialog).locator('input[name="vcpu"]')).toHaveValue("1");
  await page.getByTestId(tune.dialog).getByRole("button", { name: "Close" }).click();
});
