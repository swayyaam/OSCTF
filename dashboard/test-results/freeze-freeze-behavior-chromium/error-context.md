# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: freeze.spec.ts >> freeze behavior
- Location: e2e/freeze.spec.ts:19:1

# Error details

```
Error: expect(received).toBeTruthy()

Received: false
```

# Test source

```ts
  1  | import { type APIRequestContext, expect } from "@playwright/test";
  2  | 
  3  | export const BASE = process.env.BASE_URL ?? "http://localhost:8080";
  4  | export const ADMIN_EMAIL = process.env.OSCTF_ADMIN_EMAIL ?? "admin@example.com";
  5  | export const ADMIN_PASSWORD = process.env.OSCTF_ADMIN_PASSWORD ?? "devpassword123";
  6  | 
  7  | const ORIGIN = { Origin: BASE };
  8  | 
  9  | /** rid returns a short random suffix for unique usernames/teams per run. */
  10 | export function rid(): string {
  11 |   return Math.random().toString(36).slice(2, 8);
  12 | }
  13 | 
  14 | /** apiLoginAdmin logs the admin in on an API context and returns it. */
  15 | export async function apiAdmin(request: APIRequestContext): Promise<void> {
  16 |   const res = await request.post(`${BASE}/api/v0/auth/login`, {
  17 |     headers: ORIGIN,
  18 |     data: { email: ADMIN_EMAIL, password: ADMIN_PASSWORD },
  19 |   });
> 20 |   expect(res.ok()).toBeTruthy();
     |                    ^ Error: expect(received).toBeTruthy()
  21 | }
  22 | 
  23 | /** setEventWindow puts the event window around now so the board is open. */
  24 | export async function setEventWindow(request: APIRequestContext): Promise<void> {
  25 |   const now = Date.now();
  26 |   const iso = (ms: number) => new Date(now + ms).toISOString();
  27 |   const res = await request.patch(`${BASE}/api/v0/admin/event`, {
  28 |     headers: ORIGIN,
  29 |     data: { starts_at: iso(-3600_000), ends_at: iso(3600_000) },
  30 |   });
  31 |   expect(res.ok()).toBeTruthy();
  32 | }
  33 | 
  34 | /** createChallenge creates a visible standard challenge and returns its slug. */
  35 | export async function createChallenge(
  36 |   request: APIRequestContext,
  37 |   opts: { title: string; flag: string; points?: number },
  38 | ): Promise<string> {
  39 |   const res = await request.post(`${BASE}/api/v0/admin/challenges`, {
  40 |     headers: ORIGIN,
  41 |     data: {
  42 |       title: opts.title,
  43 |       category: "misc",
  44 |       flag: opts.flag,
  45 |       scoring: "static",
  46 |       points_initial: opts.points ?? 100,
  47 |       visible: true,
  48 |     },
  49 |   });
  50 |   expect(res.ok(), await res.text()).toBeTruthy();
  51 |   const body = (await res.json()) as { slug: string };
  52 |   return body.slug;
  53 | }
  54 | 
```