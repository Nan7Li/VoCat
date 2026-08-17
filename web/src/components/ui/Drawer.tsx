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
      <div className="ui-overlay absolute inset-0 bg-[#2C2C2C]/20" onClick={onClose} />
      <div
        className={cx(
          "absolute inset-y-2 left-2 overflow-hidden rounded-[20px] shadow-[0_4px_20px_rgba(180,140,100,0.16)] animate-[drawer-in_0.34s_cubic-bezier(0.32,0.72,0,1)]",
          widthClass,
          className,
        )}
      >
        {children}
      </div>
    </div>
  );
}
