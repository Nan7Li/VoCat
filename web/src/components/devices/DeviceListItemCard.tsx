import type { DeviceListItem } from "../../types";
import { cx } from "../../lib/utils";
import { Tag, StatusDot } from "../ui";
import { deviceStatusMeta } from "./shared";
import { deviceTypeImage } from "../../lib/deviceTypes";

export interface DeviceListItemCardProps {
  device: DeviceListItem;
  selected: boolean;
  statusText: string;
  onSelect: (id: string) => void;
}

export function DeviceListItemCard({ device, selected, statusText, onSelect }: DeviceListItemCardProps) {
  const meta = deviceStatusMeta(device);
  return (
    <div className="device-list-item">
      <button
        type="button"
        onClick={() => onSelect(device.id)}
        className={cx(
          "h-full w-full rounded-[16px] border p-3 text-left",
          "transition-[background-color,border-color,box-shadow,transform] duration-[340ms] ease-[cubic-bezier(0.32,0.72,0,1)]",
          "active:scale-[0.985]",
          selected
            ? "border-[var(--color-primary)]/25 bg-[var(--color-primary-soft)] shadow-[0_4px_20px_rgba(180,140,100,0.08)]"
            : "border-[#E8D9C8] hover:bg-[#FDF6F0] dark:border-white/10 dark:hover:bg-white/5",
        )}
      >
        <div className="flex items-start justify-between gap-2">
          <img src={deviceTypeImage(device.deviceType)} alt="" className="h-10 w-10 shrink-0 object-contain" />
          <div className="min-w-0 flex-1">
            <div className="truncate font-bold text-gray-800 dark:text-gray-100">{device.name || device.id}</div>
            <div className="mt-0.5 truncate text-xs text-gray-500">
              {device.id} · {device.interface || "--"}
            </div>
            <div className="mt-1 truncate text-xs text-gray-400">{statusText}</div>
          </div>
          <div className="flex items-center gap-2">
            <StatusDot tone={meta.tone} size="sm" animated={meta.animated} />
            <Tag type={meta.tag}>{meta.label}</Tag>
          </div>
        </div>
      </button>
    </div>
  );
}
