# Vercel Deployment

## Short Version

`mini-mi-api` is not a clean direct Vercel deploy today.

The current backend assumes:

- a long-running Go process
- optional long-running worker processes
- shared local disk for `store.json` and generated assets

Vercel is good at request/response functions. It is not a good fit for this exact local-disk job-runner shape.

## What Vercel Can Still Be Used For

Vercel can still be part of the final setup if we change the backend shape:

- Vercel hosts the public API routes
- Postgres stores sessions and jobs
- object storage stores uploaded photos and generated outputs
- a separate worker runtime processes queued jobs

That would let the shipped Mac app hit a stable Vercel URL while the actual job execution runs elsewhere.

## Fastest Path To A Working Hosted Backend

If the priority is shipping fast, deploy this repo first on a single host that supports:

- one long-running Go service
- optional worker processes
- persistent local disk

That gets you:

- a real public backend URL
- provider keys on the server instead of the Mac app
- minimal code churn from the current scaffold

## Vercel-Compatible Rewrite Scope

If you want to force Vercel to be the actual hosted API for this backend, the required changes are:

1. Replace `store.json` with Postgres (`MINIME_STORE_BACKEND=postgres`).
2. Replace local asset files with object storage.
3. Replace local job polling with queue-backed job execution.
4. Remove any assumption that the API process owns the worker lifecycle.
5. Convert the HTTP surface into Vercel function entrypoints.

## Recommendation

Ship the first hosted version on a runtime that matches the current architecture.

Then, if Vercel is still desirable for the public API layer, migrate once the storage and job model are externalized.
