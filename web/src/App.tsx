import { useEffect, useState } from "react";
import { ApiError, api, setUnauthorizedHandler } from "./api";
import { Dashboard } from "./pages/Dashboard";
import { Login } from "./pages/Login";
import { StatusPage } from "./pages/StatusPage";
import type { Me } from "./types";

/** Two routes: /status is public, everything else is the dashboard behind the login. */
export function App() {
  if (window.location.pathname.replace(/\/+$/, "") === "/status") {
    return <StatusPage />;
  }
  return <Admin />;
}

function Admin() {
  const [me, setMe] = useState<Me>();
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    // Registered first, so a 401 from any request shows the login form.
    setUnauthorizedHandler(() => {
      setMe((m) => (m ? { ...m, authenticated: false } : m));
    });
    api.me().then(
      (m) => {
        if (!cancelled) setMe(m);
      },
      (e: unknown) => {
        if (!cancelled)
          setError(e instanceof Error ? e.message : "Cannot reach the server.");
      },
    );
    return () => {
      cancelled = true;
      setUnauthorizedHandler(undefined);
    };
  }, []);

  if (error) {
    return (
      <main className="login app-bg">
        <p className="alert" role="alert">
          {error}
        </p>
      </main>
    );
  }
  if (!me) {
    return (
      <main className="login app-bg" aria-busy="true">
        <p className="muted">Loading…</p>
      </main>
    );
  }
  if (!me.authenticated) {
    return (
      <Login
        onLoggedIn={(username) => {
          setMe({ authRequired: true, authenticated: true, username });
        }}
      />
    );
  }
  // Only a confirmed logout (or an already expired session) leaves the
  // dashboard; on a network error the session may still be valid, so the
  // Dashboard reports the failure and stays.
  const logout = me.authRequired
    ? async () => {
        try {
          await api.logout();
        } catch (e) {
          if (!(e instanceof ApiError && e.status === 401)) throw e;
        }
        setMe({ authRequired: true, authenticated: false });
      }
    : undefined;
  return <Dashboard username={me.username} onLogout={logout} />;
}
