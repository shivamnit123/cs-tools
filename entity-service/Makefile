# Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com). All Rights Reserved.
#
# This software is the property of WSO2 LLC. and its suppliers, if any.
# Dissemination of any information or reproduction of any material contained
# herein in any form is strictly forbidden, unless permitted by WSO2 expressly.
# You may not alter or remove any copyright or other notice from copies of this content.

.PHONY: build test lint migrate schema-bootstrap-sql

SHELL := /bin/bash
.ONESHELL:

build:
	go build ./...

test:
	go test ./...

lint:
	go vet ./...

# Applies migrations/*.sql in ascending numeric order (4-digit names sort
# correctly, no numeric-aware sort needed), tracked in
# csm_migration_applied_migration - same convention, same loop shape, as
# operations/csm-sync-service's own `make migrate`, since both services
# migrate the same shared database and must stay interchangeable here.
# Every migration file is plain SQL with no tracking statement of its own -
# this target records each one itself, in a separate statement, after that
# file's own statements succeed. Safe to re-run: a file already recorded is
# skipped.
migrate:
	set -a; [ -f .env ] && . ./.env; set +a; \
	if [ -n "$$DATABASE_URL" ]; then \
		conn="$$DATABASE_URL"; \
	else \
		export PGHOST="$$DB_HOST" PGPORT=$${DB_PORT:-5432} PGUSER="$$DB_USER" PGPASSWORD="$$DB_PASSWORD" PGDATABASE="$$DB_NAME" PGSSLMODE=$${DB_SSLMODE:-require}; \
		conn=""; \
	fi; \
	psql $$conn -v ON_ERROR_STOP=1 -c "CREATE TABLE IF NOT EXISTS csm_migration_applied_migration (filename TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now());" || exit 1; \
	for f in migrations/*.sql; do \
		name=$$(basename "$$f"); \
		name_escaped=$$(printf '%s' "$$name" | sed "s/'/''/g"); \
		applied=$$(psql $$conn -v ON_ERROR_STOP=1 -tAc "SELECT 1 FROM csm_migration_applied_migration WHERE filename = '$$name_escaped';"); \
		if [ "$$applied" = "1" ]; then \
			echo "skipping $$f (already applied)"; \
			continue; \
		fi; \
		echo "applying $$f"; \
		psql $$conn -v ON_ERROR_STOP=1 -f "$$f" || exit 1; \
		psql $$conn -v ON_ERROR_STOP=1 -c "INSERT INTO csm_migration_applied_migration (filename) VALUES ('$$name_escaped');" || exit 1; \
	done

# Combines migrations/*.sql into one file (dist/schema_bootstrap_<timestamp>.sql)
# for a manual run against a DB that has never run any of these before -
# first-time prod, or a staging reset. Mirrors
# operations/csm-sync-service's own script of the same name and output
# shape (including --since/--from/--to for a delta), so both repos' bootstrap
# files stay directly comparable. See scripts/generate_schema_bootstrap.sh.
schema-bootstrap-sql:
	./scripts/generate_schema_bootstrap.sh
