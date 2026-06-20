/**
 * Next.js 16 proxy (formerly middleware) — protects all routes when auth is enabled.
 * Skips /login and static assets.
 *
 * In 'disabled' mode, the authorized() callback in auth.ts always returns true,
 * so this proxy is a no-op.
 */
import { auth } from "@/auth";

export default auth;

export const config = {
  matcher: [
    /*
     * Match all paths except:
     * - _next/static, _next/image, favicon.ico (static assets)
     * - /api/proxy/* (the proxy needs to work without auth for health checks)
     * - /api/auth/* (NextAuth's own routes)
     */
    "/((?!_next/static|_next/image|favicon.ico|api/proxy/health|api/auth).*)",
  ],
};
