import * as assert from "assert";
import * as vscode from "vscode";

const ID = "agentwatch.agentwatch";

describe("AgentWatch extension in a real VS Code", () => {
  it("activates", async () => {
    const ext = vscode.extensions.getExtension(ID);
    assert.ok(ext, "extension not found");
    await ext!.activate();
    assert.ok(ext!.isActive);
  });

  it("registers every command", async () => {
    const all = await vscode.commands.getCommands(true);
    for (const c of ["agentwatch.signIn", "agentwatch.signOut", "agentwatch.operatorSignIn", "agentwatch.connect",
      "agentwatch.doctor", "agentwatch.openDashboard", "agentwatch.refresh"]) {
      assert.ok(all.includes(c), c);
    }
  });

  it("contributes the six views and the walkthrough", () => {
    const pkg = vscode.extensions.getExtension(ID)!.packageJSON;
    const views = pkg.contributes.views.agentwatch.map((v: { id: string }) => v.id);
    assert.deepStrictEqual(views, ["agentwatch.live", "agentwatch.runs", "agentwatch.findings", "agentwatch.approvals",
      "agentwatch.gate", "agentwatch.audit"]);
    assert.strictEqual(pkg.contributes.walkthroughs[0].steps.length, 4);
  });

  it("opens every view without error", async () => {
    for (const v of ["live", "runs", "findings", "approvals", "gate", "audit"]) {
      await vscode.commands.executeCommand(`agentwatch.${v}.focus`);
    }
  });

  it("opens the Connect panel", async () => {
    await vscode.commands.executeCommand("agentwatch.connect");
    await new Promise((r) => setTimeout(r, 500));
    const tabs = vscode.window.tabGroups.all.flatMap((g) => g.tabs).map((t) => t.label);
    assert.ok(tabs.some((l) => l.includes("AgentWatch: Connect")), JSON.stringify(tabs));
  });

  it("rejects a non-https API URL for real requests", async () => {
    await vscode.workspace.getConfiguration("agentwatch").update("apiUrl", "http://api.example.com", vscode.ConfigurationTarget.Global);
    await vscode.commands.executeCommand("agentwatch.refresh");
    await vscode.workspace.getConfiguration("agentwatch").update("apiUrl", undefined, vscode.ConfigurationTarget.Global);
  });
});
