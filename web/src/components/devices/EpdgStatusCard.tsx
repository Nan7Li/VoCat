import { Wifi1Regular, WarningRegular, CheckmarkCircleRegular, DismissCircleRegular } from "@fluentui/react-icons";
import type { DeviceDetail } from "./types";
import { useI18n } from "../../lib/i18n";
import { cx } from "../../lib/utils";

// EpdgStatusCard surfaces the ePDG UDP/500+4500 health check on the device
// page. When a probe failure auto-disabled VoWiFi it explains exactly why,
// instead of leaving the user with a mysteriously greyed-out VoWiFi toggle.
export function EpdgStatusCard({ device }: { device: DeviceDetail }) {
  const { t } = useI18n();
  const probe = device.epdgProbe;
  if (!probe) {
    return (
      <div className="ui-panel-muted p-4">
        <div className="mb-2 text-xs font-bold uppercase tracking-wider text-gray-500">{t("ePDG 健康检查")}</div>
        <div className="text-xs text-gray-400">{t("尚无 ePDG 探测记录（启动 VoWiFi 时自动检测 UDP/500 与 UDP/4500）。")}</div>
      </div>
    );
  }

  const ok = probe.port500Ok && probe.port4500Ok;
  const checked = probe.checkedAt ? new Date(probe.checkedAt).toLocaleString() : "--";

  const portRow = (port: number, okFlag: boolean, rtt?: number) => (
    <div className="flex items-center justify-between rounded-lg border border-gray-200 px-3 py-1.5 text-xs dark:border-white/10">
      <span className="font-mono text-gray-500">UDP/{port}</span>
      <span className={cx("flex items-center gap-1 font-semibold", okFlag ? "text-emerald-600" : "text-red-500")}>
        {okFlag ? <CheckmarkCircleRegular className="h-3.5 w-3.5" /> : <DismissCircleRegular className="h-3.5 w-3.5" />}
        {okFlag ? t("可达") : t("不可达")}
        {rtt != null && rtt > 0 ? <span className="font-normal text-gray-400">· {rtt}ms</span> : null}
      </span>
    </div>
  );

  return (
    <div className="ui-panel-muted p-4">
      <div className="mb-2 flex items-center justify-between">
        <div className="flex items-center gap-1.5 text-xs font-bold uppercase tracking-wider text-gray-500">
          <Wifi1Regular className="h-3.5 w-3.5" />
          {t("ePDG 健康检查")}
        </div>
        <span className={cx("rounded-full px-2 py-0.5 text-[10px] font-bold", ok ? "bg-emerald-500/10 text-emerald-600" : "bg-red-500/10 text-red-500")}>
          {ok ? t("通过") : t("失败")}
        </span>
      </div>

      <div className="space-y-1.5">
        <div className="flex items-center justify-between text-xs">
          <span className="text-gray-500">{t("ePDG 主机")}</span>
          <span className="font-mono text-gray-700 dark:text-gray-200">{probe.epdg || "--"}</span>
        </div>
        <div className="flex items-center justify-between text-xs">
          <span className="text-gray-500">{t("检查时间")}</span>
          <span className="text-gray-700 dark:text-gray-200">{checked}</span>
        </div>
        {portRow(500, probe.port500Ok, probe.rtt500Ms)}
        {portRow(4500, probe.port4500Ok, probe.rtt4500Ms)}
      </div>

      {probe.disabledVoWiFi ? (
        <div className="mt-3 flex items-start gap-2 rounded-lg border border-red-200 bg-red-50/70 px-3 py-2 text-xs text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-300">
          <WarningRegular className="mt-0.5 h-4 w-4 flex-shrink-0" />
          <div>
            <div className="font-bold">{t("ePDG 探测失败，VoWiFi 已被自动关闭")}</div>
            <div className="mt-1 break-all">{probe.error || t("UDP/500 或 UDP/4500 未响应。请检查代理出口与 ePDG 域名解析后重新开启 VoWiFi。")}</div>
          </div>
        </div>
      ) : !ok ? (
        <div className="mt-3 flex items-start gap-2 rounded-lg border border-orange-200 bg-orange-50/70 px-3 py-2 text-xs text-orange-700 dark:border-orange-500/30 dark:bg-orange-500/10 dark:text-orange-300">
          <WarningRegular className="mt-0.5 h-4 w-4 flex-shrink-0" />
          <div>
            <div className="font-bold">{t("最近一次探测未通过")}</div>
            <div className="mt-1 break-all">{probe.error || t("ePDG 端口不可达，VoWiFi 可能无法注册。")}</div>
          </div>
        </div>
      ) : null}
    </div>
  );
}
