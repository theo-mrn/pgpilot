# ADR-008: Security Trust Model

## Status
Accepted

## Date
2026-05-16

## Context
dbpilot requires access to sensitive infrastructure: Docker socket, Kubernetes
API, and database credentials. Users — especially security-conscious developers
and ops teams — will not adopt a tool they cannot audit or trust. The security
model must be explicit, minimal, and verifiable.

## Decision
dbpilot follows a **zero-trust, least-privilege, local-only** model.

### Core Principles

**1. No outbound network traffic**
dbpilot never contacts external servers. It communicates only with:
- Local Docker socket (`/var/run/docker.sock`)
- Local or configured Kubernetes API
- User-defined S3/MinIO endpoint

No telemetry. No analytics. No phone home. Ever.

**2. dbpilot never touches DB credentials directly**
dbpilot generates configuration files and deploys schedulers.
WAL-G or Barman run **inside** the container or pod — they access the database
directly. Credentials never transit through dbpilot at runtime.

```
User → dbpilot (configure + deploy) → WAL-G (runs inside pod) → Database
                                         ↑
                              credentials stay here, never in dbpilot
```

**3. Minimal permissions, explicitly documented**

| Component | Permission Required | Why |
|---|---|---|
| Docker | Read socket, exec into container | Run WAL-G inside container |
| Kubernetes | get/list pods, create/delete CronJob, exec | Deploy scheduler, run backup |
| Database | `pg_read_all_data` role (Postgres) | WAL-G reads data, no writes needed |
| S3/MinIO | PutObject, GetObject, ListBucket | Store and retrieve backups |

No superuser. No cluster-admin. No privileged containers.

**4. Fully open source**
Every line of code is public. No proprietary backend, no SaaS component,
no closed binary. Users can audit, fork, and self-host entirely.

**5. Dry-run mode**
Every destructive or mutating operation supports `--dry-run`:
```bash
dbpilot backup --dry-run    # shows what would run, touches nothing
dbpilot deploy --dry-run    # shows manifests, applies nothing
dbpilot restore --dry-run   # shows restore plan, restores nothing
```

**6. Local audit log**
Every action dbpilot takes is written to a local JSON audit log.
Nothing is hidden. Users can inspect exactly what happened and when.

```json
{
  "timestamp": "2026-05-16T02:00:01Z",
  "action": "backup",
  "environment": "kubernetes",
  "database": "prod-postgres",
  "tool": "wal-g",
  "status": "success",
  "duration_ms": 4200,
  "backup_size_bytes": 104857600
}
```

## What dbpilot will NEVER do
- Send credentials, backup data, or metadata to any external service
- Require superuser or cluster-admin permissions
- Run as root inside containers
- Store secrets in plaintext on disk
- Silently fall back to insecure defaults

## Consequences

**Positive**
- Auditable by any security team
- Adoptable in regulated environments
- Builds trust through transparency
- Reduces blast radius if dbpilot itself is compromised

**Negative**
- Slightly more complex setup (RBAC, least-privilege roles to configure)
- No "just works" magic — users must explicitly grant permissions
- Dry-run mode must be maintained as the codebase evolves
