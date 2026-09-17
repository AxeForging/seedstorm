// Leaving a page and coming back keeps what the user set, reattaches to the
// job they started, and never remembers a destructive toggle.
import { DB } from "../support/db.helpers";
import { sel } from "../support/selectors";
import { connectPostgres, expect, openWorkspace, pgConnectionLabel, test } from "../support/test.fixture";

const ws = sel.workspace;
const cmp = sel.compare;

test("workspace and compare settings survive a trip to another page", async ({ page }) => {
  const created = await page.request.post("/api/profiles", {
    headers: { "Content-Type": "application/json" },
    data: { rules: { version: 1, name: "e2e-memory", rules: [{ column: "*name*", template: "N {{seq}}" }] } },
  });
  expect(created.ok()).toBe(true);
  const profileId = (await created.json()).id as string;
  try {
    await connectPostgres(page, DB.tgt);
    await connectPostgres(page, DB.src);
    await openWorkspace(page, 2);

    await page.getByTestId(ws.rows).fill("37");
    await page.getByTestId(ws.tuningToggle).click();
    await page.getByTestId(ws.workers).fill("3");
    await page.getByTestId(ws.genWorkers).fill("2");
    await page.getByTestId(ws.profile).selectOption(profileId);
    await page.locator("#cfg-truncate").check();

    await page.goto("/compare");
    await page.getByTestId(cmp.target).selectOption({ label: pgConnectionLabel(DB.tgt) });
    const chosenTarget = await page.getByTestId(cmp.target).inputValue();
    // Mirror settings sit next to a report: compare first.
    await page.getByTestId(cmp.run).click();
    await expect(page.getByTestId(cmp.results)).toBeVisible();
    await page.getByTestId(cmp.advancedToggle).click();
    await page.getByTestId(cmp.stopOnError).check();

    await page.goto("/profiles");
    await page.goto("/compare");
    await expect(page.getByTestId(cmp.target)).toHaveValue(chosenTarget);
    // The report for the same pair is restored, with the remembered settings.
    await expect(page.getByTestId(cmp.results)).toBeVisible();
    await page.getByTestId(cmp.advancedToggle).click();
    await expect(page.getByTestId(cmp.stopOnError)).toBeChecked();

    await openWorkspace(page, 2);
    await expect(page.getByTestId(ws.rows)).toHaveValue("37");
    await expect(page.getByTestId(ws.workers)).toHaveValue("3");
    await expect(page.getByTestId(ws.genWorkers)).toHaveValue("2");
    await expect(page.getByTestId(ws.tuningSummary)).toHaveText("3 writers · 2 gen");
    await expect(page.getByTestId(ws.profile)).toHaveValue(profileId);
    await expect(page.locator("#cfg-truncate"), "truncate is never remembered").not.toBeChecked();
  } finally {
    await page.request.delete(`/api/profiles?id=${profileId}`);
  }
});

test("a run started before leaving the workspace is shown when coming back", async ({ page }) => {
  test.setTimeout(120_000);
  await connectPostgres(page, DB.src);
  await openWorkspace(page, 2);
  // A dry run of a few million rows takes seconds and writes nothing.
  await page.getByTestId(ws.rows).fill("1500000");
  await page.locator("#cfg-dryrun").check();
  await page.getByTestId(ws.run).click();
  await expect(page.getByTestId(sel.runStrip.root).first()).toHaveAttribute("data-state", "running");
  await page.goto("/compare");
  await openWorkspace(page, 2);
  const strip = page.getByTestId(sel.runStrip.root).first();
  await expect(strip).toBeVisible();
  await expect(strip.getByTestId(sel.runStrip.status)).toHaveText(/Running|Still working|Done/);
  await expect(strip).toHaveAttribute("data-state", "done", { timeout: 90_000 });
  await expect(strip.getByTestId(sel.runStrip.status)).toHaveText("Done");
});
