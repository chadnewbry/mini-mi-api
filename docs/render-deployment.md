# Render Deployment

## Recommendation

Use a single Render web service for the first hosted deployment.

Why this is the best fit for the current backend:

- the API server and workers can run in one process
- Render supports Docker-based web services
- Render supports attaching a persistent disk for scratch workspace files
- production state is externalized to Postgres, and generated/uploaded assets are stored in S3

## Included Files

- `Dockerfile`
- `render.yaml`

## What The Deploy Uses

- one Docker web service
- health check at `/healthz`
- persistent disk mounted at `/data` for scratch files only
- embedded workers enabled with `MINIME_RUN_WORKERS=true`
- `MINIME_STORE_BACKEND=postgres` for session/job state
- `MINIME_ASSET_BACKEND=s3` for source photos, candidate bases, and generated state assets

## Required Secrets

Set these in Render before using real generation:

- `MINIME_DATABASE_URL`
- `MINIME_ASSET_BUCKET`
- `MINIME_ASSET_ACCESS_KEY_ID`
- `MINIME_ASSET_SECRET_ACCESS_KEY`
- `TONGUE_COGNITO_ISSUER`
- `TONGUE_COGNITO_CLIENT_ID`
- `GEMINI_API_KEY`
- `XAI_API_KEY`

`TONGUE_COGNITO_JWKS_URL` is optional; when omitted, the service derives it from `TONGUE_COGNITO_ISSUER`.

The S3 credentials should belong to a least-privilege IAM principal that can only read and write the configured Mini Me bucket/prefix. The production service currently uses private S3 objects and returns signed URLs in session snapshots.

## Deploy Flow

1. Push `main` to GitHub.
2. In Render, create a new Blueprint or new Web Service from the repo.
3. Use the included `render.yaml`.
4. Set the required secrets.
5. Deploy.
6. Run the hosted smoke test:

```bash
cd /Users/chadnewbry/dev/mini-mi-api
MINIME_BASE_URL=https://your-render-url.onrender.com \
MINIME_ACCESS_TOKEN=your-cognito-access-token \
bash scripts/test_hosted_backend.sh
```

The public API URL will be your Render service URL, for example:

`https://mini-mi-api.onrender.com`

That is the URL the Mac app should use as `TONGUE_MINIME_BASE_URL`.

## Current Limits

This deploy shape is intentionally pragmatic, not final:

- single-service architecture
- no queue service

It is still a single hosted web service, but durable state and asset bytes no longer depend on Render local disk.
