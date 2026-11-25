-- Consolidate the posting primitive so runtime SQL cannot insert or append lines
-- independently of sealing, even from an old snapshot.
CREATE FUNCTION public.post_ledger(
    target_workspace uuid, target_journal uuid, target_currency text,
    target_key text, target_hash bytea, target_memo text, target_reversal uuid,
    target_accounts text[], target_amounts bigint[]
) RETURNS TABLE(sequence bigint, recorded_at timestamptz)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE
    line record;
    account_currency text;
BEGIN
    IF target_workspace IS DISTINCT FROM public.current_workspace() THEN
        RAISE EXCEPTION 'workspace scope required' USING ERRCODE = '42501';
    END IF;
    IF cardinality(target_accounts) IS DISTINCT FROM cardinality(target_amounts)
       OR cardinality(target_accounts) NOT BETWEEN 2 AND 100 THEN
        RAISE EXCEPTION 'invalid line count' USING ERRCODE = 'LC002';
    END IF;
    FOR line IN
        SELECT a.account::uuid AS account, a.amount
          FROM unnest(target_accounts, target_amounts) AS a(account, amount) ORDER BY a.account::uuid
    LOOP
        -- Acquire every conflicting lock before inserting any FK references.
        SELECT currency INTO account_currency FROM public.accounts
            WHERE workspace_id = target_workspace AND id = line.account FOR UPDATE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'account not found' USING ERRCODE = 'LC004';
        END IF;
        IF account_currency IS DISTINCT FROM target_currency THEN
            RAISE EXCEPTION 'account currency mismatch' USING ERRCODE = 'LC002';
        END IF;
    END LOOP;
    INSERT INTO public.entries (workspace_id,journal_id,account_id,currency,amount)
        SELECT target_workspace,target_journal,a.account::uuid,target_currency,a.amount
          FROM unnest(target_accounts,target_amounts) AS a(account,amount);
    -- Sequence allocation and commit are serialized for each workspace.
    PERFORM pg_advisory_xact_lock(hashtextextended(target_workspace::text,2));
    RETURN QUERY INSERT INTO public.journals AS j
        (workspace_id,id,currency,idempotency_key,payload_hash,memo,reversal_of)
        VALUES (target_workspace,target_journal,target_currency,target_key,target_hash,target_memo,target_reversal)
        RETURNING j.sequence,j.recorded_at;
END;
$$;
REVOKE ALL ON FUNCTION public.post_ledger(uuid,uuid,text,text,bytea,text,uuid,text[],bigint[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.post_ledger(uuid,uuid,text,text,bytea,text,uuid,text[],bigint[]) TO ledger_app;
REVOKE INSERT (workspace_id,id,currency,idempotency_key,payload_hash,memo,reversal_of)
    ON public.journals FROM ledger_app;
REVOKE INSERT ON public.entries FROM ledger_app;
REVOKE USAGE ON SEQUENCE public.journals_sequence_seq FROM ledger_app;
DROP FUNCTION public.lock_ledger_account(uuid,uuid);
