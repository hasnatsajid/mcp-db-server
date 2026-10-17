# Connecting to Claude Code

> Applies once the MCP protocol is implemented (see roadmap).

## stdio (local)

```sh
make build
claude mcp add postgres-readonly \
  --env MCP_TRANSPORT=stdio \
  --env DATABASE_URL='postgres://mcp_readonly:<password>@<host>:5432/<db>?sslmode=require' \
  --env MAX_ROWS=500 \
  -- /absolute/path/to/bin/mcp-db-server
```

Check it is connected:

```sh
claude mcp list
```

Inside a session, run `/mcp` to see the server's status and tools.

### Scope

- `--scope local` (default): only you, only this project.
- `--scope project`: written to `.mcp.json` so teammates get it. Don't put
  credentials there. Reference an env var instead, e.g.
  `"DATABASE_URL": "${DATABASE_URL}"`, and have each developer set it.

## HTTP (shared server)

Run the server with `MCP_TRANSPORT=http`, then:

```sh
claude mcp add --transport http postgres-readonly https://<your-host>/mcp
```

Put the HTTP transport behind TLS and authentication before exposing it
beyond localhost.
