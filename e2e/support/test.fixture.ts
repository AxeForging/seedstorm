// The test object every spec imports: baseURL points at the server started in
// global setup, plus helpers for opening connections and reading the graph.
import { test as base, expect, type Page } from "@playwright/test";
import { pg, pgDSN } from "./db.helpers";
import { sel } from "./selectors";

export const test = base.extend({
  baseURL: async ({}, use) => {
    const url = process.env.SEEDSTORM_E2E_URL;
    if (!url) throw new Error("SEEDSTORM_E2E_URL is not set: run through playwright.config.ts (global setup starts the server)");
    await use(url);
  },
});
export { expect };

// The label the pickers show for a connection opened by connectPostgres.
export function pgConnectionLabel(database: string): string {
  return `${database} @ ${pg.host}:${pg.port}`;
}

// connectPostgres opens (or reuses) a server-side connection to database and
// makes it the active one for this browser context, like submitting the
// connect form with a connection string.
export async function connectPostgres(page: Page, database: string): Promise<void> {
  const res = await page.request.post("/connect", {
    form: { dbType: "postgres", dsn: pgDSN(database), label: database, action: "connect" },
    maxRedirects: 0,
  });
  expect(res.status(), `connect to ${database}: ${res.status() === 200 ? await res.text() : ""}`.slice(0, 600)).toBe(303);
}

// openWorkspace loads / and waits until the graph is rendered and the initial
// fit animation has finished.
export async function openWorkspace(page: Page, tables: number): Promise<void> {
  await page.goto("/");
  await expect(page.getByTestId(sel.workspace.tableCount)).toHaveText(String(tables));
  await expect(page.getByTestId(sel.workspace.graphLoading)).toBeHidden();
  await waitForCamera(page);
}

// The graph is a canvas: zoom, pan and node positions exist only in cytoscape.
export interface Viewport {
  zoom: number;
  pan: { x: number; y: number };
  width: number;
  height: number;
}

export async function waitForCamera(page: Page): Promise<void> {
  await page.waitForFunction(() => {
    const cy = (window as any).seedstorm?.state?.cy;
    return !!cy && !cy.animated();
  });
}

export async function viewport(page: Page): Promise<Viewport> {
  await waitForCamera(page);
  return page.evaluate(() => {
    const cy = (window as any).seedstorm.state.cy;
    return { zoom: cy.zoom(), pan: { ...cy.pan() }, width: cy.width(), height: cy.height() };
  });
}

// nodeOnScreen returns where a node is drawn, relative to the canvas.
export async function nodeOnScreen(page: Page, id: string): Promise<{ x: number; y: number }> {
  await waitForCamera(page);
  return page.evaluate((nodeId) => {
    const p = (window as any).seedstorm.state.cy.getElementById(nodeId).renderedPosition();
    return { x: p.x, y: p.y };
  }, id);
}

export async function nodesWithClass(page: Page, cls: string): Promise<string[]> {
  return page.evaluate((c) => (window as any).seedstorm.state.cy.nodes("." + c).map((n: any) => n.id()).sort(), cls);
}

export async function visibleNodes(page: Page): Promise<string[]> {
  return page.evaluate(() => (window as any).seedstorm.state.cy.nodes().filter((n: any) => n.visible()).map((n: any) => n.id()).sort());
}

// Numbers as the UI prints them with toLocaleString.
export const fmt = (n: number) => n.toLocaleString("en-US");
