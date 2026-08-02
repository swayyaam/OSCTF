import { test, expect } from "@playwright/test";
import { createChallenge, rid, setEventWindow } from "./helpers";

// Flow 1 — participant golden path: register -> create team -> open challenges ->
// open a standard challenge -> submit wrong flag -> submit correct flag ->
// scoreboard shows the team with points.
test("participant golden path", async ({ page }) => {
  const id = rid();
  await setEventWindow();
  const slug = await createChallenge({
    title: `E2E Sanity ${id}`,
    flag: `OSCTF{e2e_${id}}`,
  });

  await page.goto("/register");
  await page.getByLabel("Username").fill(`e2e_${id}`);
  await page.getByLabel("Email").fill(`e2e_${id}@example.com`);
  await page.getByLabel("Password").fill("supersecret1");
  await page.getByRole("button", { name: "Register" }).click();

  await expect(page).toHaveURL(/\/challenges/);

  // Create a team.
  await page.goto("/team");
  await page.getByLabel("Team name").fill(`E2E Team ${id}`);
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByTestId("invite-code")).toBeVisible();

  // Open the challenge and submit a wrong flag.
  await page.goto(`/challenges/${slug}`);
  await expect(page.getByTestId("flag-input")).toBeVisible();
  await page.getByTestId("flag-input").fill("OSCTF{wrong}");
  await page.getByTestId("flag-submit").click();
  await expect(page.getByText(/Incorrect flag/)).toBeVisible();

  // Submit the correct flag. The challenge then shows as solved (the transient
  // "Correct!" line is replaced by the durable "Solved ✓" state on refetch).
  await page.getByTestId("flag-input").fill(`OSCTF{e2e_${id}}`);
  await page.getByTestId("flag-submit").click();
  await expect(page.getByText(/Solved/)).toBeVisible();

  // Scoreboard shows the team with points.
  await page.goto("/scoreboard");
  const row = page.getByTestId("scoreboard-table").getByText(`E2E Team ${id}`);
  await expect(row).toBeVisible();
});
