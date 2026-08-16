import { useEffect, type ReactNode } from "react";
import { cx } from "../../lib/utils";

// el-drawer equivalent (slides in from the left) used for the mobile sidebar.
export function Drawer({
  open,
  onClose,
  children,
  widthClass = "w-64",
  className,
}: {
  open: boolean;
  onClose: () => void;
  children: ReactNode;
  widthClass?: string;
  className?: string;
}) {
  useEffect(() => {
    if (!open) return;
    function onKey(event: KeyboardEvent) {
      if (event.key === "Escape") onClose();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  if (!open) return null;

  return (
    <div className="fixed inset-0 z-[2900]">
      <div className="absolute inset-0 bg-black/25 backdrop-blur-xl" onClick={onClose} />
      <div
        className={cx(
          "absolute inset-y-2 left-2 overflow-hidden rounded-[28px] shadow-[0_20px_60px_rgba(0,0,0,0.18)] animate-[drawer-in_0.25s_cubic-bezier(0.4,0,0.2,1)]",
          widthClass,
          className,
        )}
      >
        {children}
      </div>
    </div>
  );
}
