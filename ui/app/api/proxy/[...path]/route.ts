/**
 * API proxy — the UI is browser-side and the Sentinel API is on a different
 * origin. We use Next.js's API route to proxy requests so we don't expose
 * CORS issues to the user.
 *
 * In production the Sentinel API is exposed via an Ingress; for now we
 * proxy through here.
 */
import type { NextRequest } from "next/server";

const API_BASE = process.env.API_BASE_URL || process.env.NEXT_PUBLIC_API_BASE_URL || "http://sentinel-core:8000";

export async function GET(
  req: NextRequest,
  ctx: { params: Promise<{ path: string[] }> },
) {
  const { path } = await ctx.params;
  const url = `${API_BASE}/${path.join("/")}?${req.nextUrl.searchParams.toString()}`;
  try {
    const r = await fetch(url, { cache: "no-store" });
    const body = await r.text();
    return new Response(body, {
      status: r.status,
      headers: { "content-type": r.headers.get("content-type") || "application/json" },
    });
  } catch (e) {
    return new Response(JSON.stringify({ error: String(e) }), {
      status: 502,
      headers: { "content-type": "application/json" },
    });
  }
}

export async function POST(
  req: NextRequest,
  ctx: { params: Promise<{ path: string[] }> },
) {
  const { path } = await ctx.params;
  const url = `${API_BASE}/${path.join("/")}`;
  const body = await req.text();
  try {
    const r = await fetch(url, {
      method: "POST",
      body,
      headers: { "content-type": req.headers.get("content-type") || "application/json" },
    });
    const respBody = await r.text();
    return new Response(respBody, {
      status: r.status,
      headers: { "content-type": r.headers.get("content-type") || "application/json" },
    });
  } catch (e) {
    return new Response(JSON.stringify({ error: String(e) }), {
      status: 502,
      headers: { "content-type": "application/json" },
    });
  }
}
