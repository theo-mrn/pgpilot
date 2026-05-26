# ADR-007: No Dashboard in Initial Scope

## Status
Accepted

## Date
2026-05-16

## Context
A web dashboard was considered for visualizing backup status, triggering manual
backups, and managing restore operations. Given the security implications and
the risk of scope creep, a decision was needed on whether to include it.

## Decision
No dashboard in the initial release. The orchestrator is CLI + YAML only.
A dashboard is deferred to a future phase once the core is stable and
battle-tested.

## Security Rationale
A web dashboard exposing backup operations represents a significant attack surface:
- A compromised dashboard = access to all backup data
- Restore via UI = potential data exfiltration vector
- Every exposed port is a potential entry point

These risks are acceptable only once the core security model (ADR-003, ADR-004)
is proven solid.

## Scope for Future Dashboard (Phase 4)
When implemented, the dashboard will:
- Require authentication (OIDC/SSO, no local auth)
- Be HTTPS-only, not exposed to public internet by default
- Have read-only mode by default (restore requires explicit opt-in)
- Log every action to the audit trail
- Be deployable via Docker Compose or K8s manifest

## Consequences

**Positive**
- Full focus on core backup/restore reliability in Phase 1-3
- Smaller attack surface during development
- Forces a clean CLI/API separation that benefits the future dashboard

**Negative**
- Less visual appeal for portfolio during initial phases
- Manual inspection of backup status requires CLI or log reading
