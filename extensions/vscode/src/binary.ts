import * as cp from "child_process";
import * as fs from "fs";
import * as path from "path";
import * as vscode from "vscode";

/** The agentwatch binary shipped inside this platform-specific VSIX. */
export function binaryPath(ctx: vscode.ExtensionContext): string {
  const name = process.platform === "win32" ? "agentwatch.exe" : "agentwatch";
  return path.join(ctx.extensionPath, "bin", name);
}

export function hasBinary(ctx: vscode.ExtensionContext): boolean {
  try {
    fs.accessSync(binaryPath(ctx), fs.constants.X_OK);
    return true;
  } catch {
    return process.platform === "win32" && fs.existsSync(binaryPath(ctx));
  }
}

export interface RunResult { code: number; stdout: string; stderr: string }

/** Run the binary with args (never through a shell). Secrets go on stdin. */
export function run(ctx: vscode.ExtensionContext, args: string[], opts: { stdin?: string; env?: NodeJS.ProcessEnv; timeoutMs?: number } = {}): Promise<RunResult> {
  return new Promise((resolve) => {
    const apiUrl = vscode.workspace.getConfiguration("agentwatch").get<string>("apiUrl") || "";
    const child = cp.spawn(binaryPath(ctx), args, {
      env: { ...process.env, ...(apiUrl ? { AGENTWATCH_API_URL: apiUrl } : {}), ...opts.env },
      stdio: ["pipe", "pipe", "pipe"],
      windowsHide: true,
    });
    let stdout = "";
    let stderr = "";
    const timer = setTimeout(() => child.kill(), opts.timeoutMs ?? 30000);
    child.stdout.on("data", (d) => (stdout += d.toString()));
    child.stderr.on("data", (d) => (stderr += d.toString()));
    child.on("error", (e) => {
      clearTimeout(timer);
      resolve({ code: -1, stdout, stderr: String(e) });
    });
    child.on("close", (code) => {
      clearTimeout(timer);
      resolve({ code: code ?? -1, stdout, stderr });
    });
    if (opts.stdin !== undefined) child.stdin.end(opts.stdin);
    else child.stdin.end();
  });
}

export async function runJSON<T>(ctx: vscode.ExtensionContext, args: string[]): Promise<T | null> {
  const r = await run(ctx, args);
  try {
    return JSON.parse(r.stdout) as T;
  } catch {
    return null;
  }
}
