# dry_run first

In `dry_run` AgentWatch records what it *would* have blocked and blocks nothing, except tools on the agent's denied list and edits to AgentWatch's own hook files.

When you are ready, switch in a terminal (it asks you to confirm):

```
agentwatch mode enforce
```

Agents cannot change the mode: AgentWatch blocks them from running that command.
