# ADR-004: Secrets Management Strategy

## Status
Accepted

## Date
2026-05-16

## Context
The orchestrator handles sensitive credentials: database passwords, S3 access
keys, and encryption private keys. These must never appear in `backup.yaml`,
environment history, or container logs.

## Decision
Secrets are managed differently depending on the detected environment:

| Environment | Secret Backend |
|---|---|
| Kubernetes | K8s Secrets (+ External Secrets Operator if available) |
| Docker / Compose | `.env` file with strict permissions (600), or Docker Secrets |
| Systemd / bare metal | HashiCorp Vault or environment file with strict permissions |

The orchestrator detects the environment and resolves secrets from the
appropriate backend at runtime. Secret references in `backup.yaml` use
a standard syntax:

```yaml
credentials:
  db_password:
    from: vault://secret/prod/postgres#password
  s3_access_key:
    from: k8s-secret://backup-credentials#access_key
  age_private_key:
    from: env://AGE_PRIVATE_KEY
```

## Alternatives Considered

| Approach | Reason Rejected |
|---|---|
| **Hardcoded in YAML** | Critical security violation |
| **Vault only** | Too heavy for Docker/systemd environments |
| **K8s Secrets only** | Not portable outside Kubernetes |
| **Plain env vars** | Leaked in process list, logs, history |

## Consequences

**Positive**
- Secrets never touch disk in plaintext
- Portable across environments
- Auditable via Vault audit log or K8s audit log

**Negative**
- More complex secret resolution logic in the orchestrator
- Vault setup required for systemd environments
- `.env` fallback is less secure — must be documented as dev-only
