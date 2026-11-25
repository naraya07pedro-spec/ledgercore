# Threat model

The protected assets are workspace records, account balances, tokens, and posting permissions. The trust boundaries are the browser, dashboard, Go API, restricted database role, database owner and observability components.

| Threat | Control |
| --- | --- |
| Forged or stolen workspace credentials | Random 256-bit bearer tokens, digest lookup, in-memory browser storage, loopback Compose bindings. Rotate/revoke tokens explicitly. |
| Cross-workspace resource access | Scope comes from the credential. SQL binds workspace independently of supplied resource IDs. Transaction-local scope and row policies provide a second check. |
| Unauthorized mutations | Operator role required. Runtime SQL has a narrow posting function and no direct journal mutation grants. |
| Duplicate effects or overdrafts | Payload-bound receipt, advisory locks, ordered account locks and database validation in one transaction. |
| SQL injection and content injection | Bind positional SQL parameters. Render user content through React text escaping. Dashboard forwards recognized route shapes and rejects cross-origin browser requests. The browser keeps its retry receipt in memory while the page remains open. |
| Credential leakage | Request logs use route templates, status and trace ID. Startup redacts database connection errors. The local env generator uses restrictive permissions. |
| Resource exhaustion | Bound request bodies, line counts, response pages, database pool, transaction and HTTP deadlines. The dashboard applies its deadline while reading the request and calling the API. Keep health probes outside authenticated concurrency limits. |

The database login belongs to the trusted application. It can set workspace context and call the posting function, so a compromised API or stolen runtime DB credential can bypass application authorization. A privileged database owner can bypass schema protections. Treat backups as sensitive, restrict owner access, and supply a genuinely isolated database. This demonstration uses bearer tokens scoped to one workspace and roles, with no identity provider, per-user audit actor, browser session persistence, public TLS termination, perimeter rate limiting, or external write-ahead audit store.
