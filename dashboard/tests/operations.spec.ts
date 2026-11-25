import { expect, test, type Page } from "@playwright/test";
import { mkdir } from "node:fs/promises";
import path from "node:path";

function token(name: string) {
  const value = process.env[name];
  if (!value) throw new Error(name + " is required to exercise the real ledger");
  return value;
}

async function connect(page: Page, value: string) {
  await page.goto("/");
  await page.getByLabel("Workspace token").fill(value);
  await page.getByRole("button", { name: "Connect workspace" }).click();
  await expect(page.getByRole("heading", { name: "Ledger overview" })).toBeVisible();
  await expect(page.getByText("Overdraft prohibited")).toHaveCount(2);
}

async function screenshot(page: Page, name: string) {
  if (process.env.LEDGER_SCREENSHOT_DIR) {
    await mkdir(process.env.LEDGER_SCREENSHOT_DIR, { recursive: true });
    await page.screenshot({ path: path.join(process.env.LEDGER_SCREENSHOT_DIR, name + ".png"), fullPage: true, animations: "disabled" });
  }
}

test("post, inspect, reverse, reconstruct and reconcile persisted synthetic records", async ({ page }) => {
  await connect(page, token("LEDGER_OPERATOR_TOKEN"));
  await expect(page.getByRole("heading", { name: "API instance" })).toBeVisible();
  await screenshot(page, "overview");
  const memo = "Browser synthetic transfer " + Date.now();
  const source = page.getByRole("combobox", { name: "From", exact: true });
  const destination = page.getByRole("combobox", { name: "To", exact: true });
  await source.selectOption({ label: "Operating" });
  await source.focus();
  await page.keyboard.press("Tab");
  await expect(destination).toBeFocused();
  await destination.selectOption({ label: "Reserve" });
  await page.getByLabel("Amount in minor units").fill("275");
  await page.getByLabel("Memo").fill(memo);
  await page.getByRole("button", { name: "Post transfer" }).click();
  await expect(page.getByRole("status")).toHaveText("Transfer posted.");

  await page.getByRole("button", { name: "Journal", exact: true }).click();
  await page.getByLabel("Search memo or ID").fill(memo);
  await page.getByRole("combobox", { name: "Currency", exact: true }).selectOption("USD");
  const row = page.getByRole("row").filter({ hasText: memo });
  await expect(row).toHaveCount(1);
  const sequence = (await row.getByRole("cell").nth(0).innerText()).trim();
  const journal = (await row.getByRole("cell").nth(1).locator("small").innerText()).trim();
  await row.getByRole("button", { name: "Inspect" }).click();
  await expect(page.getByText("Debit", { exact: true })).toBeVisible();
  await expect(page.getByText("Credit", { exact: true })).toBeVisible();
  await expect(page.getByText("−2.75 USD", { exact: true })).toBeVisible();
  await expect(page.getByText("2.75 USD", { exact: true })).toBeVisible();
  await screenshot(page, "journal");
  page.once("dialog", dialog => dialog.accept());
  await page.getByRole("button", { name: "Reverse transaction" }).click();
  await expect(page.getByRole("status")).toHaveText("Reversal posted.");

  await page.getByRole("button", { name: "Overview", exact: true }).click();
  await page.getByLabel("Through journal sequence").fill(sequence);
  await page.getByRole("button", { name: "Inspect balances" }).click();
  await expect(page.getByRole("list", { name: "Historical results" }).getByRole("listitem")).toHaveCount(3);

  await page.getByRole("button", { name: "Reconciliation", exact: true }).click();
  await page.getByLabel("From sequence").fill(sequence);
  await page.getByLabel("Through sequence").fill(sequence);
  await page.getByLabel("Statement JSON").fill(JSON.stringify([{ journal, currency: "USD", credits: "276" }]));
  await page.getByRole("button", { name: "Compare statement" }).click();
  await expect(page.getByRole("status")).toHaveText("Reconciliation saved.");
  const reportTitle = "Sequences " + sequence + "–" + sequence;
  const report = page.locator("details").filter({ has: page.locator("summary").filter({ hasText: reportTitle }) });
  await report.locator("summary").click();
  await expect(report.getByText("mismatch", { exact: true })).toBeVisible();
  await screenshot(page, "reconciliation");
});

test("viewer can inspect balances on mobile and cannot post", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await connect(page, token("LEDGER_VIEWER_TOKEN"));
  await expect(page.getByText("viewer", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Post transfer" })).toHaveCount(0);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
  expect(overflow).toBe(false);
  const response = await page.request.post("/api/ledger/journals", {
    headers: { Authorization: "Bearer " + token("LEDGER_VIEWER_TOKEN"), "Idempotency-Key": "viewer-forbidden" },
    data: { entries: [] },
  });
  expect(response.status()).toBe(403);
  await screenshot(page, "mobile");
  await page.getByRole("button", { name: "Disconnect" }).click();
  await expect(page.getByLabel("Workspace token")).toBeVisible();
});

test("rejects cross-origin proxy writes and retries an ambiguous committed transfer exactly once", async ({ page }) => {
  const operator = token("LEDGER_OPERATOR_TOKEN");
  await connect(page, operator);
  const denied = await page.request.post("/api/ledger/journals", {
    headers: { Authorization: "Bearer " + operator, Origin: "https://other.invalid", "Sec-Fetch-Site": "cross-site" },
    data: { entries: [] },
  });
  expect(denied.status()).toBe(403);
  const oversized = await page.request.post("/api/ledger/journals", {
    headers: { Authorization: "Bearer " + operator, "Content-Type": "application/json" },
    data: " ".repeat(128 * 1024 + 1),
  });
  expect(oversized.status()).toBe(413);
  const route = "**/api/ledger/journals";
  let originalStatus: number | undefined;
  let originalKey: string | undefined;
  await page.route(route, async intercepted => {
    originalKey = intercepted.request().headers()["idempotency-key"];
    originalStatus = (await intercepted.fetch()).status();
    await intercepted.abort();
  }, { times: 1 });
  const memo = "Ambiguous browser transfer " + Date.now();
  await page.getByRole("combobox", { name: "From", exact: true }).selectOption({ label: "Operating" });
  await page.getByRole("combobox", { name: "To", exact: true }).selectOption({ label: "Reserve" });
  await page.getByLabel("Amount in minor units").fill("1");
  await page.getByLabel("Memo").fill(memo);
  await page.getByRole("button", { name: "Post transfer" }).evaluate((button: HTMLButtonElement) => {
    button.form?.requestSubmit();
    button.form?.requestSubmit();
  });
  const retry = page.getByRole("button", { name: "Retry same request" });
  await expect(retry).toBeVisible();
  expect(originalStatus).toBe(201);
  expect(originalKey).toBeTruthy();
  const replay = page.waitForResponse(response => response.url().endsWith("/api/ledger/journals") && response.request().method() === "POST");
  await retry.click();
  const replayResponse = await replay;
  expect(replayResponse.status()).toBe(200);
  expect(replayResponse.headers()["idempotency-replayed"]).toBe("true");
  expect(replayResponse.request().headers()["idempotency-key"]).toBe(originalKey);
  await page.getByRole("button", { name: "Journal", exact: true }).click();
  await page.getByLabel("Search memo or ID").fill(memo);
  await expect(page.getByRole("row").filter({ hasText: memo })).toHaveCount(1);
});
