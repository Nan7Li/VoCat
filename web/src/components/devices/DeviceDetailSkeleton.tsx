import { tl } from "../../lib/i18n";
import { Spinner } from "../ui";

// Loading placeholder for the device detail panel — centered spinner.
export function DeviceDetailSkeleton() {
  return (
    <div className="ui-card flex items-center justify-center gap-3 p-16 text-gray-400 dark:text-gray-500">
      <Spinner className="h-6 w-6 text-[var(--color-primary)]" />
      <span className="text-sm">{tl("正在加载设备...")}</span>
    </div>
  );
}
