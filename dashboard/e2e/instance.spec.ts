import { test, expect } from "@playwright/test";
import { apiAdmin, createPerTeamWeb, rid, setEventWindow, BASE } from "./helpers";

interface ChallengeDetail {
  instance: { state: string; connection_info: string | null } | null;
}

// Flow 4 — per-team instances golden path: register -> team -> open a per_team,
// per_instance challenge -> Start -> read the team's unique flag from its own
// instance -> submit -> solved -> Extend -> Stop.
test("per-team instance golden path", async ({ page, request }) => {
  // Deploying a container (pull + start + health) can take a while on a cold CI
  // runner; allow well over the default 30s so the running poll can complete.
  test.setTimeout(150_000);
  const id = rid();
  await apiAdmin(request);
  await setEventWindow(request);
  const slug = await createPerTeamWeb(request, `E2E Instance ${id}`);

  // Register and create a team through the UI.
  await page.goto("/register");
  await page.getByLabel("Username").fill(`inst_${id}`);
  await page.getByLabel("Email").fill(`inst_${id}@example.com`);
  await page.getByLabel("Password").fill("supersecret1");
  await page.getByRole("button", { name: "Register" }).click();
  await expect(page).toHaveURL(/\/challenges/);
  await page.goto("/team");
  await page.getByLabel("Team name").fill(`E2E Inst Team ${id}`);
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect(page.getByTestId("invite-code")).toBeVisible();

  // Open the challenge and start the instance.
  await page.goto(`/challenges/${slug}`);
  await expect(page.getByTestId("instance-start")).toBeVisible();
  await page.getByTestId("instance-start").click();

  // Wait for the instance to report running (image deploy). Poll the API with the
  // page's session cookies, matching the freeze spec's poll-for-propagation style.
  let connInfo = "";
  await expect
    .poll(
      async () => {
        const res = await page.request.get(`${BASE}/api/v0/challenges/${slug}`);
        if (!res.ok()) return "";
        const body = (await res.json()) as ChallengeDetail;
        if (body.instance?.state === "running") {
          connInfo = body.instance.connection_info ?? "";
          return "running";
        }
        return body.instance?.state ?? "none";
      },
      { timeout: 90_000, intervals: [1000, 2000, 3000] },
    )
    .toBe("running");
  expect(connInfo).toMatch(/^http:\/\//);

  // Read this team's UNIQUE flag from its own instance (via the published port).
  const flagRes = await page.request.get(`${connInfo}/flag?debug=1`);
  expect(flagRes.ok(), `read flag from ${connInfo}`).toBeTruthy();
  const flag = (await flagRes.text()).trim();
  expect(flag).toMatch(/^osctf\{/i);

  // The countdown and controls are visible while running.
  await expect(page.getByTestId("instance-countdown")).toBeVisible();
  await expect(page.getByTestId("instance-extend")).toBeVisible();

  // Submit the team's flag -> solved.
  await page.getByTestId("flag-input").fill(flag);
  await page.getByTestId("flag-submit").click();
  await expect(page.getByText(/Solved/)).toBeVisible();

  // Extend does not error (button stays available).
  await page.getByTestId("instance-extend").click();
  await expect(page.getByTestId("instance-countdown")).toBeVisible();

  // Stop returns the panel to the Start state.
  await page.getByTestId("instance-stop").click();
  await expect(page.getByTestId("instance-start")).toBeVisible();
});
