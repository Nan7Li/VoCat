import { useEffect, useState, type ReactNode, type TransitionEvent } from "react";
import { cx } from "../../lib/utils";

// Left drawer for the mobile sidebar. Stays mounted through the close
// animation so opening and closing both ease instead of popping.
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
  const [mounted, setMounted] = useState(open);
  const [shown, setShown] = useState(false);

  useEffect(() => {
    if (open) {
      setMounted(true);
      const id = requestAnimationFrame(() => {
        requestAnimationFrame(() => setShown(true));
      });
      return () => cancelAnimationFrame(id);
    }
    setShown(false);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    function onKey(event: KeyboardEvent) {
      if (event.key === "Escape") onClose();
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  if (!mounted) return null;

  function onPanelTransitionEnd(event: TransitionEvent<HTMLDivElement>) {
    if (event.propertyName !== "transform") return;
    if (!open) setMounted(false);
  }

  return (
    <div className={cx("halo-drawer-root", shown && "is-open")} style={shown ? undefined : { pointerEvents: "none" }}>
      <div className="halo-drawer-overlay" onClick={onClose} />
      <div
        className={cx("halo-drawer-panel", widthClass, className)}
        onTransitionEnd={onPanelTransitionEnd}
      >
        {children}
      </div>
    </div>
  );
}
