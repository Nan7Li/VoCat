import { forwardRef, type InputHTMLAttributes, type ReactNode, type TextareaHTMLAttributes } from "react";
import { cx } from "../../lib/utils";

export interface InputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "prefix"> {
  prefix?: ReactNode;
  suffix?: ReactNode;
  inputSize?: "default" | "large";
}

const FIELD =
  "w-full rounded-[12px] border border-[#E8D9C8] bg-[#FDF8F2] px-3 text-[15px] tracking-tight text-[#2C2C2C] outline-none " +
  "transition-[border-color,box-shadow,background-color] duration-180 " +
  "placeholder:text-[#A08B7A] hover:border-[#D9C6B0] focus:border-[var(--color-primary)] focus:bg-white focus:ring-2 focus:ring-[var(--color-primary)]/20 " +
  "dark:border-[var(--color-border)] dark:bg-[var(--color-input)] dark:text-[var(--color-text)] dark:placeholder:text-[var(--color-text-weak)] " +
  "dark:hover:border-[var(--color-border-hover)] dark:focus:border-[var(--color-primary)] dark:focus:bg-[var(--color-input)] " +
  "dark:disabled:bg-[var(--color-btn-disabled-bg)] dark:disabled:text-[var(--color-text-disabled)]";

// el-input equivalent with optional prefix/suffix icons.
export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { prefix, suffix, inputSize = "default", className, ...rest },
  ref,
) {
  return (
    <div className={cx("relative flex w-full items-center", className)}>
      {prefix && (
        <span className="pointer-events-none absolute inset-y-0 left-0 flex items-center pl-3 text-[#A08B7A] dark:text-[var(--color-text-weak)]">
          {prefix}
        </span>
      )}
      <input
        ref={ref}
        className={cx(
          FIELD,
          inputSize === "large" ? "h-11" : "h-10",
          prefix ? "pl-10" : null,
          suffix ? "pr-10" : null,
        )}
        {...rest}
      />
      {suffix && (
        <span className="absolute inset-y-0 right-0 flex items-center pr-3 text-[#A08B7A] dark:text-[var(--color-text-weak)]">{suffix}</span>
      )}
    </div>
  );
});

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {}

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea({ className, ...rest }, ref) {
  return (
    <textarea
      ref={ref}
      className={cx(FIELD, "py-2", className)}
      {...rest}
    />
  );
});
