import { test, expect } from "@playwright/test";
import { rid } from "./helpers";

// Flow 4 — the v0 → v1 mount migration, proven against the real stack (not synthetically like
// the Go chi.Walk sweep). Two guarantees:
//   1. The dashboard's OWN traffic rides /api/v1 and carries NO deprecation headers — i.e. the
//      migration is complete, nothing still calls the deprecated alias.
//   2. /api/v0 is STILL a working, deprecation-marked alias — a deliberate v0 call so the
//      alias stays exercised until it is actually removed (an untested deprecated path breaks
//      silently).

test("dashboard traffic is all /api/v1 and carries no deprecation headers", async ({ page }) => {
  const apiCalls: string[] = [];
  const deprecated: string[] = [];
  page.on("response", (r) => {
    const path = new URL(r.url()).pathname;
    if (!path.startsWith("/api/")) return;
    apiCalls.push(`${r.request().method()} ${path}`);
    // header names are lower-cased by Playwright
    if (r.headers()["deprecation"] || r.headers()["sunset"]) {
      deprecated.push(`${r.request().method()} ${path}`);
    }
  });

  // A real participant flow that exercises the client, an authed read, and the board.
  const id = rid();
  await page.goto("/register");
  await page.getByLabel("Username").fill(`mig_${id}`);
  await page.getByLabel("Email").fill(`mig_${id}@example.com`);
  await page.getByLabel("Password").fill("supersecret1");
  await page.getByRole("button", { name: "Register" }).click();
  await expect(page).toHaveURL(/\/challenges/);
  await page.goto("/scoreboard");
  await expect(page.getByTestId("scoreboard-table")).toBeVisible();

  // The check must not be vacuous: the dashboard really did hit the API, and every /api call
  // went to v1 (never v0).
  expect(apiCalls.length, "the dashboard made no API calls — the assertion would be vacuous").toBeGreaterThan(0);
  const v0 = apiCalls.filter((c) => c.includes("/api/v0"));
  expect(v0, `dashboard still calls the deprecated /api/v0 alias: ${v0.join(", ")}`).toEqual([]);
  expect(deprecated, `dashboard traffic carried deprecation headers (should be v1-only): ${deprecated.join(", ")}`).toEqual([]);
});

test("the /api/v0 alias still serves and is marked deprecated", async ({ request }) => {
  // Deliberate v0 call: the alias must keep working (identical handler) AND advertise its
  // deprecation, until it is actually removed (>= v0.4).
  const res = await request.get("/api/v0/event");
  expect(res.status(), await res.text()).toBe(200);
  expect(res.headers()["deprecation"]).toBe("true");
  expect(res.headers()["sunset"]).toBeTruthy();

  // And the canonical surface serves the same thing WITHOUT the deprecation headers.
  const v1 = await request.get("/api/v1/event");
  expect(v1.status()).toBe(200);
  expect(v1.headers()["deprecation"]).toBeFalsy();
  expect(v1.headers()["sunset"]).toBeFalsy();
});
