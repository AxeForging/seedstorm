// A MySQL user that may only SELECT: the header badge, the workspace banner and
// the Seed mode all warn before a run the server would refuse.
import { DB, MYSQL_READER, my } from "../support/db.helpers";
import { sel } from "../support/selectors";
import { expect, test } from "../support/test.fixture";

const ws = sel.workspace;

test("a read-only MySQL user is flagged in the header, the workspace and the Seed mode", async ({ page }) => {
  await test.step("connect through the form", async () => {
    await page.goto("/connect?mode=form");
    const c = sel.connect;
    await page.getByTestId(c.driver).selectOption("mysql");
    await page.getByTestId(c.host).fill(my.host);
    await page.getByTestId(c.port).fill(String(my.port));
    await page.getByTestId(c.database).fill(DB.mysqlReadOnly);
    await page.getByTestId(c.user).fill(MYSQL_READER.user);
    await page.getByTestId(c.password).fill(MYSQL_READER.password);
    await page.getByTestId(c.connect).click();
    await expect(page).toHaveURL(/\/$/);
    await expect(page.getByTestId(ws.tableCount)).toHaveText("2");
  });

  await test.step("the header badge says read-only", async () => {
    const badge = page.getByTestId(sel.header.accessBadge);
    await expect(badge).toHaveText("read-only");
    await expect(badge).toHaveAttribute("title", /no INSERT on 2 tables/);
  });

  await test.step("the workspace banner lists the tables without INSERT", async () => {
    const banner = page.getByTestId(ws.access);
    await expect(banner).toContainText("Read-only here: seeding will be refused");
    await expect(banner).toContainText(`${MYSQL_READER.user}@`);
    await expect(banner).toContainText("no INSERT on 2 tables");
    await banner.getByText("Details").click();
    await expect(banner.getByText("No INSERT:").locator("..")).toHaveText(/No INSERT:\s*parcels\s+warehouses/);
  });

  await test.step("Seed carries a warning chip; Generate, which never writes, does not", async () => {
    const seedWarning = page.getByTestId(ws.modeWarning("seed"));
    await expect(seedWarning).toBeVisible();
    await expect(seedWarning).toHaveAttribute("title", /No INSERT on 2 tables in scope/);
    await expect(page.getByTestId(ws.modeWarning("generate"))).toBeHidden();
    await expect(page.getByTestId(ws.runNote)).toHaveText(/^No INSERT on 2 tables in scope/);
  });
});
