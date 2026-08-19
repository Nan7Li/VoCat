import { ArrowSyncRegular, PowerRegular, ChatRegular, Cellular4GRegular, Wifi1Regular, AirplaneRegular } from "@fluentui/react-icons";
import type { ReactNode } from "react";
import { Button } from "../ui";
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
        "flex h-8 min-w-0 flex-1 items-center justify-center gap-1 rounded-[12px] border px-2 text-[11px] font-semibold transition-colors sm:h-9 sm:flex-none sm:gap-1.5 sm:px-3 sm:text-[12px]",
        active
          ? cx("border-transparent", activeClass)
          : "border-[#E8D9C8] bg-white/60 text-gray-600 hover:bg-white dark:border-[var(--color-border)] dark:bg-[var(--color-card-2)] dark:text-[var(--color-btn-secondary-text)] dark:hover:border-[var(--color-border-hover)] dark:hover:bg-[#322C26]",
        locked && "cursor-not-allowed opacity-90",
        disabled && "cursor-not-allowed opacity-50",
      )}
    >
      {icon}
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
  const simLabel = device.modem?.operator || device.activeEsimProfileName || t("SIM");

  return (
    <div className="ui-card p-4 sm:p-6">
      <div className="flex flex-col gap-4">
        <div className="flex min-w-0 items-center gap-3">
          <img src={deviceTypeImage(device.deviceType)} alt="" className="h-11 w-11 flex-shrink-0 object-contain" />
          <div className="min-w-0">
            <div className="truncate text-lg font-bold text-gray-900 dark:text-[var(--color-text)] sm:text-xl">{device.name || device.id}</div>
            <div className="mt-0.5 truncate text-xs text-gray-500 dark:text-[var(--color-text-muted)]">
              <span className="cursor-pointer font-mono hover:underline" onClick={() => props.onCopyText(device.id)}>
                {device.id}
              </span>
            </div>
            <div className="mt-1 text-[11px] font-semibold text-[var(--color-primary)]">{t("当前射频模式")}：{modeLabel}</div>
          </div>
        </div>

        <div className="flex flex-col gap-3 lg:flex-row lg:items-end lg:justify-between">
          <div className="min-w-0">
            <div className="mb-1.5 text-[11px] font-semibold uppercase tracking-wider text-gray-400 dark:text-[var(--color-text-muted)]">{t("状态")}</div>
            <div className="flex flex-wrap items-center gap-1.5">
              <RadioTriadButton
                active={networkEnabled}
                loading={props.dataToggling}
                disabled={!device.interface}
                onClick={() => void props.onToggleRoamingData(!networkEnabled)}
                icon={<Cellular4GRegular className="h-4 w-4" />}
                label={t("4G")}
                title={t("蜂窝数据开关（漫游数据，仅供受保护路由使用）")}
                activeClass="bg-[var(--color-success)] text-white hover:bg-[#23b57c]"
              />
              <RadioTriadButton
                active={vowifiInUse}
                loading={props.vowifiToggling}
                disabled={props.wifiCallingOnly}
                onClick={() => void props.onToggleVoWiFi(!vowifiInUse)}
                icon={<Wifi1Regular className="h-4 w-4" />}
                label={t("VoWiFi")}
                title={t("VoWiFi 开关（独立于飞行模式）")}
                activeClass="bg-[var(--color-success)] text-white hover:bg-[#23b57c]"
              />
              <span className="inline-flex h-8 items-center rounded-full border border-[#E8D9C8] px-2.5 text-[11px] font-semibold text-gray-600 dark:border-[var(--color-border)] dark:bg-[var(--color-card-2)] dark:text-[var(--color-text-body)] sm:h-9">
                {simLabel}
              </span>
              <span className="inline-flex h-8 items-center rounded-full bg-[var(--color-primary-soft)] px-2.5 text-[11px] font-semibold text-[var(--color-primary)] sm:h-9">
                {modeLabel}
              </span>
            </div>
          </div>

          <div className="min-w-0">
            <div className="mb-1.5 text-[11px] font-semibold uppercase tracking-wider text-gray-400 dark:text-[var(--color-text-muted)]">{t("操作")}</div>
            <div className="flex flex-wrap items-center gap-2">
              <RadioTriadButton
                active={airplaneActive}
                locked={airplaneLocked}
                loading={props.airplaneToggling}
                disabled={props.wifiCallingOnly}
                onClick={() => void props.onToggleAirplane(!airplaneActive)}
                icon={<AirplaneRegular className="h-4 w-4" />}
                label={airplaneLocked ? t("飞行·锁") : t("飞行")}
                title={airplaneLocked ? t("VoWiFi 需要射频关闭，飞行模式由系统锁定；关闭 VoWiFi 后可手动切换。") : t("飞行模式开关（独立于 VoWiFi，不会联动）")}
                activeClass="bg-[var(--color-warning)] hover:bg-[#d49418] text-[#17140F]"
              />
              {vowifiInUse ? (
                <Button loading={props.reconnectingVoWiFi} onClick={props.onReconnectVowifi} className="flex-1 sm:flex-none" icon={<ArrowSyncRegular />}>
                  {t("重连 VoWiFi")}
                </Button>
              ) : null}
              {!props.wifiCallingOnly && !props.modemControlOnly ? (
                <Button loading={props.rebooting} onClick={props.onRebootModem} className="flex-1 hover:!text-[var(--color-danger)] sm:flex-none" icon={<PowerRegular />}>
                  {t("重启模组")}
                </Button>
              ) : null}
              {!props.modemControlOnly ? (
                <Button onClick={props.onOpenSms} className="flex-1 sm:flex-none" icon={<ChatRegular />}>
                  {t("短信")}
                </Button>
              ) : null}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
