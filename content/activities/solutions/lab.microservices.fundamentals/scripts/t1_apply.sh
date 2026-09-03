#!/bin/bash
# lab.microservices.fundamentals t1: from ~/app/monolith.json's `users`
# section, create ~/app/users-service.json with only name/port/db_table,
# no `orders` reference. Bootstraps monolith.json (fx.monolith-config.v1
# has no handler). Validators: file exists; valid JSON; no "orders".
set -uo pipefail
mkdir -p ~/app
[ -f ~/app/monolith.json ] || cat > ~/app/monolith.json <<'MJ'
{
  "app": "shop",
  "users":  { "name": "users",  "port": 8001, "db_table": "users" },
  "orders": { "name": "orders", "port": 8002, "db_table": "orders" },
  "database": { "host": "db", "port": 5432 }
}
MJ
jq '{name: .users.name, port: .users.port, db_table: .users.db_table}' ~/app/monolith.json > ~/app/users-service.json
grep -q orders ~/app/users-service.json && { echo "orders leaked" >&2; exit 1; }
echo "users-service.json written"
