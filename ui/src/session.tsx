import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { api, type Me } from "./api";

// Who is signed in to the console. Changes made through it are made with
// that person's own token, so their Kubernetes permissions decide them.
interface Session {
  // me is the signed-in person, null when nobody is, undefined while it is
  // not known or when this Sentinel serves no console.
  me: Me | null | undefined;
  available: boolean;
  signIn: (token: string) => Promise<void>;
  signOut: () => Promise<void>;
}

const SessionContext = createContext<Session>({
  me: undefined,
  available: false,
  signIn: async () => {},
  signOut: async () => {},
});

export function SessionProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<Me | null | undefined>(undefined);
  const [available, setAvailable] = useState(false);
  useEffect(() => {
    api.whoami().then(
      (who) => {
        setAvailable(who !== undefined);
        setMe(who);
      },
      () => setAvailable(false),
    );
  }, []);
  const signIn = useCallback(async (token: string) => setMe(await api.signIn(token)), []);
  const signOut = useCallback(async () => {
    await api.signOut();
    setMe(null);
  }, []);
  return <SessionContext.Provider value={{ me, available, signIn, signOut }}>{children}</SessionContext.Provider>;
}

export const useSession = () => useContext(SessionContext);

// SignIn asks for a Kubernetes token, which the cluster checks.
export function SignIn({ onClose }: { onClose: () => void }) {
  const { signIn } = useSession();
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      await signIn(token);
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="dialog-backdrop" role="presentation" onClick={onClose}>
      <div className="dialog" role="dialog" aria-modal="true" aria-labelledby="sign-in-title" onClick={(e) => e.stopPropagation()}>
        <h2 id="sign-in-title">Sign in</h2>
        <p>
          Use a token for your own Kubernetes identity. Everything you do here is done as you, with your permissions, as
          kubectl would; Sentinel's own account never makes these changes.
        </p>
        <p className="muted">
          For example <code>kubectl create token NAME -n NAMESPACE</code> for a service account, or the token your single
          sign-on gives you.
        </p>
        <textarea
          className="field mono"
          rows={4}
          value={token}
          placeholder="eyJhbGciOi…"
          aria-label="Token"
          autoFocus
          onChange={(e) => setToken(e.target.value)}
        />
        {error && <div className="error-banner">{error}</div>}
        <div className="dialog-actions">
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={!token.trim() || busy} onClick={submit}>
            {busy ? "Checking…" : "Sign in"}
          </button>
        </div>
      </div>
    </div>
  );
}

// Act runs a console change and says how it went, in place.
export function useAct(): [(label: string, run: () => Promise<{ done: string }>) => Promise<void>, string, string, boolean] {
  const [done, setDone] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const act = useCallback(async (label: string, run: () => Promise<{ done: string }>) => {
    if (!confirm(label + "?")) return;
    setBusy(true);
    setError("");
    try {
      setDone((await run()).done);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }, []);
  return [act, done, error, busy];
}
