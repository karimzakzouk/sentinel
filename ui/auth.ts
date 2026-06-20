/**
 * Sentinel auth — supports 3 modes:
 *   disabled  — no auth (dev only)
 *   credentials — shared password
 *   github    — GitHub OAuth + org membership check
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
        if (!expected) return null;
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

// Build the NextAuth config. When disabled, we add a dummy provider
// to avoid the "no providers" error. The authorized callback still
// returns true for all requests in disabled mode.
const dummyProvider = Credentials({
  id: "disabled",
  name: "Disabled",
  credentials: {},
  async authorize() { return null; },
});

const config = {
  providers: providers.length > 0 ? providers : [dummyProvider],
  session: { strategy: "jwt" as const },
  trustHost: true,
  pages: { signIn: "/login" },
  callbacks: {
    async authorized({ auth, request }: any) {
      if (authMode === "disabled") return true;
      const isLoggedIn = !!auth?.user;
      const isLoginPage = request.nextUrl.pathname.startsWith("/login");
      if (isLoginPage) return true;
      return isLoggedIn;
    },
    async signIn({ user, account }: any) {
      if (authMode === "github" && account?.provider === "github") {
        const allowedOrg = process.env.SENTINEL_UI_AUTH_ORG;
        if (!allowedOrg) return false;
        const username = account?.username || user?.name;
        if (!username) return false;
        try {
          const res = await fetch(
            `https://api.github.com/orgs/${allowedOrg}/members/${username}`,
            { headers: { Authorization: `token ${account.access_token}` } }
          );
          return res.status === 204;
        } catch {
          return false;
        }
      }
      return true;
    },
  },
};

export const { handlers, auth, signIn, signOut } = NextAuth(config);
