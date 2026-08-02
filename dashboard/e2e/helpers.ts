import { request as pwRequest, type APIRequestContext, expect } from "@playwright/test";
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

export const BASE = process.env.BASE_URL ?? "http://localhost:8080";
export const ADMIN_EMAIL = process.env.OSCTF_ADMIN_EMAIL ?? "admin@example.com";
export const ADMIN_PASSWORD = process.env.OSCTF_ADMIN_PASSWORD ?? "devpassword123";

const ORIGIN = { Origin: BASE };
const RUN_ADMIN_FILE = fileURLToPath(new URL(".run-admin.json", import.meta.url));
const RUN_ADMIN_STATE = fileURLToPath(new URL(".run-admin-state.json", import.meta.url));

/** rid returns a short random suffix for unique usernames/teams per run. */
export function rid(): string {
  return Math.random().toString(36).slice(2, 8);
}

/** runAdminCreds returns this run's admin login (for the admin UI flow), falling
 * back to the seeded admin when global-setup did not run (isolated spec). */
export function runAdminCreds(): { email: string; password: string } {
  if (existsSync(RUN_ADMIN_FILE)) {
    return JSON.parse(readFileSync(RUN_ADMIN_FILE, "utf8")) as { email: string; password: string };
  }
  return { email: ADMIN_EMAIL, password: ADMIN_PASSWORD };
}

// One shared, authenticated admin API context per run. Built from the run-admin's
// storageState (captured in global-setup) so the specs spend NO logins on the
// admin API — the whole suite stays well under the per-IP login limit. Falls back
// to a single login when run in isolation without global-setup.
let sharedAdmin: APIRequestContext | undefined;

/** adminRequest returns the shared authenticated admin API context. */
export async function adminRequest(): Promise<APIRequestContext> {
  if (sharedAdmin) return sharedAdmin;
  if (existsSync(RUN_ADMIN_STATE)) {
    sharedAdmin = await pwRequest.newContext({ storageState: RUN_ADMIN_STATE });
    return sharedAdmin;
  }
  const { email, password } = runAdminCreds();
  const ctx = await pwRequest.newContext();
  const res = await ctx.post(`${BASE}/api/v0/auth/login`, { headers: ORIGIN, data: { email, password } });
  expect(res.ok(), `admin login (${email})`).toBeTruthy();
  sharedAdmin = ctx;
  return sharedAdmin;
}

/** setEventWindow puts the event window around now so the board is open. */
export async function setEventWindow(): Promise<void> {
  const a = await adminRequest();
  const now = Date.now();
  const iso = (ms: number) => new Date(now + ms).toISOString();
  const res = await a.patch(`${BASE}/api/v0/admin/event`, {
    headers: ORIGIN,
    data: { starts_at: iso(-3600_000), ends_at: iso(3600_000) },
  });
  expect(res.ok()).toBeTruthy();
}

/** createChallenge creates a visible standard challenge and returns its slug. */
export async function createChallenge(opts: {
  title: string;
  flag: string;
  points?: number;
}): Promise<string> {
  const a = await adminRequest();
  const res = await a.post(`${BASE}/api/v0/admin/challenges`, {
    headers: ORIGIN,
    data: {
      title: opts.title,
      category: "misc",
      flag: opts.flag,
      scoring: "static",
      points_initial: opts.points ?? 100,
      visible: true,
    },
  });
  expect(res.ok(), await res.text()).toBeTruthy();
  const body = (await res.json()) as { slug: string };
  return body.slug;
}

/** createPerTeamWeb creates a visible per_team + per_instance container challenge
 * backed by the per-team-web example image and returns its slug. */
export async function createPerTeamWeb(title: string): Promise<string> {
  const a = await adminRequest();
  const res = await a.post(`${BASE}/api/v0/admin/challenges`, {
    headers: ORIGIN,
    data: {
      title,
      category: "web",
      kind: "container",
      flag: "OSCTF{placeholder}",
      scoring: "static",
      points_initial: 200,
      visible: true,
      image: "osctf/example-per-team-web:0.2",
      internal_port: 8000,
      connection_template: "http://{host}:{port}",
      instancing: "per_team",
      flag_mode: "per_instance",
    },
  });
  expect(res.ok(), await res.text()).toBeTruthy();
  const body = (await res.json()) as { slug: string };
  return body.slug;
}
