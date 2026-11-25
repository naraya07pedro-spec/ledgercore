# HTTP API

Authenticated requests send `Authorization: Bearer <workspace-token>`. Tokens identify a workspace and an operator or viewer role. UUIDs are lowercase canonical strings. The API accepts one account line per journal, between 2 and 100 lines. Specify signed minor units as JSON strings. Positive values are credits and negative values are debits.

| Method and path | Role | Behavior |
| --- | --- | --- |
| `GET /v1/accounts` | Viewer or operator | Up to 200 live accounts. Use `?through=1` for journal-prefix balances. |
| `GET /v1/journals` | Viewer or operator | Up to 25 newest journals with entries. Optional `q` searches memo or ID, `currency` filters, and `before` is an exclusive sequence cursor. |
| `POST /v1/journals` | Operator | Post a balanced single-currency journal. |
| `POST /v1/journals/{id}/reversal` | Operator | Post exact negated lines for the original. Empty body. |
| `POST /v1/reconciliations` | Operator | Compare up to 1,000 statement rows against a published sequence range of at most 1,000 values. |
| `GET /v1/reconciliations` | Viewer or operator | Return 10 most recent persisted reports. |
| `GET /v1/operations` | Viewer or operator | Return caller role and process-wide uptime, request count, and connection pool occupancy. |
| `GET /healthz`, `GET /readyz`, `GET /metrics` | Unauthenticated | Local probes and aggregate Prometheus text exposition. |

## Post and replay

```bash
source scripts/host-env.sh
key=$(openssl rand -hex 16)
curl -fsS -H "Authorization: Bearer $LEDGER_OPERATOR_TOKEN" \
  -H "Idempotency-Key: $key" -H 'Content-Type: application/json' \
  -d '{"currency":"USD","memo":"Reviewer walkthrough","entries":[
    {"account":"10000000-0000-4000-8000-000000000003","amount":"-275"},
    {"account":"10000000-0000-4000-8000-000000000004","amount":"275"}]}' \
  "$LEDGER_API_TEST_URL/v1/journals"
```

The first successful post or reversal returns `201`. Replay the same canonical payload with the same key to retrieve the original journal with `200` and `Idempotency-Replayed: true`. Entry ordering does not affect the payload digest. The reversal target and operation identity are bound to the key. Generate a fresh key for each new intent. Keys are 1–128 printable ASCII characters without spaces. Reconciliation accepts an optional `Idempotency-Key` with the same replay statuses. The key binds the sequence bounds and ordered statement rows. Unkeyed reconciliation requests retain the original behavior of creating a new report.

A statement uses `{"from":"2","through":"2","lines":[{"journal":"<journal-uuid>","currency":"USD","credits":"275"}]}`. Use a real sequence and UUID from the posted transaction. An empty `lines` array finds all missing records in the window.

Errors return `{"error":"message"}`. Typical statuses are `400` for invalid input, `401` for missing or invalid authentication, `403` for insufficient role, `404` for workspace-scoped lookup misses, `409` for insufficient funds or a conflict, and `503` for database or capacity failures. Read `X-Trace-ID` to correlate a request with server logs. After a transport error, timeout, or 503, retry financial writes with exactly the same key and payload.
