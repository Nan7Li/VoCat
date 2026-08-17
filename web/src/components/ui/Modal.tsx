import { useEffect, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { DismissRegular } from "@fluentui/react-icons";
import { cx } from "../../lib/utils";
import { useI18n } from "../../lib/i18n";

export interface ModalProps {
  open: boolean;
  onClose: () => void;
  title?: ReactNode;
  width?: string;
  children: ReactNode;
  footer?: ReactNode;
  showClose?: boolean;
  closeOnOverlay?: boolean;
  className?: string;
  bodyClassName?: string;
}

// Glassmorphism modal replicating VoHive's `.el-dialog.glass-modal`.
export function Modal({
  open,
  onClose,
  title,
  width = "max-w-lg",
  children,
  footer,
  showClose = true,
  closeOnOverlay = true,
  className,
  bodyClassName,
}: ModalProps) {
  const { t } = useI18n();
  useEffect(() => {
    if (!open) return;
    function onKey(event: KeyboardEvent) {
      if (event.key === "Escape") onClose();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  useEffect(() => {
    if (!open) return;
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.body.style.overflow = previous;
    };
  }, [open]);

  if (!open) return null;

  return createPortal(
    <div className="halo-modal-root" role="presentation">
      <button
        type="button"
        aria-label={t("关闭")}
        className="halo-modal-backdrop"
        onClick={() => {
          if (closeOnOverlay) onClose();
        }}
      />
      <div
        role="dialog"
        aria-modal="true"
        className={cx("halo-modal-panel glass-modal ui-pop", width, className)}
      >
        {(title || showClose) && (
          <div className="flex shrink-0 items-center justify-between px-6 pt-5 pb-3">
            <div className="min-w-0 pr-3 text-[17px] font-semibold tracking-tight text-[#2C2C2C] dark:text-white">{title}</div>
            {showClose && (
              <button
                type="button"
                onClick={onClose}
                aria-label={t("关闭")}
                className="shrink-0 rounded-full p-1.5 text-[#8A7A6A] transition-colors hover:bg-black/5 hover:text-[#2C2C2C] dark:hover:bg-white/10 dark:hover:text-white"
              >
                <DismissRegular className="text-[18px]" />
              </button>
            )}
          </div>
        )}
        <div className={cx("halo-modal-body min-h-0 flex-1 overflow-y-auto px-6 pb-5", !title && "pt-5", bodyClassName)}>{children}</div>
        {footer && <div className="halo-modal-footer flex shrink-0 flex-wrap items-center justify-end gap-3 px-6 py-4">{footer}</div>}
      </div>
    </div>,
    document.body,
  );
}
