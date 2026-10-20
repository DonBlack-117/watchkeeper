import { test } from "@playwright/test";
import { mockApi } from "./fixtures";

// README screenshots from the mocked API: SHOTS_DIR=../docs/screenshots npx playwright test screenshots --project chromium
const dir = process.env.SHOTS_DIR;

test("README screenshots", async ({ page }) => {
  test.skip(!dir, "set SHOTS_DIR to write the screenshots");
  await page.setViewportSize({ width: 1280, height: 1000 });
  await mockApi(page, { signedIn: true });
  await page.goto("/");
  await page.getByText("99.31 %").waitFor();
  await page.screenshot({ path: `${dir ?? ""}/dashboard.png`, fullPage: true });
  await page.goto("/status");
  await page.getByText("Major outage").waitFor();
  await page.screenshot({ path: `${dir ?? ""}/status.png` });
});
