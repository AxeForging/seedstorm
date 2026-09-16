// Clone schema from the workspace, with views, routines and triggers: the
// cloned objects must work on the target's own data, not just exist.
import { DB, pgQuery, withPg } from "../support/db.helpers";
import { createCloneTargetDatabase } from "../support/databases.fixture";
import { sel } from "../support/selectors";
import { connectPostgres, expect, pgConnectionLabel, test } from "../support/test.fixture";

const ws = sel.workspace;

test.beforeAll(createCloneTargetDatabase);

test("clone schema with objects produces working views, a function and a trigger", async ({ page }) => {
  // The target must be connected to be offered; the source is opened last so it is active.
  await connectPostgres(page, DB.clone);
  await connectPostgres(page, DB.src);
  await page.goto("/");
  await expect(page.getByTestId(ws.tableCount)).toHaveText("2");

  await page.getByTestId(ws.mode("clone")).click();
  const target = page.getByTestId(ws.cloneTarget);
  await expect(target).toBeVisible();
  await target.selectOption({ label: pgConnectionLabel(DB.clone) });
  await page.getByTestId(ws.cloneViews).check();
  await page.getByTestId(ws.cloneRoutines).check();
  await page.getByTestId(ws.cloneTriggers).check();
  await expect(page.getByTestId(ws.run)).toHaveText("Clone schema");
  await page.getByTestId(ws.run).click();

  await expect(page.getByTestId(sel.job.status)).toHaveText("done", { timeout: 60_000 });
  const stat = (label: string) => page.getByTestId(sel.job.stat(label)).getByTestId(sel.job.statValue);
  await expect(stat("tables")).toHaveText("2");
  // Two views, the SQL function, the trigger function and the trigger.
  await expect(stat("objects")).toHaveText("5");

  const objects = await pgQuery<{ kind: string; name: string }>(DB.clone, `
    SELECT 'view' AS kind, viewname AS name FROM pg_views WHERE schemaname = 'public'
    UNION ALL SELECT 'function', p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'public'
    UNION ALL SELECT 'trigger', tg.tgname FROM pg_trigger tg JOIN pg_class c ON c.oid = tg.tgrelid
      JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public' AND NOT tg.tgisinternal
    ORDER BY 1, 2`);
  expect(objects.map((o) => `${o.kind}:${o.name}`)).toEqual([
    "function:add_tax", "function:orders_default_status", "trigger:trg_orders_status", "view:a_big_spenders", "view:order_totals",
  ]);

  await withPg(DB.clone, async (c) => {
    // The clone copies structure only.
    expect((await c.query("SELECT count(*)::int AS n FROM orders")).rows[0].n).toBe(0);
    await c.query("INSERT INTO customers (id, name) VALUES (7, 'Ada')");
    await c.query("INSERT INTO orders (id, customer_id, amount) VALUES (70, 7, 150)");
    const { rows: [order] } = await c.query("SELECT status FROM orders WHERE id = 70");
    expect(order.status, "trigger fills a missing status").toBe("new");
    const { rows: [taxed] } = await c.query("SELECT add_tax(5) AS v");
    expect(taxed.v).toBe(15);
    // The view on a view reads the target's rows.
    const { rows: spenders } = await c.query("SELECT name, total FROM a_big_spenders");
    expect(spenders).toEqual([{ name: "Ada", total: 150 }]);
  });
});
