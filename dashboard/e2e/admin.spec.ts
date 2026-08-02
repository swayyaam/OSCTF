import { test, expect } from "@playwright/test";
import { rid, runAdminCreds, setEventWindow } from "./helpers";

// Flow 2 — admin challenge lifecycle: log in as admin -> create a visible standard
// challenge -> it appears on the participant board -> edit points -> delete.
test("admin challenge lifecycle", async ({ page }) => {
  const id = rid();
  await setEventWindow();

  const admin = runAdminCreds();
  await page.goto("/login");
  await page.getByLabel("Email").fill(admin.email);
  await page.getByLabel("Password").fill(admin.password);
  await page.getByRole("button", { name: "Log in" }).click();
  await expect(page).toHaveURL(/\/challenges/);

  // Create a challenge via the editor.
  await page.goto("/admin/challenges/new");
  await page.getByLabel("Title").fill(`Admin E2E ${id}`);
  await page.getByLabel("Flag").fill(`OSCTF{admin_${id}}`);
  // Static scoring keeps the form simple.
  await page.getByLabel("Scoring").selectOption("static");
  await page.getByLabel("Visible to participants").check();
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page).toHaveURL(/\/admin\/challenges\/[0-9a-f-]+/);

  // It shows up on the participant board.
  await page.goto("/challenges");
  await expect(page.getByText(`Admin E2E ${id}`)).toBeVisible();

  // Edit points and save, then confirm the change persisted (durable — avoids
  // racing the transient toast).
  await page.goto("/admin/challenges");
  await page.getByRole("link", { name: `Admin E2E ${id}` }).click();
  await page.getByLabel("Initial").fill("250");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("Saved").first()).toBeVisible();
  await page.reload();
  await expect(page.getByLabel("Initial")).toHaveValue("250");

  // Delete it.
  page.on("dialog", (d) => void d.accept());
  await page.getByRole("button", { name: "Delete" }).click();
  await expect(page).toHaveURL(/\/admin\/challenges$/);
});
