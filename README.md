# mini-mi-api

Standalone backend for Tongue's Mini Me generation flow.

This repo contains:

- the Go API server
- the optional background worker
- the Mini Me script-backed generation pipeline copied out of the Mac app repo
- smoke tests and fake generators for local verification
- a Render deployment path for the first hosted version

## Current Shape

The backend currently supports:

- Supabase-backed bearer-token auth for app clients
- session creation, photo upload, candidate generation, selection, state generation, and asset download
- queued background jobs with job polling
- local-disk persistence for sessions, jobs, and generated assets
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
- `SUPABASE_URL`
- `SUPABASE_ANON_KEY`
- `MINIME_DATA_ROOT`
- `MINIME_WORKER_COUNT`
- `MINIME_RUN_WORKERS`
- `MINIME_WORKER_POLL_INTERVAL_MS`
- `MINIME_JOB_TIMEOUT_SECONDS`
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

## Smoke Test

```bash
cd /Users/chadnewbry/dev/mini-mi-api
bash scripts/smoke_test_script_mode.sh
```

For a deployed service:

```bash
cd /Users/chadnewbry/dev/mini-mi-api
MINIME_BASE_URL=https://your-service.onrender.com \
SUPABASE_ACCESS_TOKEN=your-supabase-access-token \
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

## Hosting

The best first host for the current architecture is Render, not Vercel.

Use the included deployment files:

- [Dockerfile](/Users/chadnewbry/dev/mini-mi-api/Dockerfile)
- [render.yaml](/Users/chadnewbry/dev/mini-mi-api/render.yaml)
- [docs/render-deployment.md](/Users/chadnewbry/dev/mini-mi-api/docs/render-deployment.md)

This deploy shape runs the API and embedded workers together on one Render web service with a persistent disk.
The hosted defaults now use `4` workers with a `20 minute` per-job timeout so one hung generation run does not block the full queue indefinitely.

For app auth, the backend verifies the presented bearer token by calling:

`GET {SUPABASE_URL}/auth/v1/user`

with:

- `Authorization: Bearer <token>`
- `apikey: <SUPABASE_ANON_KEY>`

## Production Reality

This repo is now independent, but the current storage model is still local-disk based. That means:

- fastest deploy path: one hosted machine or one hosted container with embedded workers
- not yet ready for true horizontally scaled multi-instance deployment
- not a clean fit for Vercel as-is, because Vercel does not give this architecture a long-running shared process plus shared local disk

If you want Vercel as the public entrypoint, the next required step is to externalize state:

- Postgres for sessions and jobs
- object storage for uploads and generated assets
- non-local job execution, either a separate worker service or a queue-backed runner

That work is outlined in [docs/vercel-deployment.md](/Users/chadnewbry/dev/mini-mi-api/docs/vercel-deployment.md).
