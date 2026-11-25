import { NextRequest } from "next/server";

const readRoutes = new Set(["accounts", "journals", "reconciliations", "operations"]);
const writeRoutes = new Set(["journals", "reconciliations"]);
const reversal = /^journals\/[a-f0-9-]{36}\/reversal$/;
const maxBody = 128 * 1024;
const maxInFlight = 64;
let inFlight = 0;

async function boundedBody(req: NextRequest, signal: AbortSignal): Promise<ArrayBuffer | undefined> {
  if (!req.body) return undefined;
  const reader = req.body.getReader();
  const bytes = new Uint8Array(maxBody);
  let size = 0;
  const cancel = () => { void reader.cancel(signal.reason).catch(() => {}); };
  signal.addEventListener("abort", cancel, { once: true });
  try {
    signal.throwIfAborted();
    while (true) {
      const { value, done } = await reader.read();
      signal.throwIfAborted();
      if (done) return bytes.buffer.slice(0, size);
      if (size + value.byteLength > maxBody) {
        await reader.cancel();
        throw new RangeError("payload too large");
      }
      bytes.set(value, size);
      size += value.byteLength;
    }
  } finally {
    signal.removeEventListener("abort", cancel);
    reader.releaseLock();
  }
}

async function forward(req: NextRequest, context: { params: Promise<{ parts: string[] }> }) {
  const path = (await context.params).parts.join("/");
  const valid = req.method === "GET" ? readRoutes.has(path) : writeRoutes.has(path) || reversal.test(path);
  if (!valid) return Response.json({ error: "Unknown ledger route" }, { status: 404 });
  const origin = req.headers.get("origin");
  const site = req.headers.get("sec-fetch-site");
  const sameHost = !origin || ["http://", "https://"].some(scheme => origin === scheme + req.headers.get("host"));
  if (!sameHost || (site && site !== "same-origin" && site !== "none")) {
    return Response.json({ error: "Cross-origin request denied" }, { status: 403 });
  }
  if (!/^Bearer [a-f0-9]{64}$/.test(req.headers.get("authorization") ?? "")) {
    return Response.json({ error: "Workspace token required" }, { status: 401 });
  }
  const headers = new Headers();
  for (const name of ["authorization", "content-type", "idempotency-key"]) {
    const value = req.headers.get(name);
    if (value !== null) headers.set(name, value);
  }
  if (inFlight >= maxInFlight) {
    return Response.json({ error: "Dashboard request capacity reached. Retry writes with the same key." }, { status: 503 });
  }
  inFlight++;
  try {
    const base = process.env.LEDGER_API_URL ?? "http://127.0.0.1:18080";
    const signal = AbortSignal.any([req.signal, AbortSignal.timeout(6500)]);
    const body = req.method === "POST" ? await boundedBody(req, signal) : undefined;
    const response = await fetch(new URL("/v1/" + path + req.nextUrl.search, base), {
      method: req.method, headers, body, cache: "no-store", redirect: "error",
      signal,
    });
    const out = new Headers({ "cache-control": "no-store" });
    for (const name of ["content-type", "idempotency-replayed", "x-trace-id", "www-authenticate"]) {
      const value = response.headers.get(name);
      if (value !== null) out.set(name, value);
    }
    return new Response(response.body, { status: response.status, headers: out });
  } catch (error) {
    if (error instanceof RangeError) return Response.json({ error: "Payload too large" }, { status: 413 });
    return Response.json({ error: "Ledger API unavailable. Retry writes with the same key." }, { status: 503 });
  } finally {
    inFlight--;
  }
}

export { forward as GET, forward as POST };
