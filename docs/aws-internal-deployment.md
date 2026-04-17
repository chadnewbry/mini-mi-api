# AWS Internal Deployment

This is the deployment shape for running `mini-mi-api` as a private worker service behind `tongue-api` on AWS.

## Target Shape

- `tongue-api` stays the public API boundary.
- `mini-mi-api` runs on ECS or Fargate in private subnets.
- ingress to `mini-mi-api` is limited to `tongue-api` security groups or an internal load balancer.
- `mini-mi-api` keeps its existing HTTP API and job model, but auth switches from Cognito to a private shared bearer token.
- durable state uses Postgres and S3 instead of local disk.

## Required Env

```bash
MINIME_AUTH_MODE=internal
MINIME_INTERNAL_BEARER_TOKEN=replace-with-secrets-manager-value
MINIME_STORE_BACKEND=postgres
MINIME_DATABASE_URL=postgres://...
MINIME_ASSET_BACKEND=s3
MINIME_ASSET_BUCKET=your-private-bucket
MINIME_ASSET_REGION=us-east-1
MINIME_GENERATOR_MODE=script
MINIME_RUN_WORKERS=true
MINIME_WORKER_COUNT=4
MINIME_JOB_TIMEOUT_SECONDS=1200
```

Keep `MINIME_INTERNAL_BEARER_TOKEN` in AWS Secrets Manager or another secret store. `tongue-api` should send it as:

```http
Authorization: Bearer <shared-secret>
```

## Optional Transition Mode

If you still need direct Cognito-authenticated traffic during cutover, use:

```bash
MINIME_AUTH_MODE=cognito_or_internal
MINIME_INTERNAL_BEARER_TOKEN=replace-with-secrets-manager-value
TONGUE_COGNITO_ISSUER=https://...
TONGUE_COGNITO_CLIENT_ID=...
```

That allows both:

- app clients presenting Cognito access tokens
- `tongue-api` presenting the internal bearer token

Once all traffic is flowing through `tongue-api`, switch back to `MINIME_AUTH_MODE=internal`.

## Networking

Recommended AWS shape:

- private ECS service for `mini-mi-api`
- service discovery name or internal ALB target for `tongue-api` to call
- security group ingress only from `tongue-api`
- no public listener and no public DNS

This keeps the current Mini Me HTTP surface usable while making the service an internal worker instead of another public edge.

## Persistence

For internal AWS deployment, do not rely on local disk as the source of truth.

Use:

- `MINIME_STORE_BACKEND=postgres` for sessions and jobs
- `MINIME_ASSET_BACKEND=s3` for uploaded photos and generated assets

Local disk can still exist inside the container as scratch space for the Python and ffmpeg pipeline, but durable state should live outside the task.

## Operational Notes

- Bake Python, `uv`, `ffmpeg`, and any pipeline dependencies into the image used by the ECS task.
- Keep worker execution enabled in the same service unless and until the job runner is split further.
- If `tongue-api` also dispatches script runs, avoid circular call paths. In the internal worker shape, `mini-mi-api` should typically execute generation locally inside its own task image.
