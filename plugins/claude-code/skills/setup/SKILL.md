---
name: setup
description: "Sign this machine in to AgentWatch: opens the approval page in the browser, shows the code, waits for approval and stores the token in the OS keychain."
---

Sign this machine in to AgentWatch with the `agentwatch` MCP server. Do not run any `agentwatch` command in the shell for this: the sign-in goes through the MCP tool.

1. Call `agentwatch_login` with no arguments.
   - If it says the machine is **already signed in**, tell the user that in one line and stop.
   - Otherwise it opens the approval page and returns a **page link and a code**. Show both to the user exactly as returned, for example:
     "Approve this machine in AgentWatch: open <page> and check it shows the code **<code>**. Sign in to the console if it asks."
2. Call `agentwatch_login` with `{"wait": true}`. Each call waits up to 45 seconds.
   - If it says it is still waiting, call it again with `{"wait": true}`. Keep going until it reports **Signed in** or an error, up to about 10 minutes. Do not start a new sign-in while one is waiting.
   - If it reports the code was refused or expired, tell the user and offer to start again.
3. When it reports **Signed in**, call `agentwatch_status` and give the user a three-line summary: signed in (device id), mode, and how many queued events are being sent.
4. Then ask the user whether they want the `agentwatch` command on their PATH. The status output shows the full binary path and a `link` command for this. Only if they say yes, run exactly the `... link` command shown in the status output, and show its output (it may print one line to add to their shell profile). If they say no, skip it.

Never print or ask for the token or an API key. The token is stored by the sensor in the OS keychain and is not shown to you.
