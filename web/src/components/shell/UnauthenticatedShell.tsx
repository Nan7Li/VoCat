import { Outlet } from "react-router-dom";
import { LanguageSwitch } from "../ui/LanguageSwitch";
import { SwitchDark } from "../ui/SwitchDark";

// UnauthenticatedShell: centers the login card, theme toggle pinned top-right.
export function UnauthenticatedShell({
  isDark,
  onToggleTheme,
}: {
  isDark: boolean;
  onToggleTheme: () => void;
}) {
  return (
    <div className="vocat-app-shell relative h-full w-full items-center justify-center">
      <div className="pointer-events-auto absolute right-5 top-5 z-50 flex items-center gap-2">
        <LanguageSwitch />
        <SwitchDark isDark={isDark} onToggle={onToggleTheme} />
      </div>
      <Outlet />
    </div>
  );
}
