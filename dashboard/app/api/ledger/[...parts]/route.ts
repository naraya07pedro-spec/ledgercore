import { NextRequest } from "next/server";

const readRoutes = new Set(["accounts", "journals", "reconciliations", "operations"]);
const writeRoutes = new Set(["journals", "reconciliations"]);
const reversal = /^journals\/[a-f0-9-]{36}\/reversal$/;
const maxBody = 128 * 1024;

async function boundedBody(req: NextRequest): Promise<ArrayBuffer | undefined> {
  if (!req.body) return undefined;
  const reader = req.body.getReader();
  const bytes = new Uint8Array(maxBody);
  let size = 0;
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) return bytes.buffer.slice(0, size);
      if (size + value.byteLength > maxBody) {
        await reader.cancel();
        throw new RangeError("payload too large");
      }
      bytes.set(value, size);
      size += value.byteLength;
    }
  } finally {
    reader.releaseLock();
  }
}

async function forward(req: NextRequest, context: { params: Promise<{ parts: string[] }> }) {
  const path = (await context.params).parts.join("/");
  const valid = req.method === "GET" ? readRoutes.has(path) : writeRoutes.has(path) || reversal.test(path);
  if (!valid) return Response.json({ error: "Unknown ledger route" }, { status: 404 });
  const headers = new Headers();
  for (const name of ["authorization", "content-type", "idempotency-key"]) {
    const value = req.headers.get(name);
    if (value !== null) headers.set(name, value);
  }
  try {
    const base = process.env.LEDGER_API_URL ?? "http://127.0.0.1:18080";
    const body = req.method === "POST" ? await boundedBody(req) : undefined;
    const response = await fetch(new URL("/v1/" + path + req.nextUrl.search, base), {
      method: req.method, headers, body, cache: "no-store", redirect: "error",
      signal: AbortSignal.any([req.signal, AbortSignal.timeout(6500)]),
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
  }
}

export { forward as GET, forward as POST };
