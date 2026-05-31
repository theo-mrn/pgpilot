# pgpilot

Automated PostgreSQL backups for Kubernetes. No infrastructure changes required.

pgpilot deploys a CronJob per database that runs `pg_dump` and uploads the result to S3-compatible storage. It connects to Postgres over the network — no sidecar, no pod modification, no GitOps conflicts.

Point-in-time recovery (PITR) via continuous WAL streaming with WAL-G is also supported.

---

## Install

Download the binary for your platform from the [latest release](https://github.com/theo-mrn/pgpilot/releases/latest):

```bash
# macOS (Apple Silicon)
curl -L https://github.com/theo-mrn/pgpilot/releases/latest/download/dbpilot-darwin-arm64 -o /usr/local/bin/dbpilot
chmod +x /usr/local/bin/dbpilot

# macOS (Intel)
curl -L https://github.com/theo-mrn/pgpilot/releases/latest/download/dbpilot-darwin-amd64 -o /usr/local/bin/dbpilot
chmod +x /usr/local/bin/dbpilot

# Linux (amd64)
curl -L https://github.com/theo-mrn/pgpilot/releases/latest/download/dbpilot-linux-amd64 -o /usr/local/bin/dbpilot
chmod +x /usr/local/bin/dbpilot
```

---

## Prerequisites

- A Kubernetes cluster with `kubectl` configured
- A Postgres instance reachable via a Kubernetes Service
- A Postgres user with `SELECT` and `CONNECT` privileges
- An S3-compatible bucket (AWS S3, MinIO, etc.)

---

## Quickstart

### 1. Create a config

```bash
dbpilot config create myapp
```

This opens an interactive setup to configure your database and S3 destination. The config is saved to `~/.config/dbpilot/myapp.yaml`.

### 2. Validate the config

```bash
dbpilot validate myapp
```

### 3. Deploy

```bash
dbpilot deploy myapp
```

Creates a CronJob in each namespace. That's it.

### 4. Run a manual backup

```bash
dbpilot backup myapp run
```

Triggers a backup and streams the pod logs until completion.

### 5. Restore

```bash
dbpilot restore myapp run
```

Lists available backups in S3 and lets you pick one to restore interactively.

---

## Commands

### Backup

| Command | Description |
|---|---|
| `dbpilot backup <name> run [job-name]` | Trigger a backup now and wait for result |
| `dbpilot backup <name> run --no-wait` | Trigger and return immediately |
| `dbpilot backup <name> run --dry-run` | Show what would be triggered |
| `dbpilot backup <name> list [job-name]` | List available backups in S3 |

### Restore

| Command | Description |
|---|---|
| `dbpilot restore <name> run [job-name]` | Restore from a snapshot backup (interactive) |
| `dbpilot restore <name> pitr [job-name] --target-time <RFC3339>` | Restore to a point in time via WAL replay |

### PITR (Point-in-Time Recovery)

| Command | Description |
|---|---|
| `dbpilot pitr enable <name> [job-name]` | Enable continuous WAL streaming |
| `dbpilot pitr disable <name>` | Disable WAL streaming and remove agents |
| `dbpilot pitr basebackup <name> [job-name]` | Take a WAL-G base backup |
| `dbpilot pitr status <name>` | Show WAL agent status |

### Config

| Command | Description |
|---|---|
| `dbpilot config create <name>` | Create a new backup config interactively |
| `dbpilot config <name> list` | List jobs in a config |
| `dbpilot config <name> edit` | Open config in `$EDITOR` |
| `dbpilot config <name> delete` | Delete a config |
| `dbpilot config <name> storage` | Reconfigure S3 storage |

### Other

| Command | Description |
|---|---|
| `dbpilot deploy <name>` | Deploy CronJobs to Kubernetes |
| `dbpilot validate <name>` | Validate a backup configuration |
| `dbpilot status [name]` | Show status of deployed CronJobs |
| `dbpilot version` | Print version |

---

## Config format

Configs live in `~/.config/dbpilot/<name>.yaml`:

```yaml
jobs:
  - name: myapp-postgres
    db_version: "16"
    environment:
      type: kubernetes
      namespace: myapp
    schedule: "0 2 * * *"   # daily at 02:00
    retention: 7d
    destinations:
      - type: s3
        bucket: my-backups
        endpoint: https://minio.example.com   # omit for AWS S3
        prefix: myapp/postgres
        s3_access_key:
          from: k8s-secret://myapp/s3-credentials#access_key
        s3_secret_key:
          from: k8s-secret://myapp/s3-credentials#secret_key
    credentials:
      db_host: postgres
      db_password:
        from: k8s-secret://myapp/myapp-env#DB_PASSWORD
      db_user:
        from: k8s-secret://myapp/myapp-env#DB_USER
      db_name:
        from: k8s-secret://myapp/myapp-env#DB_NAME
```

---

## Secret references

Credentials are never stored in plain text. They reference existing Kubernetes Secrets:

```
k8s-secret://<namespace>/<secret-name>#<key>
```

---

## How it works

Each CronJob runs a pod with `postgres:<version>-alpine` + `aws-cli`:

```
pg_dump -Fc | aws s3 cp - s3://bucket/prefix/20240115T020000Z.dump.gz
```

Backups are named by timestamp and stored in custom format (`.dump.gz`), restorable with `pg_restore`.

For PITR, a WAL streaming agent (WAL-G) is deployed as a Deployment alongside the database pod and continuously ships WAL segments to S3.
