ALTER TABLE public.reconciliations
    ADD COLUMN idempotency_key text,
    ADD COLUMN payload_hash bytea,
    ADD CONSTRAINT reconciliation_payload_pair CHECK (
        (idempotency_key IS NULL AND payload_hash IS NULL)
        OR (idempotency_key IS NOT NULL AND payload_hash IS NOT NULL
            AND length(idempotency_key) BETWEEN 1 AND 128 AND length(payload_hash) = 32)
    ),
    ADD CONSTRAINT reconciliation_request_key UNIQUE (workspace_id, idempotency_key);
GRANT INSERT (idempotency_key, payload_hash) ON public.reconciliations TO ledger_app;
