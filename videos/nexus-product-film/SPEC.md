# Nexus product film

86 s, 1920x1080, 30 fps. Music: "EDM Detection Mode" (Kevin MacLeod, CC BY 4.0) entering at 2:05, so the drop lands on the reveal at 10.5 s. Scene cuts sit on bar lines (one bar = 1.875 s at 128 bpm).

Story: agents forget, context is scattered → Nexus → what it does → teams → download.

| Time | Scene | What is on screen | Caption |
| --- | --- | --- | --- |
| 0–10.5 | problem | Four agents asking for context they lost; instruction files drifting; everything collapses into one point | Your agents forget. / Context is scattered everywhere. |
| 10.5–14.25 | reveal | Mark draws, wordmark, "One shared memory for every coding agent." | |
| 14.25–25.5 | chat | Real chat: agent and model picker, sidebar of sessions from five agents, expanded work log, sub-agent run | Chat with any agent. / Watch every step it takes. |
| 25.5–33 | capture | Real web Timeline; sessions arriving from each agent | Every session, captured. / Every agent. Every project. |
| 33–44.25 | memory | Codex calling `memory_search` over the real Memory page; the project brief; instruction file sync | One memory every agent can query. / A brief every agent reads first. / Instruction files stay in sync. |
| 44.25–51.75 | graph | Real 3D knowledge graph; a stale node; the legend | See how it all connects. / It knows when code moves on. |
| 51.75–59.25 | linewhy | Code with line 42 picked; the `why` / `line_history` MCP answer; two lines inserted and the line still traced | Ask any line why. / Every change, traced to its session. |
| 59.25–64.875 | install | Real Agents page; agents without Nexus MCP; the Install Nexus into all agents button | One click. Every agent. |
| 64.875–72.375 | teams | Real Share chat dialog: live access for a teammate; "Share chat + code" | Share chats with your team. / Send the code along with it. |
| 72.375–76.125 | usage | Real Settings → Usage: totals, by agent, by model | Know what every chat costs. |
| 76.125–79.875 | private | Real chat with pasted keys shown as [REDACTED] | Secrets redacted. Native, not Electron. |
| 79.875–86 | endcard | Mark, wordmark, agent list, Download Nexus for Windows | |

Product scenes use real captures of the Nexus app running on demo data (`assets/shots/`). `tools/build_shots.py` writes chat, graph, install, teams, usage and private; edit the camera and highlight boxes there and re-run it. The code view in linewhy and the agent terminals are illustrations of an agent's side of the MCP calls, not Nexus UI.

Hero loop: cut from the render with `cut_loop.sh` `tools/cut_loop.sh renders/nexus-product-film.mp4 out "14.6:4.2 35.2:3.4 45.4:3.6 60.6:3.6 80.6:4.6"`.
