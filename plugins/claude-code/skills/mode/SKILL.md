---
name: mode
description: Explain AgentWatch's current mode (dry_run or enforce) and how a person changes it.
---

Call the `agentwatch_mode` tool from the `agentwatch` MCP server and show the result to the user.
Keep it short: lead with the answer, then the details that matter. If the tool says the
machine is not signed in, tell the user to run `agentwatch login` in a terminal.
You cannot change the mode yourself: AgentWatch blocks agents from running `agentwatch mode`. Tell the user the exact command to run in their own terminal.
