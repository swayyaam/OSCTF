import { Navigate, Outlet, useLocation } from "react-router-dom";
import { useMe } from "../api/hooks";
import { Spinner } from "./ui/misc";

/** RequireAuth gates participant routes; unauthenticated users go to /login. */
export function RequireAuth() {
  const { data: me, isLoading } = useMe();
  const location = useLocation();
  if (isLoading) return <CenteredSpinner />;
  if (!me) {
    return <Navigate to={`/login?next=${encodeURIComponent(location.pathname)}`} replace />;
  }
  return <Outlet />;
}

/** RequireAdmin gates admin routes; non-admins get a 404-style page (no leak). */
export function RequireAdmin() {
  const { data: me, isLoading } = useMe();
  if (isLoading) return <CenteredSpinner />;
  if (!me || me.role !== "admin") {
    return (
      <div className="mx-auto max-w-md py-24 text-center">
        <h1 className="text-2xl font-bold text-text">404 — Not found</h1>
        <p className="mt-2 text-text-muted">This page does not exist.</p>
      </div>
    );
  }
  return <Outlet />;
}

/** RedirectIfAuthed sends signed-in users away from the login/register pages. */
export function RedirectIfAuthed() {
  const { data: me, isLoading } = useMe();
  if (isLoading) return <CenteredSpinner />;
  if (me) return <Navigate to="/challenges" replace />;
  return <Outlet />;
}

function CenteredSpinner() {
  return (
    <div className="flex min-h-[60vh] items-center justify-center">
      <Spinner />
    </div>
  );
}
