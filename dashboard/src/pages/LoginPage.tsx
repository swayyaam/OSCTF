import { useState, type SyntheticEvent } from "react";
import { useNavigate, useSearchParams, Link } from "react-router-dom";
import { useLogin } from "../api/hooks";
import { RequestError } from "../api/client";
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

  const onSubmit = (e: SyntheticEvent) => {
    e.preventDefault();
    setErr(null);
    login.mutate(
      { email, password },
      {
        onSuccess: () => void navigate(params.get("next") ?? "/challenges"),
        onError: (e) => { setErr(e as RequestError); },
      },
    );
  };

  return (
    <div className="mx-auto max-w-sm py-12">
      <Card className="space-y-4">
        <h1 className="text-xl font-bold text-text">Log in</h1>
        {err && <p className="text-sm text-danger">{err.api.title}</p>}
        <form onSubmit={onSubmit} className="space-y-3">
          <div>
            <Label htmlFor="email">Email</Label>
            <Input
              id="email"
              type="email"
              value={email}
              autoComplete="email"
              onChange={(e) => { setEmail(e.target.value); }}
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
              onChange={(e) => { setPassword(e.target.value); }}
              required
            />
            <FieldError messages={err?.api.fields?.password} />
          </div>
          <Button type="submit" className="w-full" disabled={login.isPending}>
            {login.isPending ? "Signing in…" : "Log in"}
          </Button>
        </form>
        <p className="text-center text-sm text-text-muted">
          No account?{" "}
          <Link to="/register" className="text-primary">
            Register
          </Link>
        </p>
      </Card>
    </div>
  );
}
