import { request as pwRequest } from "@playwright/test";
import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const BASE = process.env.BASE_URL ?? "http://localhost:8080";
const SEED_EMAIL = process.env.OSCTF_ADMIN_EMAIL ?? "admin@example.com";
const SEED_PASSWORD = process.env.OSCTF_ADMIN_PASSWORD ?? "devpassword123";
const ORIGIN = { Origin: BASE };

/** This run's admin credentials (for the UI login) and its authenticated session
 * (as a Playwright storageState the API helpers reuse without logging in again). */
export const RUN_ADMIN_FILE = fileURLToPath(new URL(".run-admin.json", import.meta.url));
export const RUN_ADMIN_STATE = fileURLToPath(new URL(".run-admin-state.json", import.meta.url));

/**
 * Provision a fresh admin account for THIS run so the suite never shares the one
 * seeded admin against the login rate limits (re-runs then trip 429 and cascade).
 * The seeded admin is used exactly once, here, only to promote the new account.
 *
 * register auto-logs-in, so the register context already holds the new user's
 * session; once we promote the user to admin, that same session is an admin
 * session. Saving it as a storageState lets every spec act as admin with ZERO
 * further logins — the whole run spends one seeded login here plus the one UI
 * login in the admin flow.
 */
export default async function globalSetup(): Promise<void> {
  const admin = await pwRequest.newContext(); // seeded-admin session, for the promote
  const runAdmin = await pwRequest.newContext(); // run-admin session, captured below
  try {
    const login = await admin.post(`${BASE}/api/v0/auth/login`, {
      headers: ORIGIN,
      data: { email: SEED_EMAIL, password: SEED_PASSWORD },
    });
    if (!login.ok()) {
      throw new Error(`seed admin login ${String(login.status())}: ${await login.text()}`);
    }

    const tag = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
    const email = `e2e-admin-${tag}@example.com`;
    const password = "e2e-admin-pw-1234";
    const reg = await runAdmin.post(`${BASE}/api/v0/auth/register`, {
      headers: ORIGIN,
      data: { username: `e2eadmin${tag}`, email, password },
    });
    if (!reg.ok()) {
      throw new Error(`run admin register ${String(reg.status())}: ${await reg.text()}`);
    }
    const me = (await reg.json()) as { id: string };

    const promote = await admin.patch(`${BASE}/api/v0/admin/users/${me.id}`, {
      headers: ORIGIN,
      data: { role: "admin" },
    });
    if (!promote.ok()) {
      throw new Error(`promote run admin ${String(promote.status())}: ${await promote.text()}`);
    }

    // The run-admin's session is now an admin session — save it for the specs.
    await runAdmin.storageState({ path: RUN_ADMIN_STATE });
    writeFileSync(RUN_ADMIN_FILE, JSON.stringify({ email, password }));
  } finally {
    await admin.dispose();
    await runAdmin.dispose();
  }
}
