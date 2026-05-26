# ADR-002: YAML as Declarative Source of Truth

## Status
Accepted

## Date
2026-05-16

## Context
The orchestrator needs a way for users to define backup jobs across multiple
environments and database types. The configuration must be readable, versionable,
and environment-agnostic.

## Decision
All backup jobs are defined in a single YAML file (`backup.yaml`).
This file is the single source of truth — no configuration is stored in the
database, dashboard, or environment variables (except secrets).

### Example Structure
```yaml
jobs:
  - name: prod-postgres
    driver: postgres
    tool: wal-g
    environment:
      type: kubernetes
      namespace: production
      pod_selector: app=postgres
    schedule: "0 2 * * *"
    retention: 7d
    encrypt: true
    destination:
      type: s3
      bucket: backups
```

## Alternatives Considered

| Approach | Reason Rejected |
|---|---|
| **CLI flags only** | Not reproducible, hard to version, error-prone |
| **Dashboard-first config** | Adds complexity, chicken-and-egg problem |
| **Environment variables** | Not suitable for multi-job configurations |
| **TOML** | Less familiar in the DevOps/K8s ecosystem |

## Consequences

**Positive**
- GitOps-friendly: config lives in version control
- Reproducible: same file = same behavior across environments
- Human-readable and widely understood
- Easy to validate with JSON Schema

**Negative**
- Secrets must never appear in the YAML (handled by ADR-004)
- Requires a validation layer to catch misconfiguration early
