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
        "relative inline-flex shrink-0 items-center rounded-full outline-none transition-colors duration-200",
        "focus-visible:ring-2 focus-visible:ring-[var(--color-primary)]/40 focus-visible:ring-offset-2 dark:focus-visible:ring-offset-black",
        "disabled:cursor-not-allowed disabled:opacity-50",
        small ? "h-[22px] w-[38px]" : "h-[31px] w-[51px]",
        checked ? "bg-[#34C759] dark:bg-[#30D158]" : "bg-[#E9E9EA] dark:bg-[#39393D]",
      )}
    >
      <span
        className={cx(
          "inline-block rounded-full bg-white shadow-[0_1px_3px_rgba(0,0,0,0.28)] transition-transform duration-200",
          small ? "h-[18px] w-[18px]" : "h-[27px] w-[27px]",
          checked ? (small ? "translate-x-[18px]" : "translate-x-[22px]") : "translate-x-[2px]",
        )}
      />
    </button>
  );
}
