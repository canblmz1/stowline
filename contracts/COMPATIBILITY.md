# Control-plane contract compatibility

Prefix: `/api/v1`

- Schema version field: `schema_version` integer on every envelope.
- Additive optional JSON fields are compatible.
- Unknown enum values are rejected (fail closed).
- Unknown extra object properties on **requests** are rejected.
- Unknown extra object properties on **responses** may be ignored by older agents.
- Timestamps: UTC RFC3339.
- Snapshot IDs: 64 lowercase hex.
- Request bodies ≤ 256 KiB unless noted.
- `Idempotency-Key` required on admin POST mutations.
- Agents never accept `EXEC`, shell, or arbitrary argv from the server.
