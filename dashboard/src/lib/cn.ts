import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

/** cn merges class names, resolving Tailwind conflicts. */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
