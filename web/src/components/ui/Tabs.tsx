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
                "relative whitespace-nowrap px-4 py-2.5 text-sm font-medium transition-colors outline-none",
                "disabled:cursor-not-allowed disabled:opacity-50",
                active
                  ? "text-[#007AFF] dark:text-[#64B5FF]"
                  : "text-gray-500 hover:text-gray-800 dark:text-gray-400 dark:hover:text-gray-200",
              )}
            >
              {tab.label}
              <span
                className={cx(
                  "absolute inset-x-3 bottom-0 h-0.5 rounded-full bg-[#0ea5e9] transition-all duration-200 dark:bg-[#7dd3fc]",
                  active ? "opacity-100" : "opacity-0",
                )}
              />
            </button>
          );
        })}
      </div>
    </div>
  );
}
