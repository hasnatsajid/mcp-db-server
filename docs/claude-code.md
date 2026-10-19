# Connecting to Claude Code

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

Run the server with `MCP_TRANSPORT=http` and an `HTTP_AUTH_TOKEN` (at least
32 characters, e.g. `openssl rand -hex 32`), then:

```sh
claude mcp add --transport http postgres-readonly https://<your-host>/mcp \
  --header "Authorization: Bearer ${MCP_DB_TOKEN}"
```

Keep the token in an environment variable (`MCP_DB_TOKEN` here) rather than
typing it into shell history. The server only checks the bearer token; put it
behind TLS (a reverse proxy) before exposing it beyond localhost.
`GET /healthz` is unauthenticated for load balancer checks.
