"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { signIn } from "next-auth/react";

export default function LoginPage() {
  const [password, setPassword] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const router = useRouter();

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setLoading(true);
    setError("");
    const result = await signIn("credentials", {
      password,
      redirect: false,
    });
    setLoading(false);
    if (result?.error) {
      setError("Invalid password");
    } else {
      router.push("/");
      router.refresh();
    }
  };

  return (
    <div style={{
      minHeight: "100vh",
      display: "flex",
      alignItems: "center",
      justifyContent: "center",
      background: "var(--color-bg)",
    }}>
      <div className="panel" style={{ width: 360, padding: 0 }}>
        <div className="panel-header">
          <h2 style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <span style={{ color: "var(--color-accent)" }}>S</span>
            Sentinel Login
          </h2>
        </div>
        <div className="panel-body">
          <form onSubmit={handleSubmit} style={{ display: "flex", flexDirection: "column", gap: 12 }}>
            <input
              type="password"
              placeholder="Password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoFocus
              style={{
                background: "var(--color-bg)",
                border: "1px solid var(--color-border)",
                borderRadius: 3,
                padding: "8px 12px",
                color: "var(--color-text)",
                fontSize: 14,
              }}
            />
            {error && (
              <div style={{ color: "var(--color-danger)", fontSize: 12 }}>{error}</div>
            )}
            <button
              type="submit"
              disabled={loading}
              style={{
                background: "var(--color-accent)",
                color: "var(--color-bg)",
                border: "none",
                borderRadius: 3,
                padding: "8px 16px",
                fontWeight: 600,
                cursor: loading ? "not-allowed" : "pointer",
                fontSize: 13,
              }}
            >
              {loading ? "Signing in..." : "Sign in"}
            </button>
          </form>
        </div>
      </div>
    </div>
  );
}
