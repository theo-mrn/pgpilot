# pgpilot

Automated PostgreSQL backups for Kubernetes. No infrastructure changes required.

pgpilot deploys a CronJob per database that runs `pg_dump` and uploads the result to S3-compatible storage. It connects to Postgres over the network — no sidecar, no pod modification, no GitOps conflicts.

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

### 1. Detect your databases

```bash
dbpilot detect
```

Scans your cluster for running Postgres instances and generates `~/.config/dbpilot/backup.yaml`.

### 2. Edit the config

```yaml
jobs:
  - name: myapp-postgres
    db_version: "16"
    environment:
      type: kubernetes
      namespace: myapp
    schedule: "0 2 * * *"   # daily at 02:00
    retention: 7d
    destination:
      type: s3
      bucket: my-backups
      endpoint: https://minio.example.com   # omit for AWS S3
      prefix: myapp/postgres
    credentials:
      db_host: postgres          # Kubernetes service name
      db_password:
        from: k8s-secret://myapp/myapp-env#DB_PASSWORD
      db_user:
        from: k8s-secret://myapp/myapp-env#DB_USER
      db_name:
        from: k8s-secret://myapp/myapp-env#DB_NAME
      s3_access_key:
        from: k8s-secret://myapp/s3-credentials#access_key
      s3_secret_key:
        from: k8s-secret://myapp/s3-credentials#secret_key
```

### 3. Deploy

```bash
dbpilot deploy
```

Creates a CronJob in each namespace. That's it.

### 4. Run a manual backup

```bash
dbpilot backup --wait
```

---

## How it works

Each CronJob runs a pod with `postgres:<version>-alpine` + `aws-cli`:

```
pg_dump -Fc | aws s3 cp - s3://bucket/prefix/20240115T020000Z.dump.gz
```

Backups are named by timestamp and stored in custom format (`.dump.gz`), restorable with `pg_restore`.

---

## Commands

| Command | Description |
|---|---|
| `dbpilot detect` | Scan the cluster and generate `backup.yaml` |
| `dbpilot deploy` | Deploy CronJobs to Kubernetes |
| `dbpilot backup` | Trigger a manual backup |
| `dbpilot backup --wait` | Trigger and wait for result |
| `dbpilot validate` | Validate `backup.yaml` |
| `dbpilot status` | Show status of deployed jobs |
| `dbpilot version` | Print version |

---

## Secret references

Credentials are never stored in plain text. They reference existing Kubernetes Secrets:

```
k8s-secret://<namespace>/<secret-name>#<key>
```

Example: `k8s-secret://myapp/myapp-env#DB_PASSWORD`

---

## Supported Postgres versions

| Version | Docker image |
|---|---|
| 14 | `maxwellfaraday/dbpilot-backup:pg14` |
| 15 | `maxwellfaraday/dbpilot-backup:pg15` |
| 16 | `maxwellfaraday/dbpilot-backup:pg16` |
| 17 | `maxwellfaraday/dbpilot-backup:pg17` |

---

## Restore

```bash
# Download the backup
aws s3 cp s3://my-backups/myapp/postgres/20240115T020000Z.dump.gz ./backup.dump.gz

# Restore
pg_restore -h <host> -U <user> -d <database> ./backup.dump.gz
```
