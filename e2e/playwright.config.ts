import { defineConfig, devices } from "@playwright/test";

const CI = !!process.env.CI;

export default defineConfig({
  testDir: "./tests",
  globalSetup: "./support/global.setup.ts",
  globalTeardown: "./support/global.teardown.ts",
  // One shared server and database set: journeys run one at a time.
  workers: 1,
  fullyParallel: false,
  forbidOnly: CI,
  retries: CI ? 1 : 0,
  timeout: 90_000,
  expect: { timeout: 15_000 },
  reporter: CI ? [["list"], ["html", { open: "never" }]] : [["list"]],
  use: {
    headless: true,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    locale: "en-US",
    testIdAttribute: "data-testid",
    viewport: { width: 1440, height: 900 },
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 900 } } }],
});
