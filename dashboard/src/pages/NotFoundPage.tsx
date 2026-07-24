import { Link } from "react-router-dom";
import { Button } from "../components/ui/button";

export function NotFoundPage() {
  return (
    <div className="mx-auto max-w-md py-24 text-center">
      <h1 className="text-4xl font-bold text-text">404</h1>
      <p className="mt-2 text-text-muted">This page does not exist.</p>
      <Link to="/" className="mt-6 inline-block">
        <Button>Go home</Button>
      </Link>
    </div>
  );
}
