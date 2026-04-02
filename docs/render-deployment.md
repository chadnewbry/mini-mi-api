# Render Deployment

## Recommendation

Use a single Render web service for the first hosted deployment.

Why this is the best fit for the current backend:

- the API server and workers can run in one process
- Render supports Docker-based web services
- Render supports attaching a persistent disk to that web service
- the current backend still stores jobs, sessions, and generated assets on local disk

## Included Files

- `Dockerfile`
- `render.yaml`

## What The Deploy Uses

- one Docker web service
- health check at `/healthz`
- persistent disk mounted at `/data`
- embedded workers enabled with `MINIME_RUN_WORKERS=true`

## Required Secrets

Set these in Render before using real generation:

- `MINIME_DEVICE_TOKENS`
- `GEMINI_API_KEY`
- `XAI_API_KEY`

You can add others later if you change the generation stack.

## Deploy Flow

1. Push `main` to GitHub.
2. In Render, create a new Blueprint or new Web Service from the repo.
3. Use the included `render.yaml`.
4. Set the required secrets.
5. Deploy.

The public API URL will be your Render service URL, for example:

`https://mini-mi-api.onrender.com`

That is the URL the Mac app should use as `TONGUE_MINIME_BASE_URL`.

## Current Limits

This deploy shape is intentionally pragmatic, not final:

- local-disk persistence only
- single-service architecture
- no Postgres
- no object storage
- no queue service

It is the fastest path to a real hosted backend URL for shipped users.
