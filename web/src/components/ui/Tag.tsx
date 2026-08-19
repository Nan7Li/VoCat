import type { ReactNode } from "react";
import { cx } from "../../lib/utils";

export type TagType = "success" | "danger" | "warning" | "info" | "primary";

const TONE: Record<TagType, string> = {
  success: "bg-green-50 text-green-600 border-green-200 dark:bg-[var(--color-success)]/12 dark:text-[var(--color-success)] dark:border-[var(--color-success)]/25",
  danger: "bg-red-50 text-red-600 border-red-200 dark:bg-[var(--color-danger)]/12 dark:text-[var(--color-danger)] dark:border-[var(--color-danger)]/25",
  warning: "bg-amber-50 text-amber-600 border-amber-200 dark:bg-[var(--color-warning)]/12 dark:text-[var(--color-warning)] dark:border-[var(--color-warning)]/25",
  info: "bg-gray-100 text-gray-600 border-gray-200 dark:bg-[var(--color-card-2)] dark:text-[var(--color-text-body)] dark:border-[var(--color-border)]",
  primary: "bg-indigo-50 text-indigo-600 border-indigo-200 dark:bg-[var(--color-primary-soft)] dark:text-[var(--color-primary)] dark:border-[var(--color-primary-border)]",
};

// el-tag equivalent.
export function Tag({ type = "info", children, className }: { type?: TagType; children: ReactNode; className?: string }) {
  return (
    <span
      className={cx(
        "inline-flex items-center gap-1 whitespace-nowrap rounded-full border px-2 py-0.5 text-xs font-medium",
        TONE[type],
        className,
      )}
    >
      {children}
    </span>
  );
}
