# Connecting to Claude Desktop

> Applies once the MCP protocol is implemented (see roadmap). The binary
> already starts in stdio mode.

1. Build the binary:

   ```sh
   make build   # produces ./bin/mcp-db-server
   ```

2. Create a read-only Postgres role (run as an admin):

   ```sql
   CREATE ROLE mcp_readonly LOGIN PASSWORD '<password>';
   GRANT CONNECT ON DATABASE <db> TO mcp_readonly;
   GRANT USAGE ON SCHEMA public TO mcp_readonly;
   GRANT SELECT ON ALL TABLES IN SCHEMA public TO mcp_readonly;
   ALTER ROLE mcp_readonly SET default_transaction_read_only = on;
   ```

3. Open Claude Desktop → Settings → Developer → Edit Config, and add the
   server to `claude_desktop_config.json`:

   ```json
   {
     "mcpServers": {
       "postgres-readonly": {
         "command": "/absolute/path/to/bin/mcp-db-server",
         "env": {
           "MCP_TRANSPORT": "stdio",
           "DATABASE_URL": "postgres://mcp_readonly:<password>@<host>:5432/<db>?sslmode=require",
           "MAX_ROWS": "500",
           "QUERY_TIMEOUT": "5s",
           "ALLOW_SCHEMAS": "public",
           "DENY_TABLES": "public.users",
           "AUDIT_LOG_PATH": "/absolute/path/to/audit.jsonl"
         }
       }
     }
   }
   ```

   This file holds a credential: keep it private and don't commit or share it.

4. Restart Claude Desktop. The tools appear under the tools (hammer) menu.

**Troubleshooting:** logs go to stderr, which Claude Desktop captures in its MCP
log files. Nothing but protocol messages may be written to stdout.
