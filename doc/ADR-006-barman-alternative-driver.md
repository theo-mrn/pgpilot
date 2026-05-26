# ADR-006: Barman as Alternative Driver for PostgreSQL

## Status
Accepted

## Date
2026-05-16

## Context
WAL-G is the primary backup tool (ADR-001), but Barman remains relevant for
Postgres-only environments that require centralized multi-server management
or where teams already operate a Barman infrastructure.

## Decision
Barman is supported as an optional alternative driver for PostgreSQL.
Users select the tool explicitly in `backup.yaml`:

```yaml
jobs:
  - name: prod-postgres
    driver: postgres
    tool: barman        # or wal-g (default)
```

If `tool` is omitted, WAL-G is used by default.

## When to Use Barman over WAL-G

| Scenario | Recommended Tool |
|---|---|
| Multi-server centralized backup | Barman |
| Cloud-native / K8s environment | WAL-G |
| MySQL / MongoDB also in scope | WAL-G |
| Existing Barman infrastructure | Barman |
| New greenfield Postgres setup | WAL-G |

## Consequences

**Positive**
- Supports teams already using Barman
- Centralized multi-server management available when needed
- WAL-G remains the default — no breaking change

**Negative**
- Barman requires SSH access for restore (additional setup)
- Two tools to maintain and test
- Barman is Postgres-only — cannot be used with other DB drivers
