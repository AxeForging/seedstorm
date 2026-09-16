// A seed profile with ignored tables: live matches while editing, saved to
// disk, reflected in the workspace, and carried by the exported YAML.
import fs from "node:fs";
import path from "node:path";
import { DB } from "../support/db.helpers";
import { sel } from "../support/selectors";
import { wideTableName } from "../support/wide-schema.fixture";
import { connectPostgres, expect, nodesWithClass, openWorkspace, test } from "../support/test.fixture";

const PROFILE = `e2e-ignore-${Date.now().toString(36)}`;
const GLOB = "*_booking";
const BOOKINGS = Array.from({ length: 150 }, (_, i) => wideTableName(i)).filter((t) => t.endsWith("_booking")).sort();
const pf = sel.profiles;
const ws = sel.workspace;
const IGNORE_YAML = /ignore:\s*\n\s*- "?\*_booking"?/;

function profilesFile(): string {
  const state = JSON.parse(fs.readFileSync(process.env.SEEDSTORM_E2E_STATE!, "utf8")) as { tmpDir: string };
  return path.join(state.tmpDir, "profiles.yaml");
}

test("ignored tables: live matches, saved profile, greyed in the workspace, exported YAML", async ({ page }, testInfo) => {
  expect(BOOKINGS.length, "fixture: more matches than the list names").toBeGreaterThan(8);
  await connectPostgres(page, DB.wide);
  await page.goto("/profiles");

  await test.step("an ignore glob lists the tables it matches as you add it", async () => {
    await page.getByTestId(pf.name).fill(PROFILE);
    await page.getByTestId(pf.ignoreInput).fill(GLOB);
    await page.getByTestId(pf.ignoreAdd).click();
    const item = page.getByTestId(pf.ignoreItem);
    await expect(item).toHaveCount(1);
    await expect(item.getByTestId(pf.ignoreGlob)).toHaveText(GLOB);
    // The first eight matches are named, the rest counted.
    await expect(item.getByTestId(pf.ignoreHit)).toHaveCount(8);
    await expect(item.getByTestId(pf.ignoreHits)).toContainText(`+${BOOKINGS.length - 8}`);
    for (const name of await item.getByTestId(pf.ignoreHit).allTextContents()) expect(BOOKINGS).toContain(name);
    await expect(page.getByTestId(pf.status)).toHaveText("Not saved yet");
  });

  let profileUrl = "";
  await test.step("saving writes the profile file", async () => {
    await page.getByTestId(pf.save).click();
    await expect(page.getByTestId(pf.status)).toContainText(`seedstorm seed --profile ${PROFILE}`);
    await expect(page).toHaveURL(/[?&]id=/);
    profileUrl = page.url();
    const saved = fs.readFileSync(profilesFile(), "utf8");
    expect(saved).toContain(PROFILE);
    expect(saved).toMatch(IGNORE_YAML);
  });

  await test.step("the workspace lists and greys the ignored tables", async () => {
    await openWorkspace(page, 150);
    await page.getByTestId(ws.profile).selectOption({ label: PROFILE });
    const tab = page.getByTestId(ws.tabIgnored);
    await expect(tab).toBeVisible();
    await expect(tab).toHaveText(`Ignored ${BOOKINGS.length}`);
    await tab.click();
    const rows = page.getByTestId(ws.ignoredRow);
    await expect(rows).toHaveCount(BOOKINGS.length);
    expect((await rows.getByTestId(ws.ignoredTable).allTextContents()).sort()).toEqual(BOOKINGS);
    await expect(rows.getByTestId(ws.ignoredPattern)).toHaveText(BOOKINGS.map(() => GLOB));
    await expect.poll(() => nodesWithClass(page, "ignored")).toEqual(BOOKINGS);
    await expect(page.getByTestId(ws.scope)).toContainText(`${BOOKINGS.length} ignored`);
  });

  await test.step("the exported YAML carries the ignore list", async () => {
    await page.goto(profileUrl);
    await expect(page.getByTestId(pf.name)).toHaveValue(PROFILE);
    await page.getByTestId(pf.exportOpen).click();
    const link = page.getByTestId(pf.yamlDownload);
    await expect(link).toBeVisible();
    const [file] = await Promise.all([page.waitForEvent("download"), link.click()]);
    const out = testInfo.outputPath(file.suggestedFilename());
    await file.saveAs(out);
    const yaml = fs.readFileSync(out, "utf8");
    expect(yaml).toContain(`name: ${PROFILE}`);
    expect(yaml).toMatch(IGNORE_YAML);
  });
});
