# MCP server

`watchkeeper mcp` is a [Model Context Protocol](https://modelcontextprotocol.io) server on stdio. It lets an assistant such as Claude Code or Claude Desktop read your checks and incidents and start investigations, through the same REST API the dashboard uses.

It is a client of a running Watchkeeper: it needs the server's URL and, when the server has one, its API token. It has no database access of its own.

## Setup

Install the binary where the assistant runs:

```bash
go install github.com/DonBlack-117/watchkeeper/cmd/watchkeeper@latest
```

Claude Code:

```bash
claude mcp add watchkeeper \
  --env WK_URL=https://status.example.com \
  --env WK_API_TOKEN=<the server's WK_API_TOKEN> \
  -- watchkeeper mcp
```

Claude Desktop (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "watchkeeper": {
      "command": "watchkeeper",
      "args": ["mcp"],
      "env": {
        "WK_URL": "https://status.example.com",
        "WK_API_TOKEN": "<the server's WK_API_TOKEN>"
      }
    }
  }
}
```

| Variable       | Default                 | Meaning                    |
| -------------- | ----------------------- | -------------------------- |
| `WK_URL`       | `http://127.0.0.1:8090` | Watchkeeper server         |
| `WK_API_TOKEN` |                         | Bearer token of the server |

## Tools

| Tool                   | Read only | What it does                                                             |
| ---------------------- | --------- | ------------------------------------------------------------------------ |
| `get_summary`          | yes       | Status counts, open incidents, availability and p95 of the last 24 hours |
| `list_checks`          | yes       | Every check with its status and last result                              |
| `get_check_results`    | yes       | Latest runs of one check with per-phase timings                          |
| `list_incidents`       | yes       | Incidents with timelines and AI diagnoses; filter by open or by check    |
| `get_incident`         | yes       | One incident with its newest investigation                               |
| `investigate_incident` | no        | Starts an AI investigation on the server (uses API tokens, daily limit)  |
| `set_check_enabled`    | no        | Pauses or resumes a check, for example during maintenance                |

Example prompts: "What is down right now and why?", "Investigate incident 12 and tell me what to fix", "Pause the Bitso check for the maintenance window".

## Protocol

JSON-RPC 2.0, one message per line on stdin and stdout; logs go to stderr. Supported revisions: `2025-06-18`, `2025-03-26` and `2024-11-05`. API errors come back as tool results with `isError: true`, so the assistant can read them.
