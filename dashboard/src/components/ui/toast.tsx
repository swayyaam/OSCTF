import { createContext, useCallback, useContext, useState, type ReactNode } from "react";
import { cn } from "../../lib/cn";

interface Toast {
  id: number;
  title: string;
  detail?: string;
  variant: "info" | "success" | "danger";
}

interface ToastContext {
  toast: (t: Omit<Toast, "id">) => void;
}

const Ctx = createContext<ToastContext | null>(null);

let nextId = 1;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);

  const toast = useCallback((t: Omit<Toast, "id">) => {
    const id = nextId++;
    setToasts((prev) => [...prev, { ...t, id }]);
    setTimeout(() => {
      setToasts((prev) => prev.filter((x) => x.id !== id));
    }, 5000);
  }, []);

  return (
    <Ctx.Provider value={{ toast }}>
      {children}
      <div
        className="fixed bottom-4 right-4 z-[100] flex w-80 max-w-[92vw] flex-col gap-2"
        aria-live="polite"
      >
        {toasts.map((t) => (
          <div
            key={t.id}
            className={cn(
              "rounded-md border bg-surface p-3 shadow-lg",
              t.variant === "danger" && "border-danger",
              t.variant === "success" && "border-success",
              t.variant === "info" && "border-border",
            )}
          >
            <p className="text-sm font-medium text-text">{t.title}</p>
            {t.detail && <p className="mt-0.5 text-xs text-text-muted">{t.detail}</p>}
          </div>
        ))}
      </div>
    </Ctx.Provider>
  );
}

// eslint-disable-next-line react-refresh/only-export-components
export function useToast(): ToastContext {
  const ctx = useContext(Ctx);
  if (!ctx) throw new Error("useToast must be used within ToastProvider");
  return ctx;
}
