import { cx } from "../../lib/utils";

export interface SwitchProps {
  checked: boolean;
  onChange?: (checked: boolean) => void;
  disabled?: boolean;
  loading?: boolean;
  size?: "default" | "small";
  ariaLabel?: string;
}

export function Switch({ checked, onChange, disabled, loading, size = "default", ariaLabel }: SwitchProps) {
  const small = size === "small";
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={ariaLabel}
      disabled={disabled || loading}
      onClick={() => onChange?.(!checked)}
      className={cx(
        "relative inline-flex shrink-0 items-center rounded-full outline-none",
        "transition-colors duration-[340ms] ease-[cubic-bezier(0.32,0.72,0,1)]",
        "focus-visible:ring-2 focus-visible:ring-[var(--color-primary)]/40 focus-visible:ring-offset-2 dark:focus-visible:ring-offset-[#1A1610]",
        "disabled:cursor-not-allowed disabled:opacity-50 active:scale-[0.97]",
        small ? "h-[22px] w-[38px]" : "h-[31px] w-[51px]",
        checked ? "bg-[var(--color-success)]" : "bg-[#E8D9C8] dark:bg-[#3A342E]",
      )}
    >
      <span
        className={cx(
          "inline-block rounded-full bg-white shadow-[0_1px_3px_rgba(0,0,0,0.22)]",
          "transition-transform duration-[340ms] ease-[cubic-bezier(0.32,0.72,0,1)]",
          small ? "h-[18px] w-[18px]" : "h-[27px] w-[27px]",
          checked ? (small ? "translate-x-[18px]" : "translate-x-[22px]") : "translate-x-[2px]",
        )}
      />
    </button>
  );
}
