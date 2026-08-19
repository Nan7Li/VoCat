import { cx } from "../../lib/utils";
import { Switch } from "../ui";
import { useI18n } from "../../lib/i18n";

export interface PolicySwitchCardProps {
  title: string;
  subtitle?: string;
  checked: boolean;
  disabled?: boolean;
  pending?: boolean;
  failed?: boolean;
  onToggle: (value: boolean) => void;
  tone?: "indigo" | "orange";
  compact?: boolean;
}

export function PolicySwitchCard({ title, subtitle, checked, disabled, pending, failed, onToggle, tone = "indigo", compact }: PolicySwitchCardProps) {
  const { t } = useI18n();
  const status = (
    <div className="flex items-center gap-2">
      {failed ? <span className="text-xs text-orange-500 dark:text-orange-400">{t("未生效")}</span> : null}
      <Switch checked={checked} disabled={disabled} onChange={onToggle} />
    </div>
  );

  if (compact) {
    return (
      <div className="flex items-center justify-between rounded-lg bg-white px-3 py-2 dark:bg-[var(--color-input)] dark:ring-1 dark:ring-[var(--color-border)]">
        <span className="text-sm text-gray-700 dark:text-gray-200">{title}</span>
        {status}
      </div>
    );
  }

  const activeClass =
    tone === "orange"
      ? "border border-orange-300 bg-orange-50/50 dark:bg-orange-900/20"
      : "border border-indigo-300 bg-indigo-50/50 dark:bg-indigo-900/20";
  return (
    <div className={cx("ui-panel-muted space-y-1 p-3", checked && activeClass)}>
      <div className="flex items-center justify-between">
        <div>
          <div className="text-sm font-bold text-gray-800 dark:text-gray-100">{title}</div>
          {subtitle ? <div className="text-xs text-gray-500 dark:text-gray-400">{subtitle}</div> : null}
        </div>
        {status}
      </div>
    </div>
  );
}
