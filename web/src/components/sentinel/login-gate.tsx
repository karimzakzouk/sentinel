"use client";

import { useState, useEffect } from "react";
import { Shield, Lock, ArrowRight, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { getHealth, setToken, validateToken, type HealthResponse } from "@/lib/api";

export function LoginGate({ children }: { children: React.ReactNode }) {
  const [health, setHealth] = useState<HealthResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [token, setTokenInput] = useState("");
  const [error, setError] = useState("");
  const [verifying, setVerifying] = useState(false);

  useEffect(() => {
    getHealth()
      .then((h) => {
        setHealth(h);
        setLoading(false);
      })
      .catch(() => {
        setHealth({ status: "error" });
        setLoading(false);
      });
  }, []);

  // If Sentinel is unreachable
  if (loading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[#09090B]">
        <div className="flex flex-col items-center gap-4">
          <Shield className="h-10 w-10 animate-pulse text-cyan-500" />
          <p className="text-sm text-zinc-500">Connecting to Sentinel...</p>
        </div>
      </div>
    );
  }

  if (health?.status === "error") {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[#09090B]">
        <div className="flex flex-col items-center gap-4">
          <Shield className="h-10 w-10 text-red-500" />
          <p className="text-sm text-zinc-400">Cannot reach Sentinel API</p>
          <p className="text-xs text-zinc-600">
            Make sure Sentinel is running and accessible at this URL.
          </p>
        </div>
      </div>
    );
  }

  // If no auth required, show the app directly
  if (!health?.auth_required) {
    return <>{children}</>;
  }

  // Check if token is already stored
  const storedToken = typeof window !== "undefined" ? localStorage.getItem("sentinel_api_token") : null;

  if (storedToken) {
    // Validate the stored token
    return <TokenValidator token={storedToken}>{children}</TokenValidator>;
  }

  // Show login gate
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token.trim()) return;
    setVerifying(true);
    setError("");
    const valid = await validateToken(token.trim());
    if (valid) {
      setToken(token.trim());
      window.location.reload();
    } else {
      setError("Invalid token — check your SENTINEL_API_TOKEN value");
      setVerifying(false);
    }
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-[#09090B]">
      <div className="w-full max-w-sm">
        <div className="mb-8 flex flex-col items-center gap-3">
          <div className="flex h-14 w-14 items-center justify-center rounded-xl bg-cyan-500/10 ring-1 ring-cyan-500/20">
            <Shield className="h-7 w-7 text-cyan-500" />
          </div>
          <div className="text-center">
            <h1 className="text-xl font-semibold text-zinc-50">Sentinel</h1>
            <p className="text-xs text-zinc-500">
              {health?.version ? `v${health.version}` : "Autonomous AI SRE"}
            </p>
          </div>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-2">
            <label className="text-xs font-medium text-zinc-400">
              API Token
            </label>
            <div className="relative">
              <Lock className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-zinc-600" />
              <Input
                type="password"
                value={token}
                onChange={(e) => setTokenInput(e.target.value)}
                placeholder="Paste your API token..."
                className="h-10 border-zinc-800 bg-zinc-900 pl-9 text-sm text-zinc-50 placeholder:text-zinc-600 focus:border-cyan-500"
                autoFocus
                autoComplete="off"
              />
            </div>
            {error && (
              <p className="text-xs text-red-400">{error}</p>
            )}
          </div>

          <Button
            type="submit"
            disabled={!token.trim() || verifying}
            className="h-10 w-full bg-cyan-500 text-zinc-950 hover:bg-cyan-400 disabled:opacity-50"
          >
            {verifying ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                Verifying...
              </>
            ) : (
              <>
                Connect
                <ArrowRight className="ml-2 h-4 w-4" />
              </>
            )}
          </Button>
        </form>

        <div className="mt-6 rounded-md border border-zinc-800 bg-zinc-900/50 p-3">
          <p className="text-[11px] leading-relaxed text-zinc-500">
            Get your token from the Helm install output or:
          </p>
          <code className="mt-1 block rounded bg-zinc-950 px-2 py-1 text-[11px] text-zinc-400">
            kubectl -n sentinel exec deploy/sentinel -- printenv SENTINEL_API_TOKEN
          </code>
        </div>
      </div>
    </div>
  );
}

function TokenValidator({
  token,
  children,
}: {
  token: string;
  children: React.ReactNode;
}) {
  const [state, setState] = useState<"checking" | "valid" | "invalid">("checking");

  useEffect(() => {
    validateToken(token)
      .then((valid) => setState(valid ? "valid" : "invalid"))
      .catch(() => setState("invalid"));
  }, [token]);

  if (state === "checking") {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[#09090B]">
        <div className="flex flex-col items-center gap-3">
          <Shield className="h-8 w-8 animate-pulse text-cyan-500" />
          <p className="text-xs text-zinc-500">Verifying token...</p>
        </div>
      </div>
    );
  }

  if (state === "invalid") {
    // Clear the invalid token and reload to show the login form
    localStorage.removeItem("sentinel_api_token");
    window.location.reload();
    return null;
  }

  return <>{children}</>;
}
