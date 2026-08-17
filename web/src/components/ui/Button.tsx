import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";
import { cx } from "../../lib/utils";
import { Spinner } from "./Spinner";

export type ButtonVariant = "default" | "primary" | "danger" | "success" | "warning" | "text";
export type ButtonSize = "small" | "default" | "large";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  loading?: boolean;
  icon?: ReactNode;
  block?: boolean;
  plain?: boolean;
}

const SIZE: Record<ButtonSize, string> = {
  small: "h-7 px-3 text-xs",
  default: "h-9 px-4 text-[13px]",
  large: "h-11 px-5 text-[15px]",
};

const SOLID: Record<string, string> = {
  primary:
    "border-transparent bg-[var(--color-primary)] text-[var(--color-on-primary)] hover:bg-[var(--color-primary-hover)] active:bg-[var(--color-primary-active)] ui-action-btn-primary",
  danger:
    "border-transparent bg-[#FF3B30] text-white hover:bg-[#E0342B] active:bg-[#C42D25] ui-action-btn disabled:hover:bg-[#FF3B30]",
  success:
    "border-transparent bg-[#34C759] text-white hover:bg-[#2DB14F] active:bg-[#279A45] ui-action-btn disabled:hover:bg-[#34C759]",
  warning:
    "border-transparent bg-[#FF9500] text-white hover:bg-[#E08600] active:bg-[#C27400] ui-action-btn disabled:hover:bg-[#FF9500]",
};

// Element Plus "plain" style: light-tint background with colored text + border,
// going solid on hover. Primary stays on the light-blue theme.
const PLAIN: Record<string, string> = {
  primary:
    "border-[var(--color-primary)]/30 bg-[var(--color-primary-soft)] text-[var(--color-primary)] hover:border-[var(--color-primary)] hover:bg-[var(--color-primary)] hover:text-[var(--color-on-primary)] ui-action-btn",
  danger:
    "border-[#f5b3b3] bg-[#fef0f0] text-[#f56c6c] hover:border-[#f56c6c] hover:bg-[#f56c6c] hover:text-white active:bg-[#dd6161] ui-action-btn disabled:hover:border-[#f5b3b3] disabled:hover:bg-[#fef0f0] disabled:hover:text-[#f56c6c] dark:border-[#f56c6c]/40 dark:bg-[#f56c6c]/10 dark:text-[#f78989] dark:hover:border-[#f56c6c] dark:hover:bg-[#f56c6c] dark:hover:text-white",
  success:
    "border-[#a9d68b] bg-[#f0f9eb] text-[#67c23a] hover:border-[#67c23a] hover:bg-[#67c23a] hover:text-white active:bg-[#5daf34] ui-action-btn disabled:hover:border-[#a9d68b] disabled:hover:bg-[#f0f9eb] disabled:hover:text-[#67c23a] dark:border-[#67c23a]/40 dark:bg-[#67c23a]/10 dark:text-[#85ce61] dark:hover:border-[#67c23a] dark:hover:bg-[#67c23a] dark:hover:text-white",
  warning:
    "border-[#f0c687] bg-[#fdf6ec] text-[#e6a23c] hover:border-[#e6a23c] hover:bg-[#e6a23c] hover:text-white active:bg-[#cf9236] ui-action-btn disabled:hover:border-[#f0c687] disabled:hover:bg-[#fdf6ec] disabled:hover:text-[#e6a23c] dark:border-[#e6a23c]/40 dark:bg-[#e6a23c]/10 dark:text-[#ebb563] dark:hover:border-[#e6a23c] dark:hover:bg-[#e6a23c] dark:hover:text-white",
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  {
    variant = "default",
    size = "default",
    loading = false,
    icon,
    block = false,
    plain = false,
    className,
    children,
    disabled,
    type = "button",
    ...rest
  },
  ref,
) {
  const isText = variant === "text";
  const isDefault = variant === "default";
  const isDisabled = disabled || loading;

  const variantClass = isText
    ? "border-transparent bg-transparent text-[var(--color-primary)] hover:bg-black/5 dark:hover:bg-white/10 shadow-none"
    : isDefault
      ? "border-[#E8D9C8] bg-white text-[#2C2C2C] hover:bg-[#FDF6F0] ui-action-btn dark:border-white/12 dark:bg-white/8 dark:text-[#F3EADF] dark:hover:bg-white/12"
      : plain
        ? PLAIN[variant]
        : SOLID[variant];

  return (
    <button
      ref={ref}
      type={type}
      disabled={isDisabled}
      className={cx(
        "inline-flex select-none items-center justify-center gap-1.5 whitespace-nowrap rounded-full border font-semibold tracking-tight outline-none",
        "transition-[transform,background-color,border-color,box-shadow,color] duration-[180ms] ease-[cubic-bezier(0.32,0.72,0,1)]",
        "focus-visible:ring-2 focus-visible:ring-[var(--color-primary)]/35 focus-visible:ring-offset-1 dark:focus-visible:ring-offset-[#1A1610]",
        "active:scale-[0.97] disabled:cursor-not-allowed disabled:opacity-60",
        SIZE[size],
        isText && "px-2 shadow-none",
        block && "w-full",
        variantClass,
        className,
      )}
      {...rest}
    >
      {loading ? <Spinner className="h-[1em] w-[1em]" /> : icon ? <span className="inline-flex shrink-0 items-center text-[1.1em]">{icon}</span> : null}
      {children ? <span className="inline-flex items-center gap-1.5">{children}</span> : null}
    </button>
  );
});
