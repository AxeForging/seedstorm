// Compare two Postgres databases, round-trip the counts through Export and
// Import, and mirror the target from the imported counts file.
import fs from "node:fs";
import { DB, pgCounts, withPg } from "../support/db.helpers";
import { SOURCE_ROWS } from "../support/databases.fixture";
import { sel } from "../support/selectors";
import { connectPostgres, expect, fmt, pgConnectionLabel, test } from "../support/test.fixture";

const cmp = sel.compare;

test.beforeAll(async () => {
  // Mirror writes to the target: start every run from an empty one.
  await withPg(DB.tgt, (c) => c.query("TRUNCATE orders, customers RESTART IDENTITY"));
});

const SEED_PROFILE_YAML = `name: loadtest
rules:
  - column: "*email*"
    template: "lt+{{seq}}@example.test"
tables:
  customers:
    rows: 13
`;

test("compare, export and import counts, then mirror from the imported file", async ({ page }, testInfo) => {
  await connectPostgres(page, DB.tgt);
  await connectPostgres(page, DB.src);
  await page.goto("/compare");

  const source = page.getByTestId(cmp.source);
  const compareButton = page.getByTestId(cmp.run);
  const gauge = (table: string) => page.getByTestId(cmp.gauge).filter({ has: page.getByTestId(cmp.gaugeTable).getByText(table, { exact: true }) });
  const expectReport = async (target: { customers: number; orders: number }) => {
    await expect(page.getByTestId(cmp.results)).toBeVisible();
    await expect(page.getByTestId(cmp.gauge)).toHaveCount(2);
    for (const table of ["customers", "orders"] as const) {
      await expect(gauge(table).getByTestId(cmp.gaugeSourceRows)).toHaveText(fmt(SOURCE_ROWS[table]));
      await expect(gauge(table).getByTestId(cmp.gaugeTargetRows)).toHaveText(fmt(target[table]));
    }
  };

  await test.step("compare source and target", async () => {
    await expect(source.locator("option:checked")).toHaveText(pgConnectionLabel(DB.src));
    await page.getByTestId(cmp.target).selectOption({ label: pgConnectionLabel(DB.tgt) });
    await compareButton.click();
    await expectReport({ customers: 0, orders: 0 });
    await expect(page.getByTestId(cmp.stats)).toContainText(`${fmt(SOURCE_ROWS.customers + SOURCE_ROWS.orders)} → 0`);
  });

  await test.step("the Advanced checkbox is a normal checkbox", async () => {
    await page.getByTestId(cmp.advancedToggle).click();
    const stop = page.getByTestId(cmp.stopOnError);
    await expect(stop).toBeVisible();
    const box = (await stop.boundingBox())!;
    expect(box.width).toBeLessThanOrEqual(20);
    expect(box.height).toBeLessThanOrEqual(20);
  });

  const countsFile = testInfo.outputPath("source-counts.yaml");
  await test.step("export the source counts as a YAML download", async () => {
    await page.getByTestId(cmp.exportOpen).click();
    const dialog = page.getByTestId(cmp.exportDialog);
    await expect(dialog.getByTestId(cmp.exportPreview)).toContainText("kind: seedstorm.table-counts");
    const [download] = await Promise.all([page.waitForEvent("download"), dialog.getByTestId(cmp.exportDownload).click()]);
    expect(download.suggestedFilename()).toMatch(/\.ya?ml$/);
    await download.saveAs(countsFile);
    const yaml = fs.readFileSync(countsFile, "utf8");
    expect(yaml).toMatch(new RegExp(`customers:\\s*\\n\\s+rows: ${SOURCE_ROWS.customers}\\b`));
    expect(yaml).toMatch(new RegExp(`orders:\\s*\\n\\s+rows: ${SOURCE_ROWS.orders}\\b`));
    await dialog.getByTestId(cmp.exportClose).click();
    await expect(dialog).toBeHidden();
  });

  await test.step("import that file as the source", async () => {
    await page.getByTestId(cmp.importOpen).click();
    await page.getByTestId(cmp.importFile).setInputFiles(countsFile);
    await expect(page.getByTestId(cmp.importDialog)).toBeHidden();
    await expect(source).toHaveValue(/^snap:/);
    const chosen = source.locator("option:checked");
    await expect(chosen).toContainText("2 tables, imported");
    expect(await chosen.evaluate((o) => (o.parentElement as HTMLOptGroupElement).label)).toBe("Imported counts");
    await expect(compareButton).toHaveText("Compare");
    await expectReport({ customers: 0, orders: 0 });
  });

  await test.step("the report survives leaving the page", async () => {
    const url = page.url();
    expect(url).toContain("source=snap%3A");
    await page.goto("/profiles");
    await expect(page.getByTestId(sel.profiles.app)).toBeVisible();
    await page.goto(url);
    await expect(source).toHaveValue(/^snap:/);
    await expectReport({ customers: 0, orders: 0 });
  });

  await test.step("a seed profile pasted as counts is refused with a readable error", async () => {
    await page.getByTestId(cmp.importOpen).click();
    const dialog = page.getByTestId(cmp.importDialog);
    await dialog.getByTestId(cmp.importText).fill(SEED_PROFILE_YAML);
    await dialog.getByTestId(cmp.importUse).click();
    await expect(dialog.getByTestId(cmp.importStatus)).toContainText("looks like a seed profile, not a table-counts snapshot");
    await dialog.getByTestId(cmp.importClose).click();
    await expect(dialog).toBeHidden();
    await expect(source).toHaveValue(/^snap:/);
  });

  await test.step("mirror from the imported counts fills the target to match", async () => {
    const total = SOURCE_ROWS.customers + SOURCE_ROWS.orders;
    await page.getByTestId(cmp.plan).click();
    const plan = page.getByTestId(cmp.planModal);
    await expect(plan).toBeVisible();
    await expect(plan.getByTestId(cmp.planTitle)).toHaveText(`Insert ${fmt(total)} rows into 2 tables`);
    await expect(plan.getByTestId(cmp.planImportedSource)).toContainText("Source is an imported counts file");
    await expect(plan.getByTestId(cmp.planRow)).toHaveCount(2);
    await expect(plan.getByTestId(cmp.execute)).toHaveText(`Run mirror · ${fmt(total)} rows`);
    await plan.getByTestId(cmp.execute).click();
    await expect(page.getByTestId(cmp.outcome)).toContainText(`Inserted ${fmt(total)} rows`, { timeout: 60_000 });
    expect(await pgCounts(DB.tgt)).toEqual(SOURCE_ROWS);
    // The report refreshes from the target after the run.
    await expectReport(SOURCE_ROWS);
  });
});

// A job that fails on the server must end in the page: the button comes back
// and the reason is shown. The failure event used to be named "error", which
// browsers treat as a broken connection, so the page waited forever.
test("a compare that fails on the server ends with its reason instead of spinning", async ({ page }) => {
  await connectPostgres(page, DB.tgt);
  await connectPostgres(page, DB.src);
  await page.goto("/compare");
  await page.getByTestId(cmp.target).selectOption({ label: pgConnectionLabel(DB.tgt) });

  // The target disappears between choosing it and running the job.
  await page.route("**/api/compare", async (route) => {
    const body = route.request().postDataJSON();
    body.target = { id: "gone-" + Date.now() };
    await route.continue({ postData: JSON.stringify(body) });
  });

  const run = page.getByTestId(cmp.run);
  await run.click();
  await expect(page.getByTestId(cmp.outcome)).toContainText("Compare failed", { timeout: 15_000 });
  await expect(page.getByTestId(cmp.outcome)).toContainText("target connection not found");
  await expect(run).toBeEnabled();
  await expect(run).toHaveText("Compare");
});
