# ADR-010: Restore Strategy

## Status
Accepted

## Date
2026-05-16

## Context
A backup system is only as good as its restore capability. Restoring a database
is a high-risk, potentially irreversible operation. The restore strategy must
be safe, auditable, flexible, and protect users from accidental data loss while
remaining practical in an emergency.

## Decision
dbpilot provides three restore modes with mandatory safety gates for
production targets.

---

## Restore Modes

### Mode 1 — Latest
Restores the most recent successful base backup.
Fast, simple, no configuration needed.

```bash
dbpilot restore --instance production/postgres-0 --mode latest
```

### Mode 2 — PITR (Point In Time Recovery)
Restores to an exact date and time by replaying WAL archives.
Requires WAL archiving to be enabled (wal_archive: true in backup.yaml).

```bash
dbpilot restore --instance production/postgres-0 --time "2026-05-25 14:32:00"
```

**WAL-G restore process:**
1. Find the most recent base backup before the target timestamp
2. Download and decrypt the base backup (age key)
3. Restore data files to the pod/container
4. Write `recovery_target_time` to postgresql.conf (Postgres 12+)
5. Start Postgres in recovery mode
6. Postgres replays WAL automatically up to the target timestamp
7. dbpilot verifies DB is reachable and coherent

### Mode 3 — Sandbox
Restores into a new ephemeral instance without touching the source.
Safe for verification, debugging, and restore testing.

```bash
dbpilot restore --instance production/postgres-0 --mode sandbox
```

- Creates a temporary pod in a dedicated `dbpilot-sandbox` namespace
- Auto-destroys after a configurable TTL (default: 2h)
- Never touches the production instance

---

## Safety Gates

Every restore to an existing instance (modes 1 and 2) requires:

**1. Pre-restore snapshot**
dbpilot automatically takes a snapshot of the current state before
any restore operation. This is the safety net if the restore goes wrong.

**2. Explicit confirmation**
User must type the full instance name to confirm:
```
⚠ This will OVERWRITE production/postgres-0.
  Type the instance name to confirm: production/postgres-0
```

**3. Audit log entry**
Every restore is logged with:
- Who triggered it (system user)
- Target instance
- Target timestamp
- Mode used
- Pre-restore snapshot reference
- Outcome (success/failure)

**4. Restore is always decrypted locally**
Decryption happens in memory during restore — decrypted data never
touches disk outside the target database volume.

---

## Restore is a First-Class Feature

A backup system that has never been tested for restore is not a backup system.
dbpilot treats restore as equal in importance to backup.

### Automated Restore Testing (Phase 2)
After each scheduled backup, dbpilot can optionally trigger an automatic
sandbox restore to verify the backup is valid:

```yaml
global:
  verify_restore: true    # auto-test every backup in sandbox
  verify_ttl: 30m         # destroy sandbox after 30 minutes
```

This is what professional backup systems do and most teams skip.

---

## Restore Availability by Environment

| Environment | Latest | PITR | Sandbox |
|---|---|---|---|
| Kubernetes | ✅ | ✅ | ✅ |
| Docker | ✅ | ✅ | ✅ (new container) |
| Systemd | ✅ | ✅ | ⚠ (new pg_ctl instance) |
| Remote (managed DB) | ✅ | ❌ | ✅ |

---

## Restore is NOT available without encryption key
If the age private key is lost, backups cannot be decrypted.
dbpilot warns the user at deploy time and recommends storing the key
in at least two separate secure locations.

## Consequences

**Positive**
- PITR covers virtually any data loss scenario within the retention window
- Sandbox mode enables safe verification without risk
- Pre-restore snapshot eliminates the "restore made things worse" scenario
- Automated restore testing catches silent backup corruption early

**Negative**
- PITR requires WAL archiving — adds complexity to initial setup
- Pre-restore snapshot doubles storage usage temporarily
- Sandbox requires available cluster resources (a new pod)
- Key loss = unrecoverable backups — must be communicated clearly
