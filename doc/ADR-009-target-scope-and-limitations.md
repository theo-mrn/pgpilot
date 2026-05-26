# ADR-009: Target Scope and Limitations

## Status
Accepted

## Date
2026-05-16

## Context
dbpilot is an orchestrator for database backups across heterogeneous environments.
It is important to define explicitly who this tool is for, what it handles well,
and where it reaches its limits — both for user trust and to avoid over-engineering
the wrong features.

## Decision
dbpilot targets **small to medium teams** running databases in heterogeneous
environments without a dedicated DBA team.

### Primary Target

| Dimension | Target |
|---|---|
| **Team size** | 1–50 engineers |
| **DB size** | Up to ~500 GB |
| **Environments** | Mixed (Docker, K8s, systemd) |
| **DBA expertise** | Low to medium |
| **Infrastructure** | Self-hosted VPS, private cloud, or small K8s cluster |

This covers the vast majority of real-world projects: startups, scale-ups,
internal tools, side projects, and small SaaS platforms.

### DB Size Behavior

| Size | Behavior |
|---|---|
| **< 10 GB** | Fast backup and restore, no concerns |
| **10 GB – 500 GB** | Sweet spot — WAL-G parallel compression shines |
| **500 GB – 2 TB** | Works, but backup duration and bandwidth must be planned |
| **> 2 TB** | Functional but not recommended — see limitations below |

### What dbpilot Does NOT Cover

| Scenario | Why Out of Scope | Better Alternative |
|---|---|---|
| Multi-TB production DB | Requires dedicated DBA tooling and SLAs | Cloud-managed DB (RDS, CloudSQL, Azure DB) |
| RPO < 1 minute | Backup is not replication | Synchronous replication + streaming |
| > 10,000 TPS write workload | Backup impact on performance requires expert tuning | Dedicated DBA + specialized tooling |
| Compliance-heavy regulated environments (PCI-DSS, HIPAA) | Requires certified tooling and audit trails beyond dbpilot's scope | Vendor-certified backup solutions |
| Multi-region active-active setups | Outside the scope of a single backup orchestrator | Cloud-native DR solutions |

### Honest Positioning
dbpilot is not trying to replace enterprise backup solutions.
It fills the gap for teams that:
- Don't have a DBA
- Run Postgres (and eventually MySQL/MongoDB) across mixed environments
- Want backup that "just works" without weeks of configuration
- Need something auditable and secure without a dedicated security team

## Consequences

**Positive**
- Clear scope prevents feature creep
- Users self-select appropriately — no false promises
- Simpler codebase — no need to optimize for 10 TB edge cases
- Honest README builds trust

**Negative**
- Excludes large enterprise customers by design
- Some users may hit limits and need to migrate to heavier tooling
- Must be clearly communicated to avoid misuse in production at scale

## Note on Growth
If a user's DB grows beyond the recommended range, dbpilot should warn them:

```
⚠ Backup size exceeded 500 GB threshold.
  Consider reviewing your backup strategy for databases of this size.
  See: https://github.com/you/dbpilot/docs/large-db.md
```
