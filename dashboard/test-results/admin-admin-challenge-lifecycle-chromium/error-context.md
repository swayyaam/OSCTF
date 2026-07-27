# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: admin.spec.ts >> admin challenge lifecycle
- Location: e2e/admin.spec.ts:6:1

# Error details

```
Error: expect(page).toHaveURL(expected) failed

Expected pattern: /\/challenges/
Received string:  "http://localhost:8080/login"
Timeout: 5000ms

Call log:
  - Expect "toHaveURL" with timeout 5000ms
    14 × unexpected value "http://localhost:8080/login"

```

```yaml
- banner:
  - link "My CTF":
    - /url: /
  - navigation:
    - link "Challenges":
      - /url: /challenges
    - link "Scoreboard":
      - /url: /scoreboard
  - button "Toggle theme": ☾
  - link "Log in":
    - /url: /login
  - link "Register":
    - /url: /register
- main:
  - heading "Log in" [level=1]
  - paragraph: Too many requests
  - text: Email
  - textbox "Email": admin@example.com
  - text: Password
  - textbox "Password": devpassword123
  - button "Log in"
  - paragraph:
    - text: No account?
    - link "Register":
      - /url: /register
```

# Test source

```ts
  1  | import { test, expect } from "@playwright/test";
  2  | import { ADMIN_EMAIL, ADMIN_PASSWORD, rid, setEventWindow, apiAdmin } from "./helpers";
  3  | 
  4  | // Flow 2 — admin challenge lifecycle: log in as admin -> create a visible standard
  5  | // challenge -> it appears on the participant board -> edit points -> delete.
  6  | test("admin challenge lifecycle", async ({ page, request }) => {
  7  |   const id = rid();
  8  |   await apiAdmin(request);
  9  |   await setEventWindow(request);
  10 | 
  11 |   await page.goto("/login");
  12 |   await page.getByLabel("Email").fill(ADMIN_EMAIL);
  13 |   await page.getByLabel("Password").fill(ADMIN_PASSWORD);
  14 |   await page.getByRole("button", { name: "Log in" }).click();
> 15 |   await expect(page).toHaveURL(/\/challenges/);
     |                      ^ Error: expect(page).toHaveURL(expected) failed
  16 | 
  17 |   // Create a challenge via the editor.
  18 |   await page.goto("/admin/challenges/new");
  19 |   await page.getByLabel("Title").fill(`Admin E2E ${id}`);
  20 |   await page.getByLabel("Flag").fill(`OSCTF{admin_${id}}`);
  21 |   // Static scoring keeps the form simple.
  22 |   await page.getByLabel("Scoring").selectOption("static");
  23 |   await page.getByLabel("Visible to participants").check();
  24 |   await page.getByRole("button", { name: "Create", exact: true }).click();
  25 |   await expect(page).toHaveURL(/\/admin\/challenges\/[0-9a-f-]+/);
  26 | 
  27 |   // It shows up on the participant board.
  28 |   await page.goto("/challenges");
  29 |   await expect(page.getByText(`Admin E2E ${id}`)).toBeVisible();
  30 | 
  31 |   // Edit points and save.
  32 |   await page.goto("/admin/challenges");
  33 |   await page.getByRole("link", { name: `Admin E2E ${id}` }).click();
  34 |   await page.getByLabel("Initial").fill("250");
  35 |   await page.getByRole("button", { name: "Save" }).click();
  36 |   await expect(page.getByText("Saved")).toBeVisible();
  37 | 
  38 |   // Delete it.
  39 |   page.on("dialog", (d) => void d.accept());
  40 |   await page.getByRole("button", { name: "Delete" }).click();
  41 |   await expect(page).toHaveURL(/\/admin\/challenges$/);
  42 | });
  43 | 
```