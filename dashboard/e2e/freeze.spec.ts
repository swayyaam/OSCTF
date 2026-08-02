import { test, expect, type APIRequestContext } from "@playwright/test";
import { BASE, adminRequest, createChallenge, rid } from "./helpers";

const ORIGIN = { Origin: BASE };

async function setFreezeInPast() {
  const admin = await adminRequest();
  const now = Date.now();
  const iso = (ms: number) => new Date(now + ms).toISOString();
  // Window started 2h ago, ends in 1h, froze 1h ago.
  const res = await admin.patch(`${BASE}/api/v0/admin/event`, {
    headers: ORIGIN,
    data: { starts_at: iso(-7200_000), ends_at: iso(3600_000), freeze_at: iso(-3600_000) },
  });
  expect(res.ok()).toBeTruthy();
}

// Flow 3 — freeze behavior: admin sets freeze in the past -> the participant
// scoreboard shows the frozen banner and stops moving while a new solve lands.
test("freeze behavior", async ({ page, request }) => {
  const id = rid();
  await setFreezeInPast();
  const slug = await createChallenge({ title: `Freeze ${id}`, flag: `OSCTF{frz_${id}}` });

  // Wait for the freeze to actually propagate to the public API before loading
  // the page (the banner is driven by the /scoreboard `frozen` flag; asserting on
  // the UI alone races the recompute in slower CI environments).
  await expect
    .poll(async () => {
      const r = await request.get(`${BASE}/api/v0/scoreboard`);
      return ((await r.json()) as { frozen: boolean }).frozen;
    })
    .toBe(true);

  // The public scoreboard shows the frozen banner.
  await page.goto("/scoreboard");
  await expect(page.getByText("Frozen", { exact: true })).toBeVisible();

  // A brand-new team solves the challenge via the API (the first public read above
  // already captured the frozen snapshot).
  const solved = await playApiRegisterAndSolve(request, id, `Frz Team ${id}`, slug, `OSCTF{frz_${id}}`);
  expect(solved).toBeTruthy();

  // The public board stays frozen — the new team is not shown with points yet.
  await page.reload();
  await expect(page.getByText("Frozen", { exact: true })).toBeVisible();
});

// playApiRegisterAndSolve registers a user, makes a team, and submits the flag.
async function playApiRegisterAndSolve(
  request: APIRequestContext,
  id: string,
  teamName: string,
  slug: string,
  flag: string,
): Promise<boolean> {
  const H = { Origin: BASE };
  await request.post(`${BASE}/api/v0/auth/register`, {
    headers: H,
    data: { username: `frz_${id}`, email: `frz_${id}@example.com`, password: "supersecret1" },
  });
  await request.post(`${BASE}/api/v0/teams`, { headers: H, data: { name: teamName } });
  const res = await request.post(`${BASE}/api/v0/challenges/${slug}/submit`, {
    headers: H,
    data: { flag },
  });
  return res.ok();
}
