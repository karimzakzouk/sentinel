/**
 * NextAuth configuration — supports 3 auth modes:
 *
 * 1. disabled (default) — no auth, anyone can access the UI.
 *    For dev / air-gapped clusters behind a VPN.
 *
 * 2. credentials — single shared password. Set SENTINEL_UI_PASSWORD env var.
 *    Simple, no OIDC setup needed. Good for small teams.
 *
 * 3. github — GitHub OAuth. Users sign in with their GitHub account.
 *    Requires GITHUB_CLIENT_ID + GITHUB_CLIENT_SECRET + SENTINEL_UI_AUTH_ORG
 *    (only members of that GitHub org can sign in).
 *
 * Configure via the Helm chart:
 *   ui:
 *     auth:
 *       mode: "credentials" | "github" | "disabled"
 *       password: "..."  # for credentials mode
 *       githubOrg: "..."  # for github mode
 *
 * The chart refuses to install with mode=disabled if ingress.enabled=true
 * (you can't expose the UI to the internet without auth).
 */
import NextAuth from "next-auth";
import Credentials from "next-auth/providers/credentials";
import GitHub from "next-auth/providers/github";

const authMode = process.env.SENTINEL_UI_AUTH_MODE || "disabled";

const providers = [];

if (authMode === "credentials") {
  providers.push(
    Credentials({
      name: "Sentinel",
      credentials: {
        password: { label: "Password", type: "password" },
      },
      async authorize(credentials) {
        const expected = process.env.SENTINEL_UI_PASSWORD;
        if (!expected) {
          return null;
        }
        if (credentials?.password === expected) {
          return { id: "1", name: "Sentinel Operator", email: "sentinel@local" };
        }
        return null;
      },
    })
  );
}

if (authMode === "github") {
  providers.push(
    GitHub({
      clientId: process.env.GITHUB_CLIENT_ID,
      clientSecret: process.env.GITHUB_CLIENT_SECRET,
    })
  );
}

export const { handlers, auth, signIn, signOut } = NextAuth({
  providers,
  session: { strategy: "jwt" },
  pages: {
    signIn: "/login",
  },
  callbacks: {
    async authorized({ auth, request }) {
      // If auth is disabled, allow everything
      if (authMode === "disabled") return true;
      // Otherwise require a session
      const isLoggedIn = !!auth?.user;
      const isLoginPage = request.nextUrl.pathname.startsWith("/login");
      if (isLoginPage) return true;
      return isLoggedIn;
    },
    async signIn({ user, account }) {
      // For GitHub mode, restrict to a specific org
      if (authMode === "github" && account?.provider === "github") {
        const allowedOrg = process.env.SENTINEL_UI_AUTH_ORG;
        if (!allowedOrg) return false;
        // Check org membership via GitHub API
        const username = account?.username || user?.name;
        if (!username) return false;
        try {
          const res = await fetch(
            `https://api.github.com/orgs/${allowedOrg}/members/${username}`,
            { headers: { Authorization: `token ${account.access_token}` } }
          );
          return res.status === 204; // 204 = member, 404 = not a member
        } catch {
          return false;
        }
      }
      return true;
    },
  },
});
