# ADR-005: Pluggable Multi-Database Architecture

## Status
Accepted

## Date
2026-05-16

## Context
The orchestrator initially targets PostgreSQL but WAL-G supports MySQL/MariaDB,
MongoDB, and Redis. The architecture must accommodate additional database drivers
without requiring core refactoring.

## Decision
Database support is implemented via a driver model. Each driver encapsulates:
- Connection detection logic
- WAL-G configuration generation for that DB engine
- Pre/post backup hooks specific to the engine

### Driver Interface
```
drivers/
├── postgres/
│   ├── detect.sh       # Is this a Postgres instance?
│   ├── configure.sh    # Generate WAL-G config for Postgres
│   └── hooks.sh        # Pre/post backup actions
├── mysql/
│   ├── detect.sh
│   ├── configure.sh
│   └── hooks.sh
└── mongodb/
    ├── detect.sh
    ├── configure.sh
    └── hooks.sh
```

## Scope for Initial Release
Only the **PostgreSQL driver** is fully implemented in Phase 1.
MySQL and MongoDB drivers are stubbed with a "not yet supported" error.

## Alternatives Considered

| Approach | Reason Rejected |
|---|---|
| **Postgres-only forever** | Limits project value, WAL-G already supports more |
| **All DBs at once** | Over-engineering, risk of shipping nothing |
| **Monolithic script** | Impossible to extend cleanly |

## Consequences

**Positive**
- New DB support = new driver folder, no core changes
- Clean separation of concerns
- Postgres driver can be production-ready while others are in progress

**Negative**
- Driver interface must be stable from day one
- Testing matrix grows with each new driver
