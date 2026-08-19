import { cx } from "../../lib/utils";

export type StatusTone = "success" | "warning" | "danger" | "neutral";

const TONE: Record<StatusTone, string> = {
  success: "bg-[var(--color-success)]",
  warning: "bg-[var(--color-warning)]",
  danger: "bg-[var(--color-danger)]",
  neutral: "bg-[var(--color-text-disabled)]",
};

// StatusLight: colored pulsing status dot.
export function StatusDot({
  tone = "neutral",
  size = "sm",
  animated = true,
  className,
}: {
  tone?: StatusTone;
  size?: "sm" | "md";
  animated?: boolean;
  className?: string;
}) {
  return (
    <span
      aria-hidden="true"
      className={cx(
        "inline-block flex-shrink-0 rounded-full",
        TONE[tone],
        size === "md" ? "h-1.5 w-1.5" : "h-2 w-2",
        animated && "animate-pulse",
        className,
      )}
    />
  );
}
