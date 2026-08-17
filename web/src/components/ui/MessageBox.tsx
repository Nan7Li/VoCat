import { useEffect, useState, type ReactNode } from "react";
import { WarningFilled } from "@fluentui/react-icons";
import { cx } from "../../lib/utils";
import { Button } from "./Button";
import { tl, useI18n } from "../../lib/i18n";

export interface ConfirmOptions {
  title?: string;
  message: ReactNode;
  confirmText?: string;
  cancelText?: string;
  type?: "warning" | "danger" | "info";
  confirmVariant?: "primary" | "danger";
}

interface ConfirmState extends ConfirmOptions {
  resolve: (value: boolean) => void;
}

let openConfirm: ((state: ConfirmState) => void) | null = null;

// Promise-based ElMessageBox.confirm equivalent.
export function confirmDialog(message: ReactNode, title = tl("提示"), options: Omit<ConfirmOptions, "message" | "title"> = {}): Promise<boolean> {
  return new Promise((resolve) => {
    openConfirm?.({ message, title, resolve, ...options });
  });
}

export function ConfirmHost() {
  const { t } = useI18n();
  const [state, setState] = useState<ConfirmState | null>(null);

  useEffect(() => {
    openConfirm = (next) => setState(next);
    return () => {
      openConfirm = null;
    };
  }, []);

  function close(result: boolean) {
    state?.resolve(result);
    setState(null);
  }

  if (!state) return null;
  const danger = state.type === "danger" || state.confirmVariant === "danger";

  return (
    <div className="halo-modal-root" style={{ zIndex: 4500 }}>
      <button type="button" className="halo-modal-backdrop" aria-label={t("取消")} onClick={() => close(false)} />
      <div
        role="alertdialog"
        aria-modal="true"
        className="halo-modal-panel glass-modal ui-pop w-full max-w-sm px-6 py-5"
      >
        <div className="flex items-start gap-3">
          <div
            className={cx(
              "mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center",
              danger ? "text-red-500" : "text-amber-500",
            )}
          >
            <WarningFilled className="text-[22px]" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="text-[17px] font-semibold tracking-tight text-[#2C2C2C] dark:text-white">{state.title}</div>
            <div className="mt-2 text-[14px] leading-relaxed text-[#6B5C4F] dark:text-white/65">{state.message}</div>
          </div>
        </div>
        <div className="mt-5 flex items-center justify-end gap-2.5">
          <Button onClick={() => close(false)}>{state.cancelText ?? t("取消")}</Button>
          <Button variant={danger ? "danger" : "primary"} onClick={() => close(true)} autoFocus>
            {state.confirmText ?? t("确定")}
          </Button>
        </div>
      </div>
    </div>
  );
}
