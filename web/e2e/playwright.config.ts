import { defineConfig } from "@playwright/test";
import { resolve } from "node:path";
import { LOCAL, spki } from "./support/env";

/** Production-acceptance suite against the real backend; see docs/plans/2026-10-09-production-acceptance.md. */
export default defineConfig({
  testDir: ".",
  outputDir: resolve(LOCAL, "e2e/test-results"),
  globalSetup: "./support/global-setup.ts",
  fullyParallel: true,
  workers: Number(process.env.E2E_WORKERS ?? 6),
  timeout: 60_000,
  reporter: [["dot"], ["json", { outputFile: resolve(LOCAL, "e2e/results.json") }], ["html", { outputFolder: resolve(LOCAL, "e2e/report"), open: "never" }]],
  use: {
    viewport: { width: 1440, height: 900 },
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
    launchOptions: {
      args: [
        // *.redstring.test resolves to the local TLS proxy without touching /etc/hosts.
        "--host-resolver-rules=MAP *.redstring.test 127.0.0.1",
        // Trust only the acceptance certificate; WebAuthn needs a clean secure context.
        `--ignore-certificate-errors-spki-list=${spki()}`,
      ],
    },
  },
});
