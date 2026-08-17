import type { ReactNode } from "react";

export function PageHeader({ title, subtitle, actions }: { title: ReactNode; subtitle?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-col gap-3 sm:mb-8 sm:flex-row sm:items-end sm:justify-between">
      <div>
        <h2 className="font-display text-[22px] font-semibold leading-none tracking-[-0.03em] text-[#2C2C2C] dark:text-[#F3EADF]">
          {title}
        </h2>
        {subtitle ? (
          <p className="mt-2 text-[13px] leading-relaxed tracking-tight text-[#8A7A6A] dark:text-white/45">{subtitle}</p>
        ) : null}
      </div>
      {actions}
    </div>
  );
}
