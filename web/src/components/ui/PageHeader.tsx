import type { ReactNode } from "react";

export function PageHeader({ title, subtitle, actions }: { title: ReactNode; subtitle?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-col gap-3 sm:mb-8 sm:flex-row sm:items-end sm:justify-between">
      <div>
        <h2 className="font-display text-[34px] font-bold leading-none tracking-[-0.04em] text-black dark:text-white">
          {title}
        </h2>
        {subtitle ? (
          <p className="mt-2 text-[15px] leading-snug tracking-tight text-black/40 dark:text-white/45">{subtitle}</p>
        ) : null}
      </div>
      {actions}
    </div>
  );
}
