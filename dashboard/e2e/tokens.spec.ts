import { test, expect } from "@playwright/test";
import { rid } from "./helpers";

// API tokens through the UI: create -> the plaintext is shown ONCE -> it authenticates a real
// request -> revoke -> it stops working. The last two steps are what make this more than a
// rendering test: the token the UI showed has to actually work, and revoking has to actually
// take effect.
test("create, use, and revoke an API token", async ({ page, request }) => {
  const id = rid();
  const email = `tok-${id}@example.test`;
  const password = "devpassword123";

  await page.goto("/register");
  await page.getByLabel("Username").fill(`tok${id}`);
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Register" }).click();
  await expect(page).toHaveURL(/\/challenges/);

  await page.goto("/profile");
  await page.getByLabel("Name").fill(`e2e-${id}`);
  await page.getByRole("button", { name: "Create token" }).click();

  // Shown once, in full. If this ever renders a truncated or masked value the token is useless.
  const secret = page.locator("code").first();
  await expect(secret).toBeVisible();
  const plaintext = (await secret.innerText()).trim();
  expect(plaintext.length).toBeGreaterThan(20);

  // The token authenticates a real request with no cookie — a plain API context carries none.
  const authed = await request.get("/api/v1/auth/me", {
    headers: { Authorization: `Bearer ${plaintext}` },
  });
  expect(authed.status()).toBe(200);
  expect((await authed.json()).email).toBe(email);

  // It appears in the list by prefix, and never in full.
  await expect(page.getByText(`e2e-${id}`)).toBeVisible();
  await expect(page.locator("li", { hasText: `e2e-${id}` })).not.toContainText(plaintext);

  // Revoking takes effect immediately.
  await page
    .locator("li", { hasText: `e2e-${id}` })
    .getByRole("button", { name: "Revoke" })
    .click();
  await expect(page.locator("li", { hasText: `e2e-${id}` })).toHaveCount(0);

  const revoked = await request.get("/api/v1/auth/me", {
    headers: { Authorization: `Bearer ${plaintext}` },
  });
  expect(revoked.status()).toBe(401);
});
