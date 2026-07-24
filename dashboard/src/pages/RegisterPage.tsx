import { useState, type SyntheticEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useRegister } from "../api/hooks";
import { RequestError } from "../api/client";
import { Button } from "../components/ui/button";
import { Input } from "../components/ui/input";
import { Card, FieldError, Label } from "../components/ui/misc";

export function RegisterPage() {
  const register = useRegister();
  const navigate = useNavigate();
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<RequestError | null>(null);

  const onSubmit = (e: SyntheticEvent) => {
    e.preventDefault();
    setErr(null);
    register.mutate(
      { username, email, password },
      {
        onSuccess: () => void navigate("/challenges"),
        onError: (e) => { setErr(e as RequestError); },
      },
    );
  };

  return (
    <div className="mx-auto max-w-sm py-12">
      <Card className="space-y-4">
        <h1 className="text-xl font-bold text-text">Create an account</h1>
        {err && !err.api.fields && <p className="text-sm text-danger">{err.api.detail ?? err.api.title}</p>}
        <form onSubmit={onSubmit} className="space-y-3">
          <div>
            <Label htmlFor="username">Username</Label>
            <Input
              id="username"
              value={username}
              autoComplete="username"
              onChange={(e) => { setUsername(e.target.value); }}
              required
            />
            <FieldError messages={err?.api.fields?.username} />
          </div>
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
              autoComplete="new-password"
              onChange={(e) => { setPassword(e.target.value); }}
              required
            />
            <FieldError messages={err?.api.fields?.password} />
          </div>
          <Button type="submit" className="w-full" disabled={register.isPending}>
            {register.isPending ? "Creating…" : "Register"}
          </Button>
        </form>
        <p className="text-center text-sm text-text-muted">
          Already have an account?{" "}
          <Link to="/login" className="text-primary">
            Log in
          </Link>
        </p>
      </Card>
    </div>
  );
}
