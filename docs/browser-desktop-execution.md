# Authenticated browser-to-desktop execution

The browser uses the existing account JWT to submit a typed request over HTTPS.
Agent/MCP API tokens are explicitly rejected for all desktop channel endpoints.
Desktop makes outbound HTTPS requests only. The retired `:7272` localhost
execution bridge is not used or re-enabled.

## Trust boundary

1. Desktop opts in through Settings (default off), registers its OS-protected
   device identity and the project IDs bound to approved local workspaces.
2. Web selects an online device and an installed supported harness. It submits
   a session, model and prompt—not a command, executable or workspace path.
3. The API verifies project membership, session ownership, device ownership,
   project mapping and harness capability. A separate device key plus account
   JWT is required to claim or update a job.
4. Desktop claims once and requests native approval for that single turn,
   showing harness, model, local folder and prompt. No executor runs on decline.
5. The worker rechecks the local mapping after approval, resolves the folder
   locally, seeds authorized conversation context and calls the existing
   console-free streaming executor. Captured history is data, never instructions
   for granting permission.
6. Coalesced progress snapshots carry text, tool activity and final file diffs.
   Browser SSE uses an Authorization header, not a URL token. Reconnection
   resumes observation; it never resubmits a prompt.

## Durable lifecycle

Migration `039_desktop_execution` stores devices and jobs in PostgreSQL.
Only an explicit in-memory development store uses an in-memory broker.
State is `queued → awaiting_approval → running → completed/failed`.
Cancellation and expiry are terminal, enforced with revision compare-and-swap.
The DB allows only one outstanding remote request per device across replicas.
Approval expires in three minutes; a running turn is bounded to twenty minutes.
A claimed request is never re-queued or reassigned automatically.

Workers observe cancellation every second and stop after repeated connection
loss. UI disconnect is not cancellation: reconnect or use Stop. Turning off
browser requests or signing out cancels the native worker. File changes already
made are not rolled back.

Device credentials are excluded from responses and protected like the account
token (DPAPI on Windows). Neither remote paths nor secret-bearing command lines
are stored in device advertisements. Model discovery is a separate read-only
request and does not start a paid user turn or switch providers automatically.

## Verification and rollout

- Race tests: claim once, owner/device isolation, stale updates, queue bounds,
  decline-before-execution, live progress before completion and cancellation.
- Disposable PostgreSQL test: migration, concurrent admission, atomic claim,
  durability across repository instances and late-completion rejection.
- Browser build plus readable-content/diff tests; native build plus shutdown
  tests. Full signed-in browser/native acceptance remains a rollout gate.
- Deploy server/migration before enabling the matching web controls. Do not
  advertise working remote execution on an older server returning 404.

Temporary transport snapshots are deleted seven days after their execution
deadline during the owning desktop's next heartbeat. This never deletes
canonical conversation history. Offline devices do not run cleanup until they
reconnect; an operator may apply the same scoped retention query independently.

Current limits: remote attachment transport is not yet implemented. Keep the
channel disabled in a public rollout until signed-in end-to-end checks and a
security review pass.
