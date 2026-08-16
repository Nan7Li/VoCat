import { cx } from "../../lib/utils";
import { tl } from "../../lib/i18n";
import { Spinner } from "./Spinner";

// ListSkeleton: loading placeholder — a centered spinner inside a surface card.
// (Replaces the old pulsing gray rows, which read as a distracting breathing shadow.)
export function ListSkeleton({ className }: { rows?: number; className?: string }) {
  return (
    <div className={cx("ui-surface rounded-2xl p-6", className)}>
      <div className="flex items-center justify-center gap-3 py-12 text-gray-400 dark:text-gray-500">
        <Spinner className="h-5 w-5 text-[var(--color-primary)]" />
        <span className="text-sm">{tl("加载中...")}</span>
      </div>
    </div>
  );
}
