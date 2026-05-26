# ADR-011: Database Detection Strategy

## Status
Accepted

## Date
2026-05-26

## Context
dbpilot needs to automatically discover database instances across heterogeneous
environments (Kubernetes, Docker, systemd) without requiring users to manually
specify pod names, container names, or service addresses.

In real-world deployments, database instances rarely follow a predictable naming
convention. A pod running PostgreSQL might be named `monapp-db-0`,
`backend-prod-postgres-primary-0`, or `release-name-postgresql-0` (Helm).
Detection cannot rely on naming alone.

## Decision
dbpilot uses a **multi-signal confidence scoring system** to detect database
instances. No single signal is sufficient — the combination determines whether
an instance is confirmed, flagged for review, or excluded.

---

## Confidence Scoring

Each signal contributes a score. The total determines the confidence level.

| Signal | Score | Rationale |
|---|---|---|
| Image name contains known DB keyword (`postgres`, `postgresql`, `bitnami/postgresql`, `postgis`) | +3 | Image is the strongest non-intrusive signal |
| Environment variable `POSTGRES_DB`, `POSTGRES_USER`, or `PGDATA` present | +3 | Explicit DB configuration |
| Protocol handshake successful on port 5432 | +3 | Definitive confirmation |
| Port 5432 exposed | +2 | Strong indicator, not conclusive alone |
| K8s label matches known patterns (`app=postgres`, `app.kubernetes.io/name=postgresql`) | +1 | Unreliable, chart-dependent |
| Pod/container name contains known keyword (`db`, `postgres`, `pgsql`, `database`) | +1 | Weakest signal, last resort |

### Confidence Levels

| Total Score | Confidence | Behavior |
|---|---|---|
| ≥ 6 | **High** | Auto-included, shown with ✓ |
| 3 – 5 | **Medium** | Shown with ⚠, user must confirm |
| 1 – 2 | **Low** | Excluded by default, shown with --verbose |
| 0 | **None** | Ignored |

---

## Detection Per Environment

### Kubernetes
```
1. List all namespaces via K8s API
2. For each namespace, list all pods
3. For each pod, inspect:
   - spec.containers[].image
   - spec.containers[].env[]
   - spec.containers[].ports[]
4. Attempt protocol handshake via kubectl port-forward (optional, --deep-scan)
5. Score and classify each pod
```

### Docker / Docker Compose
```
1. List all running containers via Docker API
2. For each container, inspect:
   - Config.Image
   - Config.Env[]
   - NetworkSettings.Ports
3. Attempt protocol handshake on exposed port (optional, --deep-scan)
4. Score and classify each container
```

### Systemd
```
1. List all active services via systemctl
2. For matching services, inspect:
   - ExecStart path (contains postgres binary?)
   - Environment files
3. Inspect running processes via /proc
4. Attempt protocol handshake on localhost:5432 (optional, --deep-scan)
5. Score and classify each service
```

---

## Multi-Database Disambiguation

When multiple DB engines are possible, scoring is run per driver:

```
Container: monapp-db-0
  → Postgres score: 7 (high)   image: bitnami/postgresql:16
  → MySQL score:    0 (none)
  → MongoDB score:  0 (none)
  → Result: PostgreSQL confirmed
```

```
Container: monapp-db-0
  → Postgres score: 2 (low)    port 5432 exposed
  → MySQL score:    6 (high)   image: mysql:8, MYSQL_DATABASE present
  → Result: MySQL confirmed, Postgres excluded
```

---

## Protocol Handshake (--deep-scan)

By default, dbpilot does **not** attempt a network connection to detected
instances — passive inspection only, no credentials needed.

With `--deep-scan`, dbpilot attempts a Postgres protocol handshake
(without authentication) to definitively confirm the engine and version:

```bash
dbpilot init --deep-scan
```

This adds +3 to the score when successful and retrieves the exact
PostgreSQL version, which is required for WAL-G configuration.

Deep scan is opt-in because:
- It generates connection attempts visible in DB logs
- Some security policies flag unexpected connection probes
- It requires network access from where dbpilot runs

---

## User-Facing Output

```bash
$ dbpilot init

🔍 Scanning all namespaces...

Found database instances:

  namespace: production
  ✓ monapp-db-0          PostgreSQL 16.2  ~45 GB   [confidence: high]
  ✓ analytics-db-0       PostgreSQL 15.4  ~120 GB  [confidence: high]

  namespace: staging
  ✓ monapp-stag-0        PostgreSQL 15.4  ~2 GB    [confidence: high]

  namespace: monitoring
  ⚠ grafana-0            PostgreSQL 14.1  ~500 MB  [confidence: medium]
    → embedded DB detected inside grafana/grafana image

? Which instances to backup?
  ✓ production/monapp-db-0
  ✓ production/analytics-db-0
  ✓ staging/monapp-stag-0
  ✗ monitoring/grafana-0
```

---

## Explicit Override

Users can always bypass detection entirely and specify instances manually
in `backup.yaml`:

```yaml
jobs:
  - name: my-custom-db
    driver: postgres
    environment:
      type: kubernetes
      namespace: production
      pod: monapp-db-0        # explicit, no detection needed
      container: postgres     # if multi-container pod
```

This is the recommended approach for production environments where
auto-detection has already been validated once via `dbpilot init`.

---

## What dbpilot Will Never Do During Detection

- Authenticate to any database
- Read any data from any database
- Modify any resource in any environment
- Generate noise in application logs (unless --deep-scan is explicitly used)

---

## Consequences

**Positive**
- Works regardless of pod/container naming conventions
- Handles Helm-generated names, custom names, legacy names
- Confidence levels prevent silent false positives
- Deep scan opt-in respects security policies
- Explicit override covers edge cases the scorer misses

**Negative**
- Scoring thresholds may need tuning as new base images emerge
- Medium confidence instances require human validation
- Deep scan adds latency to `dbpilot init` when enabled
- New DB engines require a new scorer to be implemented (see ADR-005)
