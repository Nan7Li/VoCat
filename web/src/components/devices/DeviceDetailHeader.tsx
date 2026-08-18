import { ArrowSyncRegular, PowerRegular, ChatRegular, Cellular4GRegular, Wifi1Regular, AirplaneRegular } from "@fluentui/react-icons";
import type { ReactNode } from "react";
import { Button, Spinner } from "../ui";
import type { DeviceDetail } from "./types";
import { useI18n } from "../../lib/i18n";
import { deviceTypeImage } from "../../lib/deviceTypes";
import { isVoWiFiInUse, radioMode } from "./shared";
import { cx } from "../../lib/utils";

export interface DeviceDetailHeaderProps {
  device: DeviceDetail;
  dataToggling: boolean;
  rebooting: boolean;
  reconnectingVoWiFi: boolean;
  onCopyText: (text: string) => void;
  onToggleRoamingData: (enabled: boolean) => void;
  onReconnectVowifi: () => void;
  onRebootModem: () => void;
  onOpenSms: () => void;
  onToggleAirplane: (enabled: boolean) => void;
  onToggleVoWiFi: (enabled: boolean) => void;
  airplaneToggling: boolean;
  vowifiToggling: boolean;
  wifiCallingOnly?: boolean;
  modemControlOnly?: boolean;
}

// RadioTriadButton is one of the three independent RF toggles (4G / airplane
// / VoWiFi). Each button flips exactly one backend switch; enabling VoWiFi
// keeps airplane locked on (the IMS stack needs RF off) but never toggles it
// in the other direction, so the two never get re-bound in the UI.
function RadioTriadButton({
  active,
  locked,
  loading,
  disabled,
  onClick,
  icon,
  label,
  title,
  activeClass,
}: {
  active: boolean;
  locked?: boolean;
  loading?: boolean;
  disabled?: boolean;
  onClick: () => void;
  icon: ReactNode;
  label: string;
  title?: string;
  activeClass: string;
}) {
  return (
    <button
      type="button"
      disabled={disabled || loading}
      onClick={onClick}
      title={title}
      className={cx(
        "flex h-9 items-center gap-1.5 rounded-lg border px-3 text-[12px] font-semibold transition-colors",
        active
          ? cx("border-transparent text-white", activeClass)
          : "border-gray-300 bg-white/60 text-gray-600 hover:bg-white dark:border-white/15 dark:bg-white/5 dark:text-gray-300 dark:hover:bg-white/10",
        locked && "cursor-not-allowed opacity-90",
        disabled && "cursor-not-allowed opacity-50",
      )}
    >
      {loading ? <Spinner className="h-3.5 w-3.5 animate-spin" /> : icon}
      <span>{label}</span>
    </button>
  );
}

export function DeviceDetailHeader(props: DeviceDetailHeaderProps) {
  const { t } = useI18n();
  const { device } = props;
  const vowifiInUse = isVoWiFiInUse(device);
  const mode = radioMode(device);
  const modeLabel = mode === "vowifi" ? t("VoWiFi") : mode === "cellular" ? t("4G 数据") : mode === "airplane" ? t("飞行模式") : mode === "transition" ? t("切换中") : t("离线");

  const networkEnabled = !!device.networkEnabled;
  // While VoWiFi is enabled the IMS stack keeps the radio in airplane mode;
  // the backend refuses a flight toggle with 409. Show that lock explicitly
  // instead of re-binding the two switches.
  const airplaneLocked = vowifiInUse;
  const airplaneActive = airplaneLocked || !!device.flightMode;

  return (
    <div className="ui-card p-6">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
        <div className="min-w-0">
          <div className="flex items-center gap-3">
            <img src={deviceTypeImage(device.deviceType)} alt="" className="h-11 w-11 flex-shrink-0 object-contain" />
            <div className="min-w-0">
              <div className="truncate text-xl font-extrabold text-gray-900 dark:text-white">{device.name || device.id}</div>
              <div className="mt-0.5 truncate text-xs text-gray-500 dark:text-gray-400">
                <span className="cursor-pointer font-mono hover:underline" onClick={() => props.onCopyText(device.id)}>
                  {device.id}
                </span>
              </div>
              <div className="mt-1 text-[11px] font-semibold text-[var(--color-primary)]">{t("当前射频模式")}：{modeLabel}</div>
            </div>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {/* 射频三键：三个独立开关，各自只切自己的位。 */}
          <div className="ui-glass-border flex items-center gap-2 rounded-xl px-2.5 py-2">
            <RadioTriadButton
              active={networkEnabled}
              loading={props.dataToggling}
              disabled={!device.interface}
              onClick={() => void props.onToggleRoamingData(!networkEnabled)}
              icon={<Cellular4GRegular className="h-4 w-4" />}
              label={t("4G")}
              title={t("蜂窝数据开关（漫游数据，仅供受保护路由使用）")}
              activeClass="bg-[#0A84FF] hover:bg-[#0A75E6]"
            />
            <RadioTriadButton
              active={airplaneActive}
              locked={airplaneLocked}
              loading={props.airplaneToggling}
              disabled={props.wifiCallingOnly}
              onClick={() => void props.onToggleAirplane(!airplaneActive)}
              icon={<AirplaneRegular className="h-4 w-4" />}
              label={airplaneLocked ? t("飞行模式·锁定") : t("飞行模式")}
              title={airplaneLocked ? t("VoWiFi 需要射频关闭，飞行模式由系统锁定；关闭 VoWiFi 后可手动切换。") : t("飞行模式开关（独立于 VoWiFi，不会联动）")}
              activeClass="bg-[#FF9500] hover:bg-[#E08600]"
            />
            <RadioTriadButton
              active={vowifiInUse}
              loading={props.vowifiToggling}
              disabled={props.wifiCallingOnly}
              onClick={() => void props.onToggleVoWiFi(!vowifiInUse)}
              icon={<Wifi1Regular className="h-4 w-4" />}
              label={t("VoWiFi")}
              title={t("VoWiFi 开关（独立于飞行模式）")}
              activeClass="bg-[#34C759] hover:bg-[#2DB14F]"
            />
          </div>
          {vowifiInUse ? (
            <Button loading={props.reconnectingVoWiFi} onClick={props.onReconnectVowifi} className="ui-glass-border !border-0" icon={<ArrowSyncRegular />}>
              {t("重连 VoWiFi")}
            </Button>
          ) : null}
          {!props.wifiCallingOnly && !props.modemControlOnly ? <Button loading={props.rebooting} onClick={props.onRebootModem} className="ui-glass-border !border-0 hover:!text-red-600" icon={<PowerRegular />}>
            {t("重启模组")}
          </Button> : null}
          {!props.modemControlOnly ? <Button onClick={props.onOpenSms} className="ui-glass-border !border-0" icon={<ChatRegular />}>
            {t("短信")}
          </Button> : null}
        </div>
      </div>
    </div>
  );
}
