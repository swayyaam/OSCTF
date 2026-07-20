// M0 placeholder shell. The real router, pages, and providers land in M8/M9
// (docs/v0.1/09-frontend.md). Kept intentionally tiny so the scaffold builds green.
export function App() {
  return (
    <main className="mx-auto flex min-h-full max-w-3xl flex-col items-center justify-center gap-4 p-8 text-center">
      <h1 className="text-3xl font-bold text-text">OSCTF</h1>
      <p className="text-text-muted">
        The dashboard scaffold is up. Pages arrive with milestones M8 (participant) and
        M9 (admin).
      </p>
      <code className="rounded-md bg-surface-2 px-3 py-1 font-mono text-sm text-primary">
        make dev-api &amp;&amp; make dev-web
      </code>
    </main>
  );
}
