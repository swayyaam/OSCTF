import type { HTMLAttributes, LabelHTMLAttributes, ReactNode } from "react";
import { cn } from "../../lib/cn";

export function Label({ className, ...props }: LabelHTMLAttributes<HTMLLabelElement>) {
  return <label className={cn("mb-1 block text-sm font-medium text-text", className)} {...props} />;
}

export function Card({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("rounded-lg border border-border bg-surface p-4", className)}
      {...props}
    />
  );
}

const categoryColor: Record<string, string> = {
  web: "text-cat-web border-cat-web",
  pwn: "text-cat-pwn border-cat-pwn",
  crypto: "text-cat-crypto border-cat-crypto",
  rev: "text-cat-rev border-cat-rev",
  forensics: "text-cat-forensics border-cat-forensics",
  misc: "text-cat-misc border-cat-misc",
};

export function CategoryBadge({ category }: { category: string }) {
  return (
    <span
      className={cn(
        "inline-block rounded-full border px-2 py-0.5 text-xs font-medium",
        categoryColor[category] ?? "border-border text-text-muted",
      )}
    >
      {category}
    </span>
  );
}

export function Badge({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <span
      className={cn(
        "inline-block rounded-full border border-border px-2 py-0.5 text-xs text-text-muted",
        className,
      )}
    >
      {children}
    </span>
  );
}

export function FieldError({ messages }: { messages?: string[] }) {
  if (!messages || messages.length === 0) return null;
  return <p className="mt-1 text-sm text-danger">{messages.join(", ")}</p>;
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={cn("animate-pulse rounded bg-surface-2", className)} />;
}

export function Spinner() {
  return (
    <div
      className="h-5 w-5 animate-spin rounded-full border-2 border-border border-t-primary"
      role="status"
      aria-label="Loading"
    />
  );
}

export function EmptyState({ children }: { children: ReactNode }) {
  return <div className="rounded-lg border border-dashed border-border p-8 text-center text-text-muted">{children}</div>;
}
