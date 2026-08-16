import { WeatherMoonRegular, WeatherSunnyRegular } from "@fluentui/react-icons";
import { useI18n } from "../../lib/i18n";

// SwitchDark: circular theme toggle button (moon in light, sun in dark).
export function SwitchDark({ isDark, onToggle }: { isDark: boolean; onToggle: () => void }) {
  const { t } = useI18n();
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-label={isDark ? t("切换浅色模式") : t("切换深色模式")}
      className="vocat-glass-btn"
    >
      {isDark ? <WeatherSunnyRegular className="h-[18px] w-[18px]" /> : <WeatherMoonRegular className="h-[18px] w-[18px]" />}
    </button>
  );
}
