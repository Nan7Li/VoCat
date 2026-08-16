import { forwardRef, type InputHTMLAttributes, type ReactNode, type TextareaHTMLAttributes } from "react";
import { cx } from "../../lib/utils";

export interface InputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "prefix"> {
  prefix?: ReactNode;
  suffix?: ReactNode;
  inputSize?: "default" | "large";
}

// el-input equivalent with optional prefix/suffix icons.
export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { prefix, suffix, inputSize = "default", className, ...rest },
  ref,
) {
  return (
    <div className={cx("relative flex w-full items-center", className)}>
      {prefix && (
        <span className="pointer-events-none absolute inset-y-0 left-0 flex items-center pl-3 text-gray-400 dark:text-gray-500">
          {prefix}
        </span>
      )}
      <input
        ref={ref}
        className={cx(
          "w-full rounded-[12px] border border-black/[0.08] bg-black/[0.03] px-3 text-[15px] tracking-tight text-black outline-none transition-all",
          "placeholder:text-black/30 hover:border-black/15 focus:border-[#007AFF] focus:bg-white focus:ring-2 focus:ring-[#007AFF]/20",
          "dark:border-white/10 dark:bg-white/8 dark:text-white dark:placeholder:text-white/30 dark:hover:border-white/20 dark:focus:border-[#0A84FF] dark:focus:bg-[#1c1c1e]",
          inputSize === "large" ? "h-11" : "h-10",
          prefix ? "pl-10" : null,
          suffix ? "pr-10" : null,
        )}
        {...rest}
      />
      {suffix && (
        <span className="absolute inset-y-0 right-0 flex items-center pr-3 text-gray-400 dark:text-gray-500">{suffix}</span>
      )}
    </div>
  );
});

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {}

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea({ className, ...rest }, ref) {
  return (
    <textarea
      ref={ref}
      className={cx(
        "w-full rounded-[12px] border border-black/[0.08] bg-black/[0.03] px-3 py-2 text-[15px] tracking-tight text-black outline-none transition-all",
        "placeholder:text-black/30 hover:border-black/15 focus:border-[#007AFF] focus:bg-white focus:ring-2 focus:ring-[#007AFF]/20",
        "dark:border-white/10 dark:bg-white/8 dark:text-white dark:placeholder:text-white/30 dark:hover:border-white/20 dark:focus:border-[#0A84FF]",
        className,
      )}
      {...rest}
    />
  );
});
