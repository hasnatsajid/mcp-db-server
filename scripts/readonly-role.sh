#!/usr/bin/env bash
# Creates the least-privilege role the server connects as. Run by the
# postgres image on first start (docker-entrypoint-initdb.d), after the
# sample schema. The password comes from MCP_READONLY_PASSWORD.
set -euo pipefail

: "${MCP_READONLY_PASSWORD:?set MCP_READONLY_PASSWORD in .env}"

psql -v ON_ERROR_STOP=1 \
	--username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
	-v ro_password="$MCP_READONLY_PASSWORD" <<'SQL'
CREATE ROLE mcp_readonly LOGIN PASSWORD :'ro_password';
ALTER ROLE mcp_readonly SET default_transaction_read_only = on;
ALTER ROLE mcp_readonly SET statement_timeout = '30s';
SELECT format('GRANT CONNECT ON DATABASE %I TO mcp_readonly', current_database()) \gexec
GRANT USAGE ON SCHEMA public TO mcp_readonly;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO mcp_readonly;
-- The sensitive tables are also denied by policy; revoking them here means
-- the database enforces it even if the policy is misconfigured.
REVOKE SELECT ON users, api_keys FROM mcp_readonly;
SQL
