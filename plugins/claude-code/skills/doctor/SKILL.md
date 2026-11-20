---
name: doctor
description: "Check this machine's AgentWatch setup per coding agent and explain anything red."
---

Call the `agentwatch_doctor` tool from the `agentwatch` MCP server and show the result to the user.
Keep it short: lead with the answer, then the details that matter. If the tool says this machine is not signed in, tell the user to run `/agentwatch:setup`, which signs the machine in from here. If they prefer a terminal, give them the exact command the tool output names; never suggest a bare `agentwatch` command unless the output says it is on PATH.
For each red line, say in one sentence what to run to fix it, using the exact commands the output names.
