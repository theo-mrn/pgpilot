# ADR-003: Use `age` for Backup Encryption

## Status
Accepted

## Date
2026-05-16

## Context
Backups contain sensitive production data. A compromised backup is equivalent
to a compromised database. Encryption must be enabled by default, simple to
operate, and not depend on complex key infrastructure.

## Decision
All backups are encrypted with `age` before being sent to storage.
Encryption is **enabled by default** and cannot be silently disabled.

## Alternatives Considered

| Tool | Reason Rejected |
|---|---|
| **GPG** | Complex key management, legacy UX, easy to misconfigure |
| **OpenSSL** | Low-level, no standard backup encryption workflow |
| **Rely on S3 SSE** | Server-side only, storage provider has access to keys |
| **No encryption** | Unacceptable — backup = full DB exposure |

## Consequences

**Positive**
- Modern, simple CLI: `age -r <pubkey> -o backup.age backup.tar`
- No key servers, no web of trust
- Recipients identified by public key or SSH key
- Small, auditable Go binary
- Keys stay outside the backup storage

**Negative**
- Losing the private key = losing access to all backups
- Key rotation requires re-encrypting existing backups
- Adds a step to the restore process

## Key Management Rules
- Public key stored in `backup.yaml` (safe to commit)
- Private key stored in Vault or K8s Secret (never on disk in plaintext)
- Key rotation procedure must be documented in the runbook
