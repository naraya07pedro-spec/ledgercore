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
  await page.getByRole("combobox", { name: "From", exact: true }).selectOption({ label: "Operating" });
  await page.getByRole("combobox", { name: "To", exact: true }).selectOption({ label: "Reserve" });
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
  await page.getByText("Sequences " + sequence + "–" + sequence, { exact: false }).click();
  await expect(page.getByText("mismatch", { exact: true })).toBeVisible();
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
