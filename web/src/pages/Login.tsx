import { useRef, useState, type SyntheticEvent } from "react";
import { Activity } from "lucide-react";
import { api } from "../api";

export function Login({
  onLoggedIn,
}: {
  onLoggedIn: (username: string) => void;
}) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const passwordRef = useRef<HTMLInputElement>(null);

  const submit = async (e: SyntheticEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const me = await api.login(username, password);
      onLoggedIn(me.username);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not sign in.");
      setPassword("");
      passwordRef.current?.focus();
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className="login app-bg">
      <form className="card" onSubmit={(e) => void submit(e)}>
        <div className="brand" style={{ padding: 0 }}>
          <Activity size={20} aria-hidden="true" />
          Watchkeeper
        </div>
        <h1>Sign in</h1>
        {error && (
          <p className="alert" role="alert">
            {capitalizeFirst(error)}
          </p>
        )}
        <label className="field">
          <span>Username</span>
          <input
            name="username"
            autoComplete="username"
            value={username}
            onChange={(e) => {
              setUsername(e.target.value);
            }}
            required
            autoFocus
            spellCheck={false}
          />
        </label>
        <label className="field">
          <span>Password</span>
          <input
            ref={passwordRef}
            name="password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => {
              setPassword(e.target.value);
            }}
            required
          />
        </label>
        <button
          type="submit"
          className="btn btn-primary"
          disabled={busy || !username || !password}
        >
          {busy ? "Signing in…" : "Sign in"}
        </button>
        <a href="/status" style={{ fontSize: 12 }}>
          Public status page
        </a>
      </form>
    </main>
  );
}

function capitalizeFirst(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1) + (s.endsWith(".") ? "" : ".");
}
