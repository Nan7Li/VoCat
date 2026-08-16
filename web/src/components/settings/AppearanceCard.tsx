import { ColorRegular } from "@fluentui/react-icons";
import { ACCENT_PRESETS, normalizeHex, useAccent } from "../../lib/accent";
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
        <CardTitle title={t("外观")} subtitle={t("主色可自定义，默认接近 Grok 的暖金色")} />
      </div>
      <div className="flex flex-wrap items-center gap-3">
        {ACCENT_PRESETS.map((preset) => {
          const active = accent.toUpperCase() === preset.hex.toUpperCase();
          return (
            <button
              key={preset.id}
              type="button"
              onClick={() => setAccent(preset.hex)}
              className="flex items-center gap-2 rounded-full border border-black/8 bg-black/[0.03] px-3 py-1.5 text-[13px] font-medium tracking-tight transition-colors dark:border-white/10 dark:bg-white/8"
              style={active ? { borderColor: preset.hex, background: "var(--color-primary-soft)", color: "var(--color-primary)" } : undefined}
            >
              <span className="h-4 w-4 rounded-full ring-2 ring-white/70 dark:ring-black/40" style={{ background: preset.hex }} />
              {t(preset.label)}
            </button>
          );
        })}
        <label className="flex items-center gap-2 rounded-full border border-black/8 bg-black/[0.03] px-3 py-1.5 text-[13px] font-medium tracking-tight dark:border-white/10 dark:bg-white/8">
          <input
            type="color"
            value={normalizeHex(accent) || accent}
            onChange={(event) => setAccent(event.target.value)}
            className="h-5 w-5 cursor-pointer rounded-full border-0 bg-transparent p-0"
            aria-label={t("自定义颜色")}
          />
          {t("自定义")}
        </label>
      </div>
    </div>
  );
}
