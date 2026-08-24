import { test, expect } from "@playwright/test";
import { runAdminCreds } from "./helpers";

// The plugin surfaces, against a REAL loaded plugin (the e2e image stage ships one stub auth
// provider). This is the only place the whole chain is visible the way an operator sees it:
// the loader discovers and launches a plugin binary, the registrar publishes it, the admin page
// reports it healthy, and the login page grows a button for it.
//
// Everything below the browser is covered by integration tests; what this adds is that the
// production image actually loads a plugin and the dashboard reflects it.

test("admin sees the loaded plugin and can reload it", async ({ page }) => {
  const admin = runAdminCreds();
  await page.goto("/login");
  await page.getByLabel("Email").fill(admin.email);
  await page.getByLabel("Password").fill(admin.password);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page).toHaveURL(/\/challenges/);

  await page.goto("/admin/plugins");
  const row = page.locator("tr", { hasText: "example-oidc" });
  await expect(row).toBeVisible();
  await expect(row).toContainText("auth");
  // The plugin must actually be serving, not merely discovered — a quarantined or failed plugin
  // would still appear in this table, which is the point of showing state.
  await expect(row).toContainText("ready", { timeout: 30_000 });

  // Reload swaps in a new instance; the row must come back to ready rather than disappearing.
  await row.getByRole("button", { name: "Reload" }).click();
  await expect(row).toContainText("ready", { timeout: 30_000 });
});

test("the login page offers the loaded provider", async ({ page }) => {
  await page.goto("/login");
  // Rendered from GET /auth/providers, so this only appears because the plugin registered.
  await expect(page.getByRole("link", { name: /Continue with example-oidc/i })).toBeVisible();
  // The built-in stays available alongside it — a plugin adds a login, it never replaces one.
  await expect(page.getByRole("button", { name: "Log in" })).toBeVisible();
});
