import type { ReactNode } from "react";

export function PageHeader({ title, subtitle, actions }: { title: ReactNode; subtitle?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-4 flex flex-col gap-3 sm:mb-6 sm:flex-row sm:items-end sm:justify-between">
      <div className="min-w-0">
        <h2 className="font-display text-[22px] font-bold leading-none tracking-[-0.03em] text-[#2C2C2C] dark:text-[var(--color-text)] sm:text-[28px]">
          {title}
        </h2>
        {subtitle ? (
          <p className="mt-2 text-[13px] leading-relaxed tracking-tight text-[#8A7A6A] dark:text-[var(--color-text-muted)]">{subtitle}</p>
        ) : null}
      </div>
      {actions ? <div className="min-w-0 shrink-0">{actions}</div> : null}
    </div>
  );
}
