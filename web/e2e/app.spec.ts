import { expect, test } from "@playwright/test";
import { mockApi } from "./fixtures";

test("signs in with the keyboard and lands on the dashboard", async ({
  page,
}) => {
  await mockApi(page);
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();

  await page.getByLabel("Username").fill("diego");
  await page.getByLabel("Password").fill("wrong");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("alert")).toHaveText(
    "Wrong username or password.",
  );

  await page.getByLabel("Password").fill("correct horse battery");
  await page.keyboard.press("Enter");
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await expect(page.getByText("99.31 %")).toBeVisible();
  await expect(
    page.getByRole("group", { name: /p50 and p95 latency/ }),
  ).toBeVisible();
});

test("creates a check from the dialog and returns focus", async ({ page }) => {
  await mockApi(page, { signedIn: true });
  await page.goto("/");
  const open = page.getByRole("button", { name: "New check" });
  await open.click();

  const dialog = page.getByRole("dialog", { name: "New check" });
  await expect(dialog).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(open).toBeFocused();

  await open.click();
  await dialog.getByLabel("Name").fill("Status API");
  await dialog.getByLabel("Target").fill("https://status.example.com/health");
  await dialog.getByRole("button", { name: "Create check" }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("status")).toContainText("Created Status API");
});

test("asks before deleting and keeps Cancel as the default", async ({
  page,
}) => {
  await mockApi(page, { signedIn: true });
  await page.goto("/");
  await page.getByRole("button", { name: "Delete Bitso API" }).click();
  const dialog = page.getByRole("dialog", { name: "Delete Bitso API?" });
  await expect(dialog.getByRole("button", { name: "Cancel" })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
});

test("the chart is readable with the keyboard and as a table", async ({
  page,
}) => {
  await mockApi(page, { signedIn: true });
  await page.goto("/");
  const chart = page.getByRole("group", { name: /p50 and p95 latency/ });
  await chart.focus();
  await page.keyboard.press("ArrowLeft");
  await expect(page.locator(".tooltip")).toContainText("p95");
  await page.getByRole("button", { name: "Show as table" }).click();
  await expect(
    page.getByRole("table", { name: /p50 and p95 latency/ }).getByRole("row"),
  ).toHaveCount(25);
});

test("public status page needs no login and hides targets", async ({
  page,
}) => {
  await mockApi(page);
  await page.goto("/status");
  await expect(page.getByText("Major outage")).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Bitso API" }).first(),
  ).toBeVisible();
  await expect(page.getByText("api.bitso.com")).toHaveCount(0);
  await expect(page.getByText(/ongoing for/)).toBeVisible();
  await expect(page.getByText("HTTP 503")).toHaveCount(0);
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth >
      document.documentElement.clientWidth,
  );
  expect(overflow).toBe(false);
});

test("sends a test alert from the incidents card", async ({ page }) => {
  await mockApi(page, { signedIn: true });
  await page.goto("/");
  await expect(page.getByText("· timeout").first()).toBeVisible();
  await page.getByRole("button", { name: "Send test alert" }).click();
  await expect(
    page.getByRole("status").filter({ hasText: "Test alert sent" }),
  ).toBeVisible();
});

test("shows the AI diagnosis and starts a new investigation", async ({
  page,
}) => {
  await mockApi(page, { signedIn: true });
  await page.goto("/");
  const diagnosis = page.getByRole("region", {
    name: "AI diagnosis for donblack.dev TLS",
  });
  await expect(
    diagnosis.getByText(/stopped answering on port 443/),
  ).toBeVisible();
  await expect(diagnosis.getByText("high confidence")).toBeVisible();
  await expect(
    diagnosis.getByText("Check that the web server is running on the host"),
  ).toBeVisible();

  // Evidence and tools are folded until asked for.
  await expect(diagnosis.getByText("Live probe")).toBeHidden();
  await diagnosis.getByText(/Evidence and tools \(3 calls\)/).click();
  await expect(diagnosis.getByText("Live probe")).toBeVisible();
  await expect(diagnosis.getByText(/19,150\s+tokens/)).toBeVisible();

  await page
    .getByRole("button", { name: "Investigate with Claude (Bitso API)" })
    .click();
  await expect(
    page.getByRole("status").filter({ hasText: "Claude is investigating" }),
  ).toBeVisible();
  await expect(
    page
      .getByRole("region", { name: "AI diagnosis for Bitso API" })
      .getByText(/503 for 20 minutes/),
  ).toBeVisible();
});

test("the public status page never shows the AI diagnosis", async ({
  page,
}) => {
  await mockApi(page);
  await page.goto("/status");
  await expect(page.getByText("donblack.dev TLS").first()).toBeVisible();
  await expect(page.getByText(/AI diagnosis|port 443/)).toHaveCount(0);
});
