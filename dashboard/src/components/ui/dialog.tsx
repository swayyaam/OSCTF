import * as DialogPrimitive from "@radix-ui/react-dialog";
import type { ReactNode } from "react";
import { cn } from "../../lib/cn";

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title?: ReactNode;
  children: ReactNode;
  className?: string;
}

export function Dialog({ open, onOpenChange, title, children, className }: Props) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-40 bg-black/60 data-[state=open]:animate-in" />
        <DialogPrimitive.Content
          className={cn(
            "fixed left-1/2 top-1/2 z-50 max-h-[85vh] w-[92vw] max-w-2xl -translate-x-1/2 -translate-y-1/2 overflow-y-auto rounded-lg border border-border bg-surface p-6 shadow-xl focus:outline-none",
            className,
          )}
        >
          {title && (
            <DialogPrimitive.Title className="mb-4 text-lg font-semibold text-text">
              {title}
            </DialogPrimitive.Title>
          )}
          <DialogPrimitive.Description className="sr-only">Dialog</DialogPrimitive.Description>
          {children}
          <DialogPrimitive.Close
            className="absolute right-4 top-4 text-text-muted hover:text-text"
            aria-label="Close"
          >
            ✕
          </DialogPrimitive.Close>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
