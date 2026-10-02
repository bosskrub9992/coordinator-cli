# M0 spike A: Worker headless protocol (items 1, 2, 8)

Run on 2026-10-01 against claude 2.1.285, with --model claude-sonnet-5-5 --effort low. Raw logs are in runs/*.jsonl (one line per record: {t, dir: in|out|stderr|note, msg}).

Scripts in this folder:
- drive.py: scenario runner. Spawns claude, writes stdin JSON lines, waits for event types. Strips ORCA_*, HERDR_* and the parent session's CLAUDE* vars from the child env.
- coord_mcp.py: stdio MCP server with one tool, `permission`. Supports a policy file, delays, progress heartbeats and concurrent calls (one thread each).
- show.py / show8.py: log pretty-printers.
- scenarios/*.json: the scenarios.

| Item | Verdict | One line |
|---|---|---|
| 1. Long-lived stream-json session | PASS | One process handles many turns. A message sent mid-run is injected into the current turn. The interrupt control request ends the turn and the process survives. --session-id followed by --resume works. |
| 2. --permission-prompt-tool via local MCP | PASS, with required changes | Request and response shapes confirmed. updatedInput rewrite works. Holds of 20 s and 330 s work. A silent hold is aborted by the MCP idle timeout, so heartbeats are required. --permission-mode default is required because the user's defaultMode is auto. |
| 8. MCP visibility in headless | PASS, one caveat | A headless Worker in the workspace sees exactly what `claude mcp list` shows there (user servers, claude.ai connectors, per-folder disabled state), plus coord. Two concurrent Workers each opened and closed their own Playwright tab. Caveat: each Playwright connection opens an extension "Welcome" connect tab in the Captain's Chrome, and I could not verify that it closes. |

---

### Item 1: long-lived stream-json session (PASS)

Command (base flags in drive.py):

```
claude -p --input-format stream-json --output-format stream-json --verbose --model claude-sonnet-5-5 --effort low --max-turns 3 --session-id <uuid> [--replay-user-messages]
```

**Stdin framing**

One JSON object per line. `content` can be a string or an array of content blocks; both work:

```json
{"type":"user","message":{"role":"user","content":"Reply with exactly the word ONE and nothing else."}}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Reply with exactly the word TWO"}]}}
```

**Interrupt**

Send:

```json
{"type":"control_request","request_id":"req_int_1","request":{"subtype":"interrupt"}}
```

Reply, within about 10 ms:

```json
{"type":"control_response","response":{"subtype":"success","request_id":"req_int_1","response":{"still_queued":[]}}}
```

Then, in order:
1. A synthetic tool_result: "The user doesn't want to proceed with this tool use… STOP what you are doing and wait for the user…"
2. A user text block: "[Request interrupted by user for tool use]"
3. A result: `{"type":"result","subtype":"error_during_execution","is_error":true,"stop_reason":"tool_use","terminal_reason":"aborted_tools",…}`

The process stays alive, and the next user message runs normally (result_index 1).

**Control requests that cost no tokens and work before any turn** (runs/s8_W0_probe.jsonl)
- `{"subtype":"initialize"}` returns commands, agents, models, account, pid, current_permission_mode, session_state, and more.
- `{"subtype":"mcp_status"}` returns `{"mcpServers":[{"name","status",…}]}`.

**Observed**
- The process stays alive between turns. In s1 it idled 5 s, ran the next message in the same process, and exited 0 when stdin closed.
- The `result` event marks the end of a turn. Its fields: subtype (success / error_during_execution / …), is_error, terminal_reason (completed / aborted_tools), result (final text), result_index (0, 1, 2… per turn in this process), num_turns, permission_denials[], session_id, total_cost_usd.
- JSON key order is not stable. "type":"result" is NOT the first key, so parse by field, never by prefix.
- system/init is emitted at the start of every turn, not once. No init appears until the first user message is read. SessionStart hook events (system/hook_started, hook_response) come before it.
- A message sent during a tool call is injected into the running turn (s4). The steer arrived while `sleep 12` ran. It was delivered right after the tool_result, the same turn's reply obeyed it ("DONE PINEAPPLE"), and only one result was emitted. With --replay-user-messages, the echo (isReplay:true) appears when the message is consumed, not when it is sent, so it works as a delivery receipt.
- A message sent while a permission prompt is pending is held (s6b). It was not consumed until the interrupt, then ran as the next turn (result_index 1). still_queued was still [].
- Closing stdin mid-turn (s9) lets the current turn finish and emit its result, then the process exits 0. This is the graceful way to end a Worker.
- Session ids:
  - --session-id <uuid> is honoured; init and result carry it.
  - A fresh process with --resume <uuid> remembered both earlier turns ("ONE, TWO") and kept the SAME session_id.
  - Reusing --session-id <uuid> for an existing session fails immediately with stderr "Error: Session ID … is already in use." and exit code 1.
- total_cost_usd is cumulative, both within the process and across --resume. The resumed run reported 0.233, which includes the earlier 0.149.

**What Go must do (item 1)**
- Write one JSON object plus "\n" per message. Keep stdin open for the Worker's whole life; closing it means "finish and exit".
- Read stdout line by line and switch on type/subtype. Keep unknown types (rate_limit_event, system/task_started, task_notification, …) as passthrough log entries.
- Treat a Task as idle / awaiting input when a result arrives and no steer is in flight.
- Send a steer as just another user line. Use --replay-user-messages and match the echo to mark it delivered.
- Send interrupt as the control_request above with a unique request_id. Match the control_response, then expect a result with terminal_reason aborted_tools (or similar).
- Use --session-id exactly once, at first launch. Always use --resume on relaunch.
- Compute per-turn cost as the difference between consecutive total_cost_usd values.
- Feature-detect with init.capabilities. 2.1.285 reports: interrupt_receipt_v1, interrupt_cancel_queued_v1, msg_lifecycle_v1, mcp_read_resource_v1, mcp_tool_ui_meta_v1.
- Use the mcp_status control request for the startup health check instead of waiting for the first init.

---

### Item 2: --permission-prompt-tool via a local MCP server (PASS, with required changes)

Command (scenarios/s5_perm.json):

```
claude -p --input-format stream-json --output-format stream-json --verbose --model claude-sonnet-5-5 --effort low --permission-mode default --mcp-config scenarios/s5_mcp.json --permission-prompt-tool mcp__coord__permission
```

This does NOT use --strict-mcp-config.

MCP config:

```json
{"mcpServers":{"coord":{"type":"stdio","command":"python3","args":[".../coord_mcp.py"],"env":{"COORD_LOG":"…","COORD_POLICY":"…"}}}}
```

**Handshake from the client**
1. initialize with protocolVersion "2025-11-25", capabilities {roots, elicitation}, clientInfo claude-code 2.1.285.
2. notifications/initialized.
3. tools/list.

**Request the tool receives** (exact, from runs/s5_mcp.log)

```json
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"permission","arguments":{"tool_name":"Bash","input":{"command":"touch perm-allow.txt","description":"Create empty file perm-allow.txt"},"tool_use_id":"toolu_01GtMWNRxHNJxY32j2qNQ6q8"},"_meta":{"claudecode/toolUseId":"toolu_01GtMWNRxHNJxY32j2qNQ6q8","progressToken":2}}}
```

Other tools have the same shape:
- Write: `{"tool_name":"Write","input":{"file_path":"/abs/path","content":"hi"},…}`
- MCP tool: `{"tool_name":"mcp__playwright__browser_tabs","input":{"action":"new"},…}`

No other fields arrive: no permission suggestions, no decision reason.

**Response shape**

A normal tool result whose single text block is a JSON string:

```json
{"content":[{"type":"text","text":"{\"behavior\":\"allow\",\"updatedInput\":{...}}"}]}
{"content":[{"type":"text","text":"{\"behavior\":\"deny\",\"message\":\"Captain said no: do not create that file. Do not retry.\"}"}]}
```

| Case | Result |
|---|---|
| allow, updatedInput = same input | Command ran. |
| allow, no updatedInput | Ran with the original input. |
| allow, updatedInput {} | Ran with the ORIGINAL input (Write produced "hi"). Empty means unchanged. |
| allow, rewritten updatedInput (touch perm-rewritten.txt) | The rewritten command ran; only perm-rewritten.txt exists. The stream's assistant.tool_use still shows the ORIGINAL input, so the rewrite is invisible in the stream. |
| deny + message | Model sees tool_result {is_error:true, content:"<message>"} word for word. result.permission_denials[] gets {tool_name, tool_use_id, tool_input}. The model obeyed "do not retry". |
| text that is not JSON | tool_result error: "The permission prompt tool returned an invalid permission result. Expected {behavior: 'allow', updatedInput?: object} or {behavior: 'deny', message: string}." Counted as a denial. |

**Holding the request open** (how a Worker pauses until answered)
- Holds of 20 s (s5) and 330 s (s6a) both succeeded.
- The stream is completely silent while a request is pending. No event says "waiting for permission".
- A silent hold is aborted by the MCP idle timeout (s7a). With CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=15000 set, it aborted after 30 s. I did not check why 30 s rather than 15 s. The documented stdio default is 30 min.
  - The tool error was: `<tool_use_error>Error calling tool (Bash): MCP server "coord" tool "permission" sent no response or progress for 30s; aborting. If this server is configured in your MCP settings, set a per-server "timeout" (ms) to allow longer silent runs for just this server; otherwise set CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT (ms) globally (0 disables).</tool_use_error>`
  - The command did not run. The abort was NOT recorded in permission_denials. No notifications/cancelled was sent.
- The same hold with progress notifications every 5 s succeeded (s7b: 40 s hold, 7 heartbeats). Heartbeat frame:

  ```json
  {"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":<params._meta.progressToken>,"progress":<n>,"message":"waiting for Captain"}}
  ```
- MCP_TOOL_TIMEOUT (the overall cap) defaults to about 28 h for stdio.
- An interrupt while a request is pending (s6b) ends the turn at once. Claude Code sends:

  ```json
  {"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":2,"reason":"AbortError: remote-cancel"}}
  ```

  The call counts as a denial and the process stays alive. My first single-threaded server never read this notification because it was blocked in the hold, so the server must read stdin concurrently.

**Gotchas (item 2)**
- The default permission mode is auto. The Captain's ~/.claude/settings.json has defaultMode "auto", and an unflagged -p run reported permissionMode "auto". In auto mode a classifier decides, and the prompt tool is only a fallback. Workers must pass --permission-mode default (or dontAsk plus explicit allow rules) to get every prompt.
- Read-only commands and anything already allowed never reach the tool.
- Child processes inherit the parent session's env: CLAUDECODE, CLAUDE_CODE_SESSION_ID, CLAUDE_CODE_CHILD_SESSION, CLAUDE_CODE_MESSAGING_*, CLAUDE_EFFORT, and all the ORCA_* hook vars.
  - The Captain's user-scope hooks are Orca and herdr hooks, which report to ORCA_AGENT_HOOK_PORT / ORCA_PANE_KEY.
  - My first eight runs (s1–s6b's first run) did not scrub these, so their hooks probably posted events to the Coordinator's own Orca pane. Harmless but untidy. drive.py scrubs them from s6b's second run on.

**What Go must do (item 2)**
- The coord MCP server must:
  - Handle each tools/call concurrently (goroutine per call) and keep reading stdin.
  - Honour notifications/cancelled by requestId: mark the escalation withdrawn and drop any later answer for that id.
  - Send notifications/progress with the request's progressToken every 10–15 s while waiting for the Captain.
  - As a second safeguard, also set a large per-server "timeout" in the generated --mcp-config, or CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=0 in the Worker env.
- The answer is one text block: `{"behavior":"allow","updatedInput":<obj>}` or `{"behavior":"deny","message":"…"}`. Always send the full updatedInput, never {} as a shortcut.
- The supervisor, not the stream, owns the needs-decision state. The MCP server must tell the supervisor, because nothing appears in stream-json.
- Log any updatedInput rewrite in events.jsonl yourself; the stream shows only the original input.
- Always launch Workers with --permission-mode default.
- Build the Worker env from scratch, or at least remove: ORCA_*, HERDR_*, CLAUDECODE, CLAUDE_CODE_SESSION_ID, CLAUDE_CODE_CHILD_SESSION, CLAUDE_CODE_MESSAGING_SOCKET, CLAUDE_CODE_MESSAGING_TOKEN, CLAUDE_PID, CLAUDE_CODE_SESSION_ATTENDED, CLAUDE_EFFORT, CLAUDE_CODE_ENTRYPOINT, CLAUDE_CODE_EXECPATH.
  - Otherwise Workers report as the Coordinator's Orca pane and count as "nested" sessions.
  - CLAUDE_CODE_FORCE_SESSION_PERSISTENCE=1 is already in the Captain's settings env, so transcripts persist either way.

---

### Item 8: MCP visibility in headless (PASS, one caveat)

**`claude mcp list` from the workspace** (runs/s8_mcp_list_workspace.txt)

| Server | Status |
|---|---|
| a code-search MCP server, an admin MCP server (beta and prod), a database MCP server (prod and nonprod), playwright (user scope) | Connected |
| claude.ai Mermaid Chart, Atlassian, Slack, Google Drive | Connected |
| claude.ai Sentry, Atlassian Rovo | Needs authentication |
| claude.ai Microsoft 365, Docusign, Lucid, Figma, Canva, Excalidraw, Google Calendar, Gmail, Atlassian (2) | Disabled for this project |

**Headless Worker in the workspace**

Launched with cwd = the workspace, --mcp-config for coord, --permission-mode default, --permission-prompt-tool mcp__coord__permission, and CLAUDE_CODE_DISABLE_AUTO_MEMORY=1.
- mcp_status (before any turn) and system/init both showed the identical set and statuses, plus coord: connected.
- The per-folder disabled connectors show "status":"disabled" and contribute no tools.
- init listed 156 mcp__* tools.
- memory_paths was null, so auto memory is off.

**Gotchas (item 8)**
- Without --mcp-config, the first init shows user servers as "pending" and lists no claude.ai connectors at all (s1). With --mcp-config, -p waits for all servers before turn 1 (about 3–4 s here) and init is complete. Workers always pass --mcp-config, so this is fine, but never treat an init from a run without it as "what the Worker can see".
- MCP tools are deferred, so Workers call ToolSearch "select:…" first. ToolSearch is auto-allowed.

**Playwright with two concurrent Workers** (s8_W4, s8_W5, started 1 s apart)

The coord policy allowed only mcp__playwright__browser_tabs and denied everything else. Each Worker ran: list → new → list → close its own index → list.
- Each Worker's connection sees ONLY its own tabs:
  - index 0 is the extension's "Welcome" connect page (chrome-extension://…/connect.html?mcpRelayUrl=…)
  - index 1 is its new about:blank tab
- Both closed index 1 and ended with only their own Welcome tab listed.
- Neither saw the other's tabs or any of the Captain's tabs, so closing by index stays inside the Worker's own connection.
- An earlier variant called `new` with url "about:blank#coord-m0-Wn" (s8_W1, s8_W2). It failed in both Workers with "Target page, context or browser has been closed".
  - The solo run without a url (s8_W3) and the concurrent runs without a url all succeeded.
  - So the failure is most likely the about:blank#hash navigation, not concurrency. I did not isolate it.
- Caveat I could not contain or verify:
  - Every Playwright connection opens a "Welcome" connect tab in the Captain's real Chrome. Five Worker connections ran (W1–W5).
  - I can't see whether the extension closes those tabs when the Worker's @playwright/mcp process exits. The Captain should check Chrome for stray "Welcome" tabs.
  - If they persist, the supervisor or the Brief should close them at Task end (for example, a final browser_tabs close of index 0).
- Nothing else was done in the browser.

**What Go must do (item 8)**
- Pass --mcp-config <coord.json> and never --strict-mcp-config.
- Launch the Worker with cwd = the Launch folder, so the per-folder disabled state applies. That state lives in ~/.claude.json at projects[<cwd>].disabledMcpServers.
- Run mcp_status at startup, record the server list in events.jsonl, and warn on needs-auth or failed.
- Add a Playwright rule to the Brief: open your own tab with browser_tabs new and close it when done. Avoid `new` with a url until that failure is understood; open the tab first, then navigate.
- Grant Playwright actions through the coord permission tool.

---

### Run index (runs/)

| Run | What it tests |
|---|---|
| s1_multiturn | Two turns in one process, --session-id |
| s2_resume | --resume keeps context and session id |
| s2b_reuse_sid | Reusing --session-id errors |
| s3_interrupt | Interrupt during Bash, then a follow-up turn |
| s4_midrun_followup | A steer sent mid-tool joins the same turn |
| s5_perm_default_deny | Deny shape (the policy file was missing, so everything was denied) |
| s5_perm | Allow, rewrite, deny, and a 20 s hold |
| s6a | 330 s hold |
| s6b (+ _singlethread) | Interrupt during a pending permission; notifications/cancelled |
| s6c | Allow without updatedInput, with {}, and invalid text |
| s7a | Idle-timeout abort with no heartbeat |
| s7b | Same hold succeeds with progress heartbeats |
| s8_W0_probe | initialize + mcp_status control requests in the workspace |
| s8_W1..W5 | Playwright tab tests |
| s9_close_midturn | Closing stdin mid-turn finishes the turn, exit 0 |

The only leftovers are empty perm-*.txt files in scratch/. No repo, config or workspace file was modified. The Playwright connect tabs are covered by the caveat above.
