# Secure Document Sharing

Go API for encrypted document sharing (PostgreSQL, Redis, Azure Blob Storage, Azure Key Vault).

## Requirements
- Go (version in `go.mod`)
- PostgreSQL 16 (pgAdmin 4 recommended)
- Docker Desktop (for Redis and the Azurite blob-storage emulator)
- Optional: `brew install golang-migrate golangci-lint azure-cli`

## First-time setup
1. **Database.** In pgAdmin create a role and database:
   ```sql
   CREATE ROLE securedoc WITH LOGIN PASSWORD 'securedoc_dev';
   CREATE DATABASE securedocsharing OWNER securedoc;
   ```
2. **Settings.** `cp .env.example .env`, then edit the values. Never commit `.env`. Generate the two secrets
   and paste them into the empty `JWT_SECRET=` and `LOCAL_KEK=` lines:
   ```sh
   openssl rand -base64 48   # JWT_SECRET
   openssl rand -base64 32   # LOCAL_KEK
   ```
3. **Redis + Azurite.** `make up`
4. **Run.** `make run` (migrations are applied on startup).
5. **Check.** `curl http://localhost:8081/ready` reports `database`, `redis`, `blob_storage` and `key_management` as `ok`.

### Local vs real Azure
Locally, Azurite stands in for Blob Storage and `LOCAL_KEK` stands in for Key Vault. The app refuses
both outside `APP_ENV=development|test`. To use real Azure: `az login`, run `scripts/azure-setup.sh`,
then make the `.env` changes it prints. The app authenticates with your `az login` locally and with
Managed Identity when deployed; no storage keys are used.

## Auth API
| Endpoint | Notes |
|---|---|
| `POST /api/v1/auth/register` | `{"email","password","accept_terms"}`. Always `202` for valid input, so it cannot reveal which emails exist. Password 12–128 characters. |
| `POST /api/v1/auth/login` | `{"email","password"}`. Returns a 15-minute access token; sets the 7-day refresh token as an `HttpOnly`, `SameSite=Strict` cookie. 5/min per IP, 10/15 min per account. |
| `POST /api/v1/auth/refresh` | Uses the cookie. Rotates the refresh token; replaying an old one revokes all of the user's sessions. |
| `POST /api/v1/auth/logout` | Revokes the refresh token and, if sent, denies the bearer token immediately. |
| `GET /api/v1/auth/me` | `Authorization: Bearer <access_token>` |

Responses use `{"success": bool, "data": ..., "error": {"code", "message", "field"}}`.

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
- `internal/{config,database,handler,model,repository,service}` application code
- `internal/{auth,ratelimit,storage,keys}` password/token primitives, rate limiter, Blob Storage and Key Vault clients
- `migrations/` SQL migrations (golang-migrate)
- `scripts/azure-setup.sh` creates the Azure resources
- `.github/workflows/ci.yml` runs checks on every push