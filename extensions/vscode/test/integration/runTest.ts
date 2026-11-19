import * as path from "path";
import { runTests } from "@vscode/test-electron";

async function main(): Promise<void> {
  const extensionDevelopmentPath = path.resolve(__dirname, "../../..");
  const extensionTestsPath = path.resolve(__dirname, "./suite/index");
  await runTests({
    version: process.env.VSCODE_TEST_VERSION || "stable",
    extensionDevelopmentPath,
    extensionTestsPath,
    launchArgs: ["--disable-extensions", "--skip-welcome", "--skip-release-notes"],
  });
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
