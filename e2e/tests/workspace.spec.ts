// Workspace graph navigation on the 150-table schema: packed layout, search
// with the match bar, "Only matches", and the Navigator minimap.
import { DB } from "../support/db.helpers";
import { sel } from "../support/selectors";
import {
  connectPostgres, expect, nodeOnScreen, nodesWithClass, openWorkspace, test, viewport, visibleNodes, waitForCamera,
} from "../support/test.fixture";

const READABLE_ZOOM = 0.72;
const ws = sel.workspace;

test.beforeEach(async ({ page }) => {
  await connectPostgres(page, DB.wide);
  await openWorkspace(page, 150);
});

test("a 150-table schema opens at a usable zoom and search walks the matches", async ({ page }) => {
  const graph = await (await page.request.get("/api/graph")).json() as { nodes: { id: string }[]; edges: { source: string; target: string }[] };
  const bookings = graph.nodes.map((n) => n.id).filter((id) => id.includes("booking")).sort();
  expect(bookings.length).toBeGreaterThan(2);

  await test.step("graph renders every table with the packed layout", async () => {
    expect(await page.evaluate(() => (window as any).seedstorm.state.cy.nodes().length)).toBe(150);
    // Plain dagre fits this schema at ~0.15; level packing keeps it readable.
    expect((await viewport(page)).zoom).toBeGreaterThanOrEqual(0.3);
  });

  const matchBar = page.getByTestId(ws.matchBar);
  const label = page.getByTestId(ws.matchLabel);

  async function expectCurrentMatchCentered(index: number) {
    await expect(label).toHaveText(`${index + 1} of ${bookings.length} matches`);
    const current = await nodesWithClass(page, "search-current");
    expect(current).toHaveLength(1);
    expect(bookings).toContain(current[0]);
    const vp = await viewport(page);
    expect(vp.zoom).toBeGreaterThanOrEqual(READABLE_ZOOM);
    const at = await nodeOnScreen(page, current[0]);
    expect(Math.abs(at.x - vp.width / 2)).toBeLessThan(4);
    expect(Math.abs(at.y - vp.height / 2)).toBeLessThan(4);
    return current[0];
  }

  let first = "";
  await test.step("typing shows the match bar and centres the best match readably", async () => {
    await page.getByTestId(ws.search).fill("booking");
    await expect(page.getByTestId(ws.searchCount)).toHaveText(`1 of ${bookings.length}`);
    await expect(matchBar).toBeVisible();
    await expect(matchBar).toBeInViewport({ ratio: 1 });
    first = await expectCurrentMatchCentered(0);
  });

  await test.step("› and ‹ step through the matches", async () => {
    await page.getByTestId(ws.matchNext).click();
    const second = await expectCurrentMatchCentered(1);
    expect(second).not.toBe(first);
    await page.getByTestId(ws.matchPrev).click();
    expect(await expectCurrentMatchCentered(0)).toBe(first);
    await page.getByTestId(ws.matchNext).click();
    expect(await expectCurrentMatchCentered(1)).toBe(second);
  });

  const positions = () => page.evaluate(() => {
    const out: Record<string, { x: number; y: number }> = {};
    (window as any).seedstorm.state.cy.nodes().forEach((n: any) => { out[n.id()] = { ...n.position() }; });
    return out;
  });
  const fullLayout = await positions();
  const only = page.getByTestId(ws.matchOnly);

  await test.step("Only matches keeps the matches and the tables they link to", async () => {
    await expect(only).toHaveText("Only matches");
    await only.click();
    await expect(only).toHaveText("Show full graph");
    await expect(only).toHaveAttribute("aria-pressed", "true");
    const keep = new Set(bookings);
    for (const e of graph.edges) {
      if (bookings.includes(e.source)) keep.add(e.target);
      if (bookings.includes(e.target)) keep.add(e.source);
    }
    expect(keep.size).toBeLessThan(150);
    await expect.poll(() => visibleNodes(page)).toEqual([...keep].sort());
  });

  await test.step("Show full graph restores every table at its original position", async () => {
    await only.click();
    await expect(only).toHaveText("Only matches");
    await expect(only).toHaveAttribute("aria-pressed", "false");
    await expect.poll(() => visibleNodes(page)).toHaveLength(150);
    expect(await positions()).toEqual(fullLayout);
  });
});

test("Navigator clamps the zoom to a readable level and the minimap pans", async ({ page }) => {
  const navigator = page.getByTestId(ws.navigator);
  const minimap = page.getByTestId(ws.minimapCanvas);
  await expect(navigator).toHaveAttribute("aria-pressed", "false");
  await expect(minimap).toBeHidden();
  expect((await viewport(page)).zoom).toBeLessThan(READABLE_ZOOM);

  await navigator.click();
  await expect(navigator).toHaveAttribute("aria-pressed", "true");
  await expect(minimap).toBeVisible();
  await waitForCamera(page);
  expect((await viewport(page)).zoom).toBeGreaterThanOrEqual(READABLE_ZOOM);
  // The minimap prints the zoom it keeps.
  const zoomLabel = page.getByTestId(ws.minimapZoom);
  await expect(zoomLabel).toHaveText(/^\d+%$/);
  expect(parseInt(await zoomLabel.innerText(), 10)).toBeGreaterThanOrEqual(72);

  // Clicking a side of the map moves the view towards that side of the graph.
  const box = (await minimap.boundingBox())!;
  const centreX = async () => {
    const vp = await viewport(page);
    return (vp.width / 2 - vp.pan.x) / vp.zoom;
  };
  await minimap.click({ position: { x: box.width * 0.1, y: box.height / 2 } });
  const left = await centreX();
  await minimap.click({ position: { x: box.width * 0.9, y: box.height / 2 } });
  const right = await centreX();
  expect(right - left).toBeGreaterThan(500);
  // The map pans; the zoom stays where Navigator put it.
  expect((await viewport(page)).zoom).toBeGreaterThanOrEqual(READABLE_ZOOM);
});
