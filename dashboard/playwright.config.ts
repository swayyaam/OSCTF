import { defineConfig, devices } from "@playwright/test";

// E2E runs against a composed stack already up on :8080 (see CI's e2e job).
// The 3 golden flows live in e2e/ (docs/v0.1/11-testing-ci.md).
export default defineConfig({
  testDir: "./e2e",
  // Provision a unique admin per run (see e2e/global-setup.ts) so the suite does
  // not share the one seeded admin against the 5-per-5-min login limit.
  globalSetup: "./e2e/global-setup.ts",
  timeout: 30_000,
  fullyParallel: false,
  // The flows mutate one shared global event (window, freeze), so they must not
  // run concurrently against the same backend. Retries are off: a retry re-runs
  // the admin login + registrations, which would burn the per-account/per-IP rate
  // limits and cascade into the other flows.
  workers: 1,
  retries: 0,
  reporter: "list",
  use: {
    baseURL: process.env.BASE_URL ?? "http://localhost:8080",
    trace: "on-first-retry",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
