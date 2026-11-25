CREATE TABLE public.workspaces (
    id uuid PRIMARY KEY,
    name text NOT NULL
);
CREATE TABLE public.memberships (
    token_hash bytea PRIMARY KEY CHECK (length(token_hash) = 32),
    workspace_id uuid NOT NULL REFERENCES public.workspaces,
    role text NOT NULL CHECK (role IN ('operator', 'viewer'))
);
CREATE TABLE public.accounts (
    workspace_id uuid NOT NULL REFERENCES public.workspaces,
    id uuid NOT NULL,
    name text NOT NULL,
    currency text NOT NULL CHECK (currency IN ('USD', 'EUR', 'IDR')),
    allow_overdraft boolean NOT NULL DEFAULT false,
    balance bigint NOT NULL DEFAULT 0 CHECK (allow_overdraft OR balance >= 0),
    PRIMARY KEY (workspace_id, id),
    UNIQUE (workspace_id, id, currency)
);
CREATE TABLE public.journals (
    workspace_id uuid NOT NULL REFERENCES public.workspaces,
    id uuid NOT NULL,
    sequence bigint GENERATED ALWAYS AS IDENTITY,
    currency text NOT NULL CHECK (currency IN ('USD', 'EUR', 'IDR')),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    payload_hash bytea NOT NULL CHECK (length(payload_hash) = 32),
    memo text NOT NULL CHECK (length(memo) <= 200),
    reversal_of uuid,
    PRIMARY KEY (workspace_id, id),
    UNIQUE (workspace_id, sequence),
    UNIQUE (workspace_id, id, currency),
    UNIQUE (workspace_id, idempotency_key),
    UNIQUE (workspace_id, reversal_of),
    FOREIGN KEY (workspace_id, reversal_of, currency)
        REFERENCES public.journals (workspace_id, id, currency),
    CHECK (reversal_of IS DISTINCT FROM id)
);
CREATE TABLE public.entries (
    workspace_id uuid NOT NULL,
    journal_id uuid NOT NULL,
    account_id uuid NOT NULL,
    currency text NOT NULL,
    amount bigint NOT NULL CHECK (amount BETWEEN -9000000000000000 AND 9000000000000000 AND amount <> 0),
    PRIMARY KEY (workspace_id, journal_id, account_id),
    FOREIGN KEY (workspace_id, journal_id, currency)
        REFERENCES public.journals (workspace_id, id, currency) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (workspace_id, account_id, currency)
        REFERENCES public.accounts (workspace_id, id, currency)
);
CREATE INDEX entries_by_account ON public.entries (workspace_id, account_id);
CREATE INDEX journals_by_time ON public.journals (workspace_id, recorded_at);
CREATE TABLE public.reconciliations (
    workspace_id uuid NOT NULL REFERENCES public.workspaces,
    id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    from_sequence bigint NOT NULL,
    through_sequence bigint NOT NULL,
    report jsonb NOT NULL,
    PRIMARY KEY (workspace_id, id)
);
CREATE INDEX reconciliations_recent ON public.reconciliations (workspace_id, recorded_at DESC);

CREATE FUNCTION public.reject_ledger_mutation() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public AS $$
BEGIN
    RAISE EXCEPTION 'posted history is immutable' USING ERRCODE = 'LC001';
END;
$$;

CREATE FUNCTION public.require_open_journal() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public AS $$
BEGIN
    -- The same transaction-local identity lock protects both entries and sealing.
    PERFORM pg_advisory_xact_lock(hashtextextended(NEW.workspace_id::text || '/' || NEW.journal_id::text, 1));
    IF EXISTS (SELECT 1 FROM public.journals WHERE workspace_id = NEW.workspace_id AND id = NEW.journal_id) THEN
        RAISE EXCEPTION 'journal already sealed' USING ERRCODE = 'LC001';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION public.seal_journal() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE
    line_count bigint;
    total numeric;
    original_reversal uuid;
    line record;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(NEW.workspace_id::text || '/' || NEW.id::text, 1));
    SELECT count(*), sum(amount) INTO line_count, total
      FROM public.entries WHERE workspace_id = NEW.workspace_id AND journal_id = NEW.id;
    IF line_count NOT BETWEEN 2 AND 100 OR total <> 0 THEN
        RAISE EXCEPTION 'journal does not balance' USING ERRCODE = 'LC002';
    END IF;
    IF NEW.reversal_of IS NOT NULL THEN
        SELECT reversal_of INTO original_reversal FROM public.journals
         WHERE workspace_id = NEW.workspace_id AND id = NEW.reversal_of;
        IF NOT FOUND OR original_reversal IS NOT NULL OR EXISTS (
            SELECT account_id, amount FROM public.entries
             WHERE workspace_id = NEW.workspace_id AND journal_id = NEW.id
            EXCEPT
            SELECT account_id, -amount FROM public.entries
             WHERE workspace_id = NEW.workspace_id AND journal_id = NEW.reversal_of
        ) OR line_count <> (SELECT count(*) FROM public.entries
             WHERE workspace_id = NEW.workspace_id AND journal_id = NEW.reversal_of) THEN
            RAISE EXCEPTION 'invalid reversal' USING ERRCODE = 'LC002';
        END IF;
    END IF;
    FOR line IN SELECT account_id, amount FROM public.entries
        WHERE workspace_id = NEW.workspace_id AND journal_id = NEW.id ORDER BY account_id
    LOOP
        UPDATE public.accounts SET balance = balance + line.amount
            WHERE workspace_id = NEW.workspace_id AND id = line.account_id
            AND (allow_overdraft OR balance::numeric + line.amount >= 0);
        IF NOT FOUND THEN
            RAISE EXCEPTION 'insufficient funds' USING ERRCODE = 'LC003';
        END IF;
    END LOOP;
    RETURN NEW;
END;
$$;

CREATE TRIGGER entries_open BEFORE INSERT ON public.entries
FOR EACH ROW EXECUTE FUNCTION public.require_open_journal();
CREATE TRIGGER journals_seal BEFORE INSERT ON public.journals
FOR EACH ROW EXECUTE FUNCTION public.seal_journal();
CREATE TRIGGER journals_immutable BEFORE UPDATE OR DELETE ON public.journals
FOR EACH ROW EXECUTE FUNCTION public.reject_ledger_mutation();
CREATE TRIGGER entries_immutable BEFORE UPDATE OR DELETE ON public.entries
FOR EACH ROW EXECUTE FUNCTION public.reject_ledger_mutation();

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON FUNCTION public.seal_journal() FROM PUBLIC;
GRANT SELECT ON public.memberships, public.accounts, public.journals, public.entries, public.reconciliations TO ledger_app;
GRANT INSERT (workspace_id, id, currency, idempotency_key, payload_hash, memo, reversal_of)
    ON public.journals TO ledger_app;
GRANT INSERT ON public.entries TO ledger_app;
GRANT INSERT (workspace_id, id, from_sequence, through_sequence, report) ON public.reconciliations TO ledger_app;
GRANT USAGE ON SEQUENCE public.journals_sequence_seq TO ledger_app;

-- Row locking requires UPDATE permission. Expose only the scoped lock operation.
CREATE FUNCTION public.lock_ledger_account(target_workspace uuid, target_account uuid)
RETURNS text LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
    SELECT currency FROM public.accounts
    WHERE workspace_id = target_workspace AND id = target_account FOR UPDATE;
$$;
REVOKE ALL ON FUNCTION public.lock_ledger_account(uuid, uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.lock_ledger_account(uuid, uuid) TO ledger_app;

CREATE FUNCTION public.current_workspace() RETURNS uuid
LANGUAGE sql STABLE SET search_path = pg_catalog, public AS $$
    SELECT nullif(current_setting('ledger.workspace', true), '')::uuid;
$$;
CREATE FUNCTION public.authenticate_ledger(digest bytea) RETURNS TABLE(workspace_id uuid, role text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public AS $$
    SELECT m.workspace_id, m.role FROM public.memberships m WHERE m.token_hash = digest;
$$;
REVOKE SELECT ON public.memberships FROM ledger_app;
REVOKE ALL ON FUNCTION public.authenticate_ledger(bytea) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.authenticate_ledger(bytea) TO ledger_app;
CREATE OR REPLACE FUNCTION public.lock_ledger_account(target_workspace uuid, target_account uuid)
RETURNS text LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
    SELECT currency FROM public.accounts WHERE target_workspace = public.current_workspace()
          AND workspace_id = target_workspace AND id = target_account FOR UPDATE;
$$;
ALTER TABLE public.accounts ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.journals ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.entries ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.reconciliations ENABLE ROW LEVEL SECURITY;
CREATE POLICY workspace_accounts ON public.accounts USING (workspace_id = public.current_workspace());
CREATE POLICY workspace_journals ON public.journals USING (workspace_id = public.current_workspace());
CREATE POLICY workspace_entries ON public.entries USING (workspace_id = public.current_workspace());
CREATE POLICY workspace_reconciliations ON public.reconciliations USING (workspace_id = public.current_workspace());
