# Sign in this machine

AgentWatch signs a machine in with a **device code**: the editor shows a short code, you approve it in the AgentWatch console, and a token scoped to this device is stored in your OS keychain.

The token can report sessions and steps and ask for pre-action checks. It **cannot** approve held calls, isolate agents or change policy, so an agent on this machine cannot use it to approve its own actions.
