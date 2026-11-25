"use client";

import { Fragment, useEffect, useRef, useState, type FormEvent } from "react";

type Account = { id: string; name: string; currency: string; balance: string; allowOverdraft: boolean };
type Journal = { id: string; sequence: string; currency: string; memo: string; recordedAt: string; reversalOf?: string; entries: { account: string; amount: string }[] };
type Report = { id: string; recordedAt: string; from: string; through: string; matched: number; differences: { journal: string; kind: string; occurrences?: number; expected?: { credits: string; currency: string }; observed?: { credits: string; currency: string } }[] };
type Ops = { role: string; httpRequests: number; uptimeSeconds: number; databaseAcquired: number; databasePoolLimit: number };
type Tab = "Overview" | "Journal" | "Reconciliation";
type Pending = { path: string; body?: string; key?: string; message: string };

function money(value: string, currency: string) {
  const n = BigInt(value);
  const magnitude = n < 0n ? -n : n;
  const scale = 100n;
  return `${n < 0n ? "−" : ""}${(magnitude / scale).toLocaleString("en-US")}.${String(magnitude % scale).padStart(2, "0")} ${currency}`;
}

class RequestError extends Error {
  constructor(message: string, readonly status: number) { super(message); }
}

async function call<T>(token: string, path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  headers.set("Authorization", "Bearer " + token);
  const response = await fetch("/api/ledger/" + path, { ...init, headers, cache: "no-store" });
  const data = await response.json();
  if (!response.ok) throw new RequestError(data.error ?? "Request failed", response.status);
  return data as T;
}

function errorText(error: unknown) { return error instanceof Error ? error.message : "Request failed"; }

export default function Page() {
  const [token, setToken] = useState("");
  const [draft, setDraft] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function connect(e: FormEvent) {
    e.preventDefault();
    setError("");
    setBusy(true);
    try {
      await call<Ops>(draft.trim(), "operations");
      setToken(draft.trim());
      setDraft("");
    } catch (err) { setError(errorText(err)); }
    finally { setBusy(false); }
  }

  if (token) return <Console token={token} disconnect={() => setToken("")} />;
  return <main className="login"><div className="brand"><Mark /> LedgerCore</div>
    <form className="panel login-form" onSubmit={connect}>
      <h1>Ledger operations</h1>
      <p>Connect to your synthetic workspace with an operator or viewer token.</p>
      <label>Workspace token<input type="password" value={draft} onChange={e => setDraft(e.target.value)} autoComplete="off" required /></label>
      <button className="primary" disabled={busy}>{busy ? "Connecting…" : "Connect workspace"}</button>
      {error && <p className="error" role="alert">{error}</p>}
    </form>
  </main>;
}

function Mark() { return <span className="mark" aria-hidden="true">◈</span>; }

function Console({ token, disconnect }: { token: string; disconnect: () => void }) {
  const [tab, setTab] = useState<Tab>("Overview");
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [journals, setJournals] = useState<Journal[]>([]);
  const [reports, setReports] = useState<Report[]>([]);
  const [ops, setOps] = useState<Ops>();
  const [through, setThrough] = useState("");
  const [historic, setHistoric] = useState<Account[]>();
  const [search, setSearch] = useState("");
  const [currency, setCurrency] = useState("");
  const [before, setBefore] = useState("");
  const [expanded, setExpanded] = useState<string>();
  const [refresh, setRefresh] = useState(0);
  const [pending, setPending] = useState<Pending>();
  const [active, setActive] = useState(false);
  const submitting = useRef(false);
  const historyRequest = useRef<AbortController | null>(null);
  useEffect(() => () => historyRequest.current?.abort(), []);
  const [notice, setNotice] = useState("");
  const [requestError, setRequestError] = useState("");
  const [loadError, setLoadError] = useState("");
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const abort = new AbortController();
    const filter = new URLSearchParams({ q: search, currency, before });
    Promise.all([
      call<Account[]>(token, "accounts", { signal: abort.signal }),
      call<Journal[]>(token, "journals?" + filter, { signal: abort.signal }),
      call<Report[]>(token, "reconciliations", { signal: abort.signal }),
      call<Ops>(token, "operations", { signal: abort.signal }),
    ]).then(([a, j, r, o]) => {
      if (abort.signal.aborted) return;
      setAccounts(a);
      setJournals(j);
      setReports(r);
      setOps(o);
      setLoadError("");
      setLoading(false);
    }).catch(err => { if (!abort.signal.aborted) { setLoadError(errorText(err)); setLoading(false); } });
    return () => abort.abort();
  }, [token, search, currency, before, refresh]);

  const names = new Map(accounts.map(a => [a.id, a.name]));
  const operator = ops?.role === "operator";
  const reversed = new Set(journals.flatMap(j => j.reversalOf ? [j.reversalOf] : []));

  async function send(request: Pending) {
    if (submitting.current) return;
    submitting.current = true;
    setActive(true);
    setPending(request);
    setRequestError("");
    setNotice("");
    try {
      await call(token, request.path, { method: "POST", body: request.body, headers: {
        ...(request.body ? { "Content-Type": "application/json" } : {}),
        ...(request.key ? { "Idempotency-Key": request.key } : {}),
      } });
      setNotice(request.message);
      setPending(undefined);
      setRefresh(n => n + 1);
    } catch (err) {
      setRequestError(errorText(err));
      if (err instanceof RequestError && err.status >= 400 && err.status < 500) setPending(undefined);
    } finally {
      submitting.current = false;
      setActive(false);
    }
  }

  function transfer(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    const from = accounts.find(a => a.id === form.get("from"));
    const to = accounts.find(a => a.id === form.get("to"));
    const amount = String(form.get("amount"));
    if (!from || !to || from.id === to.id || from.currency !== to.currency || !/^[1-9][0-9]{0,15}$/.test(amount)) {
      setRequestError("Choose two different accounts in the same currency and enter a positive minor-unit amount.");
      return;
    }
    void send({ path: "journals", key: crypto.randomUUID(), message: "Transfer posted.", body: JSON.stringify({
      currency: from.currency, memo: form.get("memo"), entries: [{ account: from.id, amount: "-" + amount }, { account: to.id, amount }],
    }) });
  }

  async function history(e: FormEvent) {
    e.preventDefault();
    setRequestError("");
    setHistoric(undefined);
    historyRequest.current?.abort();
    const controller = new AbortController();
    historyRequest.current = controller;
    try {
      const balances = await call<Account[]>(token, "accounts?through=" + encodeURIComponent(through), { signal: controller.signal });
      if (!controller.signal.aborted) setHistoric(balances);
    } catch (err) {
      if (!controller.signal.aborted) setRequestError(errorText(err));
    }
  }

  function reconcile(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    let lines: unknown;
    try { lines = JSON.parse(String(form.get("statement"))); }
    catch { setRequestError("The statement must be a valid JSON array."); return; }
    void send({ path: "reconciliations", key: crypto.randomUUID(), message: "Reconciliation saved.", body: JSON.stringify({
      from: form.get("fromSequence"), through: form.get("throughSequence"), lines,
    }) });
  }

  return <div className="shell">
    <aside className="sidebar">
      <div className="brand"><Mark /> LedgerCore</div>
      <nav aria-label="Workspace">
        {(["Overview", "Journal", "Reconciliation"] as const).map(t =>
          <button key={t} aria-current={tab === t ? "page" : undefined} onClick={() => setTab(t)}>{t}</button>)}
      </nav>
      <div className="sidebar-foot"><span className="signal" /> Synthetic workspace</div>
    </aside>
    <main className="workspace">
      <header className="topbar"><span>Operations workspace</span><div>{ops?.role && <span className="role">{ops.role}</span>}<button onClick={disconnect} disabled={active || !!pending}>Disconnect</button></div></header>
      <div className="content">
        <div className="headline"><div><h1>{tab === "Overview" ? "Ledger overview" : tab}</h1>
          <p>{tab === "Overview" ? "Inspect balances and post synthetic transfers." : tab === "Journal" ? "Search posted transactions and inspect their account entries." : "Compare an external synthetic statement with the journal."}</p></div>
          <button onClick={() => setRefresh(n => n + 1)}>Refresh</button>
        </div>
        {loading && <p role="status">Loading workspace…</p>}
        {loadError && <p className="error" role="alert">{loadError}</p>}
        {notice && <p className="notice" role="status">{notice}</p>}
        {requestError && <p className="error" role="alert">{requestError}</p>}
        {pending?.key && !active && <div className="notice pending"><span>Outcome unknown. Retry to recover the result for <code>{pending.key}</code>.</span>
          <button onClick={() => void send(pending)}>Retry same request</button>
          <button onClick={() => { if (window.confirm("Inspect the ledger before submitting a new request. Clear this retry?")) { setPending(undefined); setRequestError(""); } }}>Clear pending request</button></div>}
        {tab === "Overview" && <>
          <section aria-labelledby="balances"><h2 id="balances">Account balances</h2><div className="balances">
            {accounts.map(a => <article className="panel balance" key={a.id}><span>{a.name}</span><strong>{money(a.balance, a.currency)}</strong>
              <small>{a.allowOverdraft ? "Negative balance permitted" : "Overdraft prohibited"}</small></article>)}
          </div></section>
          <div className="columns">
            {operator && <section className="panel section"><h2>Synthetic transfer</h2><form onSubmit={transfer}>
              <div className="fields"><label>From<select name="from" required defaultValue=""><option value="">Select account</option>{accounts.map(a => <option key={a.id} value={a.id}>{a.name}</option>)}</select></label>
              <label>To<select name="to" required defaultValue=""><option value="">Select account</option>{accounts.map(a => <option key={a.id} value={a.id}>{a.name}</option>)}</select></label></div>
              <label>Amount in minor units<input name="amount" placeholder="2500" inputMode="numeric" required pattern="[1-9][0-9]{0,15}" /></label>
              <label>Memo<input name="memo" maxLength={200} placeholder="Optional reference" /></label>
              <button className="primary" disabled={!!pending || active}>Post transfer</button>
            </form></section>}
            <section className="panel section"><h2>Historical balances</h2><form onSubmit={history}>
              <label>Through journal sequence<input inputMode="numeric" pattern="[0-9]+" required value={through} onChange={e => setThrough(e.target.value)} placeholder="Sequence number" /></label>
              <button>Inspect balances</button>
            </form>
              {historic && <ul className="history" aria-label="Historical results">{historic.map(a => <li key={a.id}><span>{a.name}</span><b>{money(a.balance, a.currency)}</b></li>)}</ul>}
            </section>
          </div>
          {ops && <section className="panel section"><h2>API instance</h2><dl className="metrics">
            <div><dt>Completed HTTP requests</dt><dd>{ops.httpRequests.toLocaleString()}</dd></div>
            <div><dt>Database connections acquired</dt><dd>{ops.databaseAcquired} / {ops.databasePoolLimit}</dd></div>
            <div><dt>Uptime</dt><dd>{ops.uptimeSeconds.toLocaleString()} s</dd></div>
          </dl></section>}
        </>}
        {tab === "Journal" && <section className="panel section">
          <div className="fields filters"><label>Search memo or ID<input value={search} maxLength={100} onChange={e => { setBefore(""); setSearch(e.target.value); }} /></label>
          <label>Currency<select value={currency} onChange={e => { setBefore(""); setCurrency(e.target.value); }}><option value="">All currencies</option><option>USD</option><option>EUR</option><option>IDR</option></select></label></div>
          <div className="table-wrap"><table><thead><tr><th>Sequence</th><th>Transaction</th><th>Currency</th><th>Recorded</th><th>Entries</th></tr></thead>
          <tbody>{journals.map(j => <Fragment key={j.id}><tr>
            <td className="number">{j.sequence}</td><td><b>{j.memo || "Journal posting"}</b><small className="number">{j.id}</small>{j.reversalOf && <small>Reverses {j.reversalOf}</small>}</td>
            <td>{j.currency}</td><td><time dateTime={j.recordedAt}>{new Date(j.recordedAt).toLocaleString()}</time></td>
            <td><button aria-expanded={expanded === j.id} onClick={() => setExpanded(expanded === j.id ? undefined : j.id)}>{expanded === j.id ? "Hide" : "Inspect"}</button></td>
          </tr>{expanded === j.id && <tr className="detail"><td colSpan={5}>
            <ul className="history">{j.entries.map(line => <li key={line.account}><span>{names.get(line.account) ?? line.account}</span><span>{BigInt(line.amount) > 0n ? "Credit" : "Debit"}</span><b>{money(line.amount, j.currency)}</b></li>)}</ul>
            {operator && !j.reversalOf && !reversed.has(j.id) && <button disabled={!!pending || active} onClick={() => {
              if (window.confirm("Post an offsetting journal for this transaction?")) void send({
                path: "journals/" + j.id + "/reversal", key: crypto.randomUUID(), message: "Reversal posted.",
              });
            }}>Reverse transaction</button>}</td></tr>}</Fragment>)}</tbody></table></div>
          {journals.length === 0 && <p>No matching journal transactions.</p>}
          <div className="pagination">{before && <button onClick={() => setBefore("")}>Newest</button>}
            {journals.length === 25 && <button onClick={() => setBefore(journals.at(-1)!.sequence)}>Older transactions</button>}</div>
        </section>}
        {tab === "Reconciliation" && <>
          {operator && <section className="panel section"><h2>Run reconciliation</h2><form onSubmit={reconcile}>
            <div className="fields"><label>From sequence<input name="fromSequence" required inputMode="numeric" pattern="[1-9][0-9]*" /></label>
            <label>Through sequence<input name="throughSequence" required inputMode="numeric" pattern="[1-9][0-9]*" /></label></div>
            <label>Statement JSON<textarea name="statement" required rows={5} maxLength={100000} aria-describedby="statement-help" /></label>
            <small id="statement-help">Each line requires a journal ID, currency, and credits as a minor-unit string.</small>
            <button className="primary" disabled={!!pending || active}>Compare statement</button>
          </form></section>}
          <section className="panel section"><h2>Recent results</h2>{reports.length === 0 && <p>No reconciliation reports yet.</p>}
            {reports.map(r => <details key={r.id}><summary>Sequences {r.from}–{r.through} · {r.matched} matched · {r.differences.length} findings</summary>
              <time dateTime={r.recordedAt}>{new Date(r.recordedAt).toLocaleString()}</time>
              <ul className="history">{r.differences.map((d, i) => <li key={i}><code>{d.journal}</code> <b>{d.kind}{d.occurrences ? " (" + d.occurrences + ")" : ""}</b>
                {d.expected && <span>Expected {money(d.expected.credits, d.expected.currency)}</span>}
                {d.observed && <span>Observed {money(d.observed.credits, d.observed.currency)}</span>}</li>)}</ul>
            </details>)}</section>
        </>}
      </div>
    </main>
  </div>;
}
