# Secure Document Sharing

Go API for encrypted document sharing (PostgreSQL, Redis, Azure Blob Storage, Azure Key Vault).

## Requirements
- Go (version in `go.mod`)
- PostgreSQL 16 (pgAdmin 4 recommended)
- Docker Desktop (for Redis)
- Optional: `brew install golang-migrate golangci-lint azure-cli`

## First-time setup
1. **Database.** In pgAdmin create a role and database:
   ```sql
   CREATE ROLE securedoc WITH LOGIN PASSWORD 'securedoc_dev';
   CREATE DATABASE securedocsharing OWNER securedoc;
   ```
2. **Settings.** `cp .env.example .env`, then edit the values. Never commit `.env`.
3. **Redis.** `make up`
4. **Run.** `make run` (migrations are applied on startup).
5. **Check.** `curl http://localhost:8081/health` returns `{"status":"ok"}`.

## Everyday commands
| Command | What it does |
|---|---|
| `make run` | Start the API |
| `make test` | Tests with the race detector |
| `make cover` | Tests plus coverage summary |
| `make check` | Format, vet and test |
| `make lint` | golangci-lint (or go vet if not installed) |
| `make vuln` | Dependency vulnerability scan |
| `make migrate-test` | Prove the migration is reversible (up, down, up) |

## Layout
- `cmd/api` entry point
- `internal/{config,database,handler,model,repository}` application code
- `migrations/` SQL migrations (golang-migrate)
- `scripts/azure-setup.sh` creates the Azure resources
- `.github/workflows/ci.yml` runs checks on every push