# mini-mi-api

Standalone backend for Tongue's Mini Me generation flow.

This repo contains:

- the Go API server
- the optional background worker
- the Mini Me script-backed generation pipeline copied out of the Mac app repo
- smoke tests and fake generators for local verification
- a Render deployment path for the first hosted version
- an internal-service auth mode for running Mini Me behind `tongue-api` on AWS

## Current Shape

The backend currently supports:

- Cognito-backed bearer-token auth for app clients
- optional internal bearer-token auth for private worker deployments
- session creation, photo upload, candidate generation, selection, state generation, and asset download
- queued background jobs with job polling
- configurable session/job store backend: local file (default) or Postgres with optimistic concurrency
- script-backed generation using the Mini Me Go, Python, and ffmpeg pipeline in this repo

This is good enough for local development and a fast single-host deployment.

## Run Locally

```bash
cd /Users/chadnewbry/dev/mini-mi-api
go run ./cmd/minime-server
```

Default settings:

- port: `8088`
- data root: `.data`
- photo upload limit: `20 MB` per image
- accepted upload types: `png`, `jpeg`, `gif`, `heic`, `webp`

## Env Vars

- `MINIME_PORT`
- `PORT`
- `TONGUE_COGNITO_ISSUER`
- `TONGUE_COGNITO_CLIENT_ID`
- `TONGUE_COGNITO_JWKS_URL`
- `MINIME_DATA_ROOT`
- `MINIME_WORKER_COUNT`
- `MINIME_RUN_WORKERS`
- `MINIME_WORKER_POLL_INTERVAL_MS`
- `MINIME_JOB_TIMEOUT_SECONDS`
- `MINIME_AUTH_MODE`
  - supported values: `cognito` (default), `internal`, `cognito_or_internal`
- `MINIME_INTERNAL_BEARER_TOKEN`
  - required when `MINIME_AUTH_MODE=internal` or `MINIME_AUTH_MODE=cognito_or_internal`
- `MINIME_GENERATOR_MODE`
  - supported values: `placeholder` and `script`
- `MINIME_REPO_ROOT`
- `MINIME_IMAGE_GENERATOR_SCRIPT`
- `MINIME_PYTHON_EXECUTABLE`
- `MINIME_STATE_PIPELINE_SCRIPT`
- `MINIME_SCRIPT_RUNNER_MODE`
  - supported values: `local` (default) and `remote`
- `MINIME_SCRIPT_RUNNER_URL`
  - required when `MINIME_SCRIPT_RUNNER_MODE=remote`
- `MINIME_SCRIPT_RUNNER_TOKEN`
  - optional bearer token sent to the remote script runner
- `MINIME_STORE_BACKEND`
  - supported values: `file` (default) and `postgres`
- `MINIME_DATABASE_URL`
  - Postgres DSN for `MINIME_STORE_BACKEND=postgres`
  - if omitted, `DATABASE_URL` is used when present
- `MINIME_STORE_TABLE`
  - optional Postgres table name for persisted snapshot state (default: `minime_store_snapshots`)
- `MINIME_ASSET_BACKEND`
  - supported values: `file` (default) and `s3`
- `MINIME_ASSET_BUCKET`
  - required when `MINIME_ASSET_BACKEND=s3`
- `MINIME_ASSET_REGION`
  - optional region for S3-compatible storage (default: `us-east-1`)
- `MINIME_ASSET_ENDPOINT`
  - optional custom S3-compatible endpoint (for example MinIO or R2)
- `MINIME_ASSET_ACCESS_KEY_ID`
- `MINIME_ASSET_SECRET_ACCESS_KEY`
- `MINIME_ASSET_SESSION_TOKEN`
- `MINIME_ASSET_FORCE_PATH_STYLE`
  - optional (`true`/`false`), useful for local S3-compatible providers
- `MINIME_ASSET_KEY_PREFIX`
  - optional object key prefix
- `MINIME_ASSET_SIGNED_URL_TTL_SECONDS`
  - optional signed download URL TTL (default: `900`)
- `MINIME_ASSET_OBJECT_TAGGING`
  - optional URL-encoded object tags (for lifecycle policy filters), for example `ttl=30d&app=minime`

## Split API And Worker Mode

```bash
cd /Users/chadnewbry/dev/mini-mi-api
MINIME_RUN_WORKERS=false go run ./cmd/minime-server
```

In another shell:

```bash
cd /Users/chadnewbry/dev/mini-mi-api
go run ./cmd/minime-worker
```

## Script Mode

```bash
cd /Users/chadnewbry/dev/mini-mi-api
MINIME_GENERATOR_MODE=script go run ./cmd/minime-server
```

The script path is repo-local now. The remaining runtime dependencies are still external:

- `uv`
- Python 3
- `ffmpeg`
- the configured image generator script
- background removal tooling such as `rembg`, if you use that branch of the pipeline
- provider API keys such as `XAI_API_KEY`, depending on the scripts you point at

The default image generator script is now [scripts/generate_image.py](/Users/chadnewbry/dev/mini-mi-api/scripts/generate_image.py), which uses `GEMINI_API_KEY`.

### Remote Script Runner Mode

Set `MINIME_SCRIPT_RUNNER_MODE=remote` to dispatch script execution to Tongue API:

```bash
MINIME_GENERATOR_MODE=script \
MINIME_SCRIPT_RUNNER_MODE=remote \
MINIME_SCRIPT_RUNNER_URL=http://localhost:8080 \
MINIME_SCRIPT_RUNNER_TOKEN=dev-token \
go run ./cmd/minime-server
```

When enabled, Mini Me keeps its API/session flow unchanged but forwards script execution to `POST /v1/minime/scripts:run` on Tongue API. This is reversible by switching back to `MINIME_SCRIPT_RUNNER_MODE=local`.

## Internal Worker Mode

For the AWS migration, Mini Me can now run as a private worker service behind `tongue-api` instead of as a public Cognito-facing app backend.

Use `MINIME_AUTH_MODE=internal` to require a shared bearer token from `tongue-api`:

```bash
MINIME_AUTH_MODE=internal \
MINIME_INTERNAL_BEARER_TOKEN=replace-with-secret \
MINIME_STORE_BACKEND=postgres \
MINIME_ASSET_BACKEND=s3 \
MINIME_GENERATOR_MODE=script \
go run ./cmd/minime-server
```

In this mode, every request still uses the normal `Authorization: Bearer ...` header, but the token is a private service secret instead of a Cognito access token.

For migration overlap, use `MINIME_AUTH_MODE=cognito_or_internal` to accept either:

- direct Cognito-authenticated app traffic
- private `tongue-api` worker traffic with `MINIME_INTERNAL_BEARER_TOKEN`

The detailed AWS deployment shape is documented in [docs/aws-internal-deployment.md](/Users/chadnewbry/dev/mini-mi-api/docs/aws-internal-deployment.md).

## Smoke Test

```bash
cd /Users/chadnewbry/dev/mini-mi-api
bash scripts/smoke_test_script_mode.sh
```

For a deployed service:

```bash
cd /Users/chadnewbry/dev/mini-mi-api
MINIME_BASE_URL=https://your-service.onrender.com \
MINIME_ACCESS_TOKEN=your-cognito-access-token \
bash scripts/test_hosted_backend.sh
```

## API

- `POST /v1/minime/sessions`
- `GET /v1/minime/sessions/{id}`
- `POST /v1/minime/sessions/{id}/photos`
- `POST /v1/minime/sessions/{id}/bootstrap`
- `POST /v1/minime/sessions/{id}/candidates:generate`
- `POST /v1/minime/sessions/{id}/candidate-selection`
- `POST /v1/minime/sessions/{id}/states:generate`
- `GET /v1/minime/jobs/{jobId}`
- `GET /v1/minime/assets/{assetId}`

Candidate and state generation return immediately with `X-MiniMe-Job-ID`. Clients poll `GET /v1/minime/jobs/{jobId}` and refresh the session snapshot until the job reaches `completed` or `failed`.

When `MINIME_ASSET_BACKEND=s3`, session snapshot `download_url` fields are signed object-storage URLs and no longer point at `/v1/minime/assets/{assetId}`.

## Hosting

The best first host for the current architecture is Render, not Vercel.

Use the included deployment files:

- [Dockerfile](/Users/chadnewbry/dev/mini-mi-api/Dockerfile)
- [render.yaml](/Users/chadnewbry/dev/mini-mi-api/render.yaml)
- [docs/render-deployment.md](/Users/chadnewbry/dev/mini-mi-api/docs/render-deployment.md)
- [docs/aws-internal-deployment.md](/Users/chadnewbry/dev/mini-mi-api/docs/aws-internal-deployment.md)

This deploy shape runs the API and embedded workers together on one Render web service with a persistent disk for scratch workspace files.
The hosted defaults now use `4` workers with a `20 minute` per-job timeout so one hung generation run does not block the full queue indefinitely.
Production uses Postgres for session/job state and S3 for source photos, generated Mini Me bases, and generated state assets. The service returns signed S3 URLs in session snapshots when `MINIME_ASSET_BACKEND=s3`.

For app auth, the backend verifies the presented bearer token as a Cognito access token:

- `Authorization: Bearer <access-token>`
- signed by the Cognito JWKS for `TONGUE_COGNITO_ISSUER`
- `iss` must match `TONGUE_COGNITO_ISSUER`
- `token_use` must be `access`
- `client_id` must match `TONGUE_COGNITO_CLIENT_ID`
- `exp` must still be valid
- `sub` must be present

For internal worker auth, set:

- `MINIME_AUTH_MODE=internal`
- `MINIME_INTERNAL_BEARER_TOKEN=<shared-secret>`

For transition mode, set:

- `MINIME_AUTH_MODE=cognito_or_internal`
- Cognito env vars for direct app traffic
- `MINIME_INTERNAL_BEARER_TOKEN=<shared-secret>` for `tongue-api`

## Production Reality

This repo is now independent. Session/job state can run on local disk or Postgres:

- fastest deploy path: one hosted machine or one hosted container with embedded workers
- for multi-instance safety, use Postgres store mode (`MINIME_STORE_BACKEND=postgres`) so writes use versioned compare-and-swap persistence
- not a clean fit for Vercel as-is, because Vercel does not give this architecture a long-running shared process plus shared local disk
- for a private AWS worker behind `tongue-api`, use internal auth mode plus Postgres and S3 so the service can scale independently of a single host

If you want Vercel as the public entrypoint, the remaining required step is to externalize job execution:

- Postgres for sessions and jobs (now supported via `MINIME_STORE_BACKEND=postgres`)
- object storage for uploads and generated assets (now supported via `MINIME_ASSET_BACKEND=s3`)
- non-local job execution, either a separate worker service or a queue-backed runner

That work is outlined in [docs/vercel-deployment.md](/Users/chadnewbry/dev/mini-mi-api/docs/vercel-deployment.md).
