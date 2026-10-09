# Deploying JAWIR Sentinel backend to Cloud Run

Two Cloud Run resources share one container image; only the container
command differs:

| Source | Binary | Cloud Run resource | Manifest |
| --- | --- | --- | --- |
| `cmd/api` | `sentinel-api` | `sentinel-api` Service | `cloudrun-api.yaml` |
| `cmd/worker` | `sentinel-worker` | `sentinel-worker` Worker Pool | `cloudrun-worker.yaml` |

The API request path never runs process-local durable AI goroutines; the
worker pool runs the outbox dispatcher + RabbitMQ consumer continuously.

> Actual deployment requires a real GCP project, IAM permissions, and
> reachable Cloud SQL / RabbitMQ / GCS / Vertex AI. Nothing here
> auto-deploys from CI without those credentials.

## 1. Build and push the image

```bash
PROJECT=my-gcp-project
REGION=asia-southeast2
TAG=$(git rev-parse --short HEAD)

gcloud builds submit --region "$REGION" \
  --tag "$REGION-docker.pkg.dev/$PROJECT/sentinel/sentinel-api:$TAG" .
```

The same image serves both resources; retag it for the worker or reference
the identical digest in `cloudrun-worker.yaml`.

## 2. Create secrets

```bash
gcloud secrets create sentinel-database-url --data-file=<(printf '%s' "$DATABASE_URL")
gcloud secrets create sentinel-rabbitmq-url --data-file=<(printf '%s' "$RABBITMQ_URL")
# repeat for any other *_SECRET values referenced via valueFrom in the manifests
```

Grant the runtime service account `roles/secretmanager.secretAccessor` on
each secret.

## 3. Deploy the API service

Edit `cloudrun-api.yaml`: replace `PROJECT`, `REGION`, and `TAG`
placeholders, set env values, and point `valueFrom.secretKeyRef` entries at
the secrets created above. Then:

```bash
gcloud run services replace deploy/cloudrun-api.yaml --region "$REGION"
```

Or the imperative equivalent:

```bash
gcloud run deploy sentinel-api \
  --image "$REGION-docker.pkg.dev/$PROJECT/sentinel/sentinel-api:$TAG" \
  --region "$REGION" --port 8080 \
  --set-env-vars APP_ENV=production,APP_PORT=8080 \
  --set-secrets DATABASE_URL=sentinel-database-url:latest \
  --service-account "sentinel-runtime@$PROJECT.iam.gserviceaccount.com"
```

Verify:

```bash
curl -s "https://sentinel-api-xxx.$REGION.run.app/healthz"
```

## 4. Deploy the worker pool

Edit `cloudrun-worker.yaml` the same way (image digest identical to the API,
command override `["/app/sentinel-worker"]`, min instances ≥ 1 so the
dispatcher/consumer loop stays continuous, no public ingress), then:

```bash
gcloud run worker-pools replace deploy/cloudrun-worker.yaml --region "$REGION"
```

If worker pools are unavailable in your region, deploy as a service with
`--no-cpu-throttling --min-instances=1 --no-allow-unauthenticated` and the
worker command override instead.

Verify the worker is consuming:

```bash
gcloud run worker-pools logs read sentinel-worker --region "$REGION" --limit 50
```

## 5. Outbox restart-survival check

1. Submit a case so an `AI_ANALYSIS_REQUESTED` outbox row goes `PENDING`.
2. Stop the broker or delete the worker revision mid-dispatch.
3. Bring the worker back; the dispatcher must redeliver the event
   (attempt count increments, no duplicate finalization — see BE-050
   integration tests).
4. Repeat while restarting the API service: published state must be
   unaffected because publishing only happens in the worker.

## 6. Rollback

```bash
gcloud run services update-traffic sentinel-api --to-revisions PREV_REVISION=100 --region "$REGION"
```

## Local parity

`docker compose up --build` runs postgres+pgvector, RabbitMQ, the API, and
the worker from the same Dockerfile. Copy `.env.example` to `.env` and adjust
local-only values; never commit real credentials.
