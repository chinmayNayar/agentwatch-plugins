// Package one platform-specific VSIX with its agentwatch binary inside.
//   node scripts/package-target.mjs <target> <path-to-agentwatch-binary> [outdir]
// target: darwin-arm64 | darwin-x64 | linux-x64 | linux-arm64 | win32-x64
import { execFileSync } from "node:child_process";
import { chmodSync, copyFileSync, mkdirSync, readFileSync, rmSync } from "node:fs";
import { join } from "node:path";

const [target, binary, outdir = "vsix"] = process.argv.slice(2);
const TARGETS = ["darwin-arm64", "darwin-x64", "linux-x64", "linux-arm64", "win32-x64"];
if (!TARGETS.includes(target) || !binary) {
  console.error(`usage: package-target.mjs <${TARGETS.join("|")}> <binary> [outdir]`);
  process.exit(2);
}
const { version } = JSON.parse(readFileSync("package.json", "utf8"));
rmSync("bin", { recursive: true, force: true });
mkdirSync("bin");
const name = target.startsWith("win32") ? "agentwatch.exe" : "agentwatch";
copyFileSync(binary, join("bin", name));
chmodSync(join("bin", name), 0o755);
mkdirSync(outdir, { recursive: true });
const out = join(outdir, `agentwatch-${target}-${version}.vsix`);
execFileSync("node", ["esbuild.mjs"], { stdio: "inherit" });
execFileSync("npx", ["vsce", "package", "--target", target, "--out", out], { stdio: "inherit" });
console.log(out);
