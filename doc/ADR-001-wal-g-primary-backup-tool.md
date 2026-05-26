# ADR-001: Use WAL-G as Primary Backup Tool

## Status
Accepted

## Date
2026-05-16

## Context
The project needs a backup tool that can handle multiple database engines,
integrate with S3-compatible object storage, and work across heterogeneous
environments (Docker, Kubernetes, systemd). The tool must be actively maintained
and production-grade.

## Decision
WAL-G is used as the primary backup engine underneath the orchestrator.

## Alternatives Considered

| Tool | Reason Rejected |
|---|---|
| **pg_dump** | Logical only, no PITR, slow at scale, Postgres-only |
| **Barman** | Postgres-only, requires SSH for restore, no multi-DB support |
| **pgBackRest** | Archived and unmaintained as of April 2026 |
| **pgmoneta** | Smaller community, Postgres-only, less mature |

## Consequences

**Positive**
- Supports PostgreSQL, MySQL/MariaDB, MS SQL Server, MongoDB (beta), Redis (beta)
- Cloud-native: S3, GCS, Azure Blob, MinIO out of the box
- Lightweight Go binary, no heavy dependencies
- Supports PITR, incremental backups, parallel compression
- Actively maintained

**Negative**
- Configuration syntax differs per database engine
- MongoDB and Redis support still in beta
- Requires WAL archiving setup on Postgres (more complex than pg_dump)

## Notes
Barman remains available as an alternative driver for Postgres-only environments
where centralized multi-server management is needed (see ADR-006).
