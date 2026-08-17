import type { ReactNode } from "react";
import { cx } from "../../lib/utils";

export interface TabItem {
  key: string;
  label: ReactNode;
  disabled?: boolean;
}

// el-tabs equivalent: top nav with sliding active bar.
export function Tabs({
  tabs,
  value,
  onChange,
  className,
}: {
  tabs: TabItem[];
  value: string;
  onChange: (key: string) => void;
  className?: string;
}) {
  return (
    <div className={cx("relative", className)}>
      <div className="relative flex items-center gap-1 overflow-x-auto border-b border-gray-200/70 dark:border-white/10">
        {tabs.map((tab) => {
          const active = tab.key === value;
          return (
            <button
              key={tab.key}
              type="button"
              disabled={tab.disabled}
              onClick={() => onChange(tab.key)}
              className={cx(
                "relative whitespace-nowrap px-4 py-2.5 text-sm font-medium outline-none",
                "transition-[color,transform] duration-[180ms] ease-[cubic-bezier(0.32,0.72,0,1)]",
                "disabled:cursor-not-allowed disabled:opacity-50 active:scale-[0.97]",
                active
                  ? "text-[var(--color-primary)]"
                  : "text-[#8A7A6A] hover:text-[#2C2C2C] dark:text-gray-400 dark:hover:text-gray-200",
              )}
            >
              {tab.label}
              <span
                className={cx(
                  "absolute inset-x-3 bottom-0 h-0.5 rounded-full bg-[var(--color-primary)]",
                  "transition-[opacity,transform] duration-[340ms] ease-[cubic-bezier(0.32,0.72,0,1)]",
                  active ? "scale-x-100 opacity-100" : "scale-x-50 opacity-0",
                )}
              />
            </button>
          );
        })}
      </div>
    </div>
  );
}
