import { useState, type SyntheticEvent } from "react";
import { useNavigate, useSearchParams, Link } from "react-router-dom";
import { useAuthProviders, useLogin } from "../api/hooks";
import { API_BASE, RequestError } from "../api/client";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Card, FieldError, Label } from "../components/ui/misc";

export function LoginPage() {
  const login = useLogin();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<RequestError | null>(null);
  const { data: providers } = useAuthProviders();

  // The callback sends every failure here with a generic marker; it deliberately does not say
  // whether the account exists, is banned, or was refused by policy.
  const ssoFailed = params.get("error") === "sso";
  // Until the listing loads, assume the built-in is available: showing the form and having it
  // rejected is a better first paint than hiding the only login a normal deployment has.
  const emailEnabled = !providers || providers.some((p) => !p.redirect);
  const redirectProviders = (providers ?? []).filter((p) => p.redirect);

  const onSubmit = (e: SyntheticEvent) => {
    e.preventDefault();
    setErr(null);
    login.mutate(
      { email, password },
      {
        onSuccess: () => void navigate(params.get("next") ?? "/challenges"),
        onError: (e) => {
          setErr(e as RequestError);
        },
      },
    );
  };

  return (
    <div className="mx-auto max-w-sm py-12">
      <Card className="space-y-4">
        <h1 className="text-xl font-bold text-text">Log in</h1>
        {err && <p className="text-sm text-danger">{err.api.title}</p>}
        {ssoFailed && (
          <p className="text-sm text-danger">
            That login could not be completed. Try again, or use another method.
          </p>
        )}

        {redirectProviders.length > 0 && (
          <div className="space-y-2">
            {redirectProviders.map((p) => (
              // A full page navigation, not a fetch: this endpoint answers 302 to the identity
              // provider and sets the state cookie the callback is checked against.
              <a
                key={p.id}
                href={`${API_BASE}/auth/${p.id}/login`}
                className="block w-full rounded-md border border-border px-3 py-2 text-center text-sm font-medium text-text hover:bg-surface-2"
              >
                Continue with {p.id}
              </a>
            ))}
            {emailEnabled && (
              <div className="flex items-center gap-3 pt-1">
                <span className="h-px flex-1 bg-border" />
                <span className="text-xs text-text-muted">or</span>
                <span className="h-px flex-1 bg-border" />
              </div>
            )}
          </div>
        )}

        {!emailEnabled && redirectProviders.length === 0 && (
          <p className="text-sm text-text-muted">
            This deployment has no login method configured. Contact the organizer.
          </p>
        )}

        {emailEnabled && (
          <form onSubmit={onSubmit} className="space-y-3">
            <div>
              <Label htmlFor="email">Email</Label>
              <Input
                id="email"
                type="email"
                value={email}
                autoComplete="email"
                onChange={(e) => {
                  setEmail(e.target.value);
                }}
                required
              />
              <FieldError messages={err?.api.fields?.email} />
            </div>
            <div>
              <Label htmlFor="password">Password</Label>
              <Input
                id="password"
                type="password"
                value={password}
                autoComplete="current-password"
                onChange={(e) => {
                  setPassword(e.target.value);
                }}
                required
              />
              <FieldError messages={err?.api.fields?.password} />
            </div>
            <Button type="submit" className="w-full" disabled={login.isPending}>
              {login.isPending ? "Signing in…" : "Log in"}
            </Button>
          </form>
        )}
        {emailEnabled && (
          <p className="text-center text-sm text-text-muted">
            No account?{" "}
            <Link to="/register" className="text-primary">
              Register
            </Link>
          </p>
        )}
      </Card>
    </div>
  );
}
