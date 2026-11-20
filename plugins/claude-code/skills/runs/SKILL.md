---
name: runs
description: "List this agent's recent AgentWatch runs with links to the console."
---

Call the `agentwatch_runs` tool from the `agentwatch` MCP server and show the result to the user.
Keep it short: lead with the answer, then the details that matter. If the tool says this machine is not signed in, tell the user to run `/agentwatch:setup`, which signs the machine in from here. If they prefer a terminal, give them the exact command the tool output names; never suggest a bare `agentwatch` command unless the output says it is on PATH.

