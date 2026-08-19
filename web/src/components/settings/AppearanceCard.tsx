import { ColorRegular } from "@fluentui/react-icons";
import { ACCENT_PRESETS, normalizeHex, useAccent } from "../../lib/accent";
import { withViewTransition } from "../../lib/motion";
import { useI18n } from "../../lib/i18n";
import { CardIcon, CardTitle } from "./Cards";

export function AppearanceCard() {
  const { t } = useI18n();
  const { accent, setAccent } = useAccent();

  return (
    <div className="ui-card p-8 lg:col-span-2">
      <div className="mb-6 flex items-center gap-3">
        <CardIcon>
          <ColorRegular className="text-[24px]" />
        </CardIcon>
        <CardTitle title={t("外观")} subtitle={t("主色可自定义，默认是柔和杏橙色")} />
      </div>
      <div className="flex flex-wrap items-center gap-3">
        {ACCENT_PRESETS.map((preset) => {
          const active = accent.toUpperCase() === preset.hex.toUpperCase();
          return (
            <button
              key={preset.id}
              type="button"
              onClick={() => withViewTransition(() => setAccent(preset.hex))}
              className="flex items-center gap-2 rounded-full border border-[#E8D9C8] bg-white px-3 py-1.5 text-[13px] font-medium tracking-tight transition-[transform,background-color,border-color,color] duration-[180ms] ease-[cubic-bezier(0.32,0.72,0,1)] active:scale-[0.97] dark:border-[var(--color-border)] dark:bg-[var(--color-input)] dark:text-[var(--color-text)]"
              style={active ? { borderColor: preset.hex, background: "var(--color-primary-soft)", color: "var(--color-primary)" } : undefined}
            >
              <span className="h-4 w-4 rounded-full ring-2 ring-white/70 dark:ring-black/40" style={{ background: preset.hex }} />
              {t(preset.label)}
            </button>
          );
        })}
        <label className="flex items-center gap-2 rounded-full border border-black/8 bg-black/[0.03] px-3 py-1.5 text-[13px] font-medium tracking-tight dark:border-[var(--color-border)] dark:bg-[var(--color-input)] dark:text-[var(--color-text)]">
          <input
            type="color"
            value={normalizeHex(accent) || accent}
            onChange={(event) => withViewTransition(() => setAccent(event.target.value))}
            className="h-5 w-5 cursor-pointer rounded-full border-0 bg-transparent p-0"
            aria-label={t("自定义颜色")}
          />
          {t("自定义")}
        </label>
      </div>
    </div>
  );
}
