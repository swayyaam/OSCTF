import { type APIRequestContext, expect } from "@playwright/test";

export const BASE = process.env.BASE_URL ?? "http://localhost:8080";
export const ADMIN_EMAIL = process.env.OSCTF_ADMIN_EMAIL ?? "admin@example.com";
export const ADMIN_PASSWORD = process.env.OSCTF_ADMIN_PASSWORD ?? "devpassword123";

const ORIGIN = { Origin: BASE };

/** rid returns a short random suffix for unique usernames/teams per run. */
export function rid(): string {
  return Math.random().toString(36).slice(2, 8);
}

/** apiLoginAdmin logs the admin in on an API context and returns it. */
export async function apiAdmin(request: APIRequestContext): Promise<void> {
  const res = await request.post(`${BASE}/api/v0/auth/login`, {
    headers: ORIGIN,
    data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD },
  });
  expect(res.ok()).toBeTruthy();
}

/** setEventWindow puts the event window around now so the board is open. */
export async function setEventWindow(request: APIRequestContext): Promise<void> {
  const now = Date.now();
  const iso = (ms: number) => new Date(now + ms).toISOString();
  const res = await request.patch(`${BASE}/api/v0/admin/event`, {
    headers: ORIGIN,
    data: { starts_at: iso(-3600_000), ends_at: iso(3600_000) },
  });
  expect(res.ok()).toBeTruthy();
}

/** createChallenge creates a visible standard challenge and returns its slug. */
export async function createChallenge(
  request: APIRequestContext,
  opts: { title: string; flag: string; points?: number },
): Promise<string> {
  const res = await request.post(`${BASE}/api/v0/admin/challenges`, {
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
export async function createPerTeamWeb(
  request: APIRequestContext,
  title: string,
): Promise<string> {
  const res = await request.post(`${BASE}/api/v0/admin/challenges`, {
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
