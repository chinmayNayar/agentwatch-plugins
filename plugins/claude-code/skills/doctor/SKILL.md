---
name: doctor
description: Check this machine's AgentWatch setup per coding agent and explain anything red.
---

Call the `agentwatch_doctor` tool from the `agentwatch` MCP server and show the result to the user.
Keep it short: lead with the answer, then the details that matter. If the tool says the
machine is not signed in, tell the user to run `agentwatch login` in a terminal.
For each red line, say in one sentence what to run to fix it.
