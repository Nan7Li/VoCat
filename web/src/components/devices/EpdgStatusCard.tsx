import { useState } from "react";
import { Wifi1Regular, WarningRegular, CheckmarkCircleRegular, DismissCircleRegular, ArrowSyncRegular } from "@fluentui/react-icons";
import type { EPDGProbeStatus } from "../../types";
import type { DeviceDetail } from "./types";
import { useI18n } from "../../lib/i18n";
import { cx } from "../../lib/utils";
import { api, apiMessage } from "../../api";
import { Button, message } from "../ui";

export function EpdgStatusCard({ device, onRefreshed }: { device: DeviceDetail; onRefreshed?: () => void }) {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const [localProbe, setLocalProbe] = useState<EPDGProbeStatus | null>(null);
  const probe = localProbe ?? device.epdgProbe;

  const runProbe = async () => {
    if (!device.id || busy) return;
    setBusy(true);
    try {
      const result = await api<EPDGProbeStatus>(`/devices/${encodeURIComponent(device.id)}/epdg/probe`, {
        method: "POST",
        signal: AbortSignal.timeout(20000),
      });
      setLocalProbe(result);
      if (result.port500Ok && result.port4500Ok) message.success(t("ePDG 探测通过"));
      else message.warning(result.error || t("ePDG 探测未通过"));
      onRefreshed?.();
    } catch (error) {
      message.error(apiMessage(error) || t("ePDG 探测失败"));
    } finally {
      setBusy(false);
    }
  };

  const ok = !!probe && probe.port500Ok && probe.port4500Ok;
  const checked = probe?.checkedAt ? new Date(probe.checkedAt).toLocaleString() : "--";

  const portRow = (port: number, okFlag?: boolean, rtt?: number) => (
    <div className="flex items-center justify-between rounded-lg border border-gray-200 px-3 py-1.5 text-xs dark:border-[var(--color-border)]">
      <span className="font-mono text-gray-500">UDP/{port}</span>
      <span className={cx("flex items-center gap-1 font-semibold", okFlag ? "text-[var(--color-success)]" : "text-[var(--color-danger)]")}>
        {okFlag ? <CheckmarkCircleRegular className="h-3.5 w-3.5" /> : <DismissCircleRegular className="h-3.5 w-3.5" />}
        {okFlag ? t("可达") : t("不可达")}
        {rtt != null && rtt > 0 ? <span className="font-normal text-gray-400">· {rtt}ms</span> : null}
      </span>
    </div>
  );

  return (
    <div className="ui-panel-muted p-4">
      <div className="mb-2 flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5 text-xs font-bold uppercase tracking-wider text-gray-500 dark:text-[var(--color-text-muted)]">
          <Wifi1Regular className="h-3.5 w-3.5" />
          {t("ePDG 健康检查")}
        </div>
        <div className="flex items-center gap-2">
          {probe ? (
            <span className={cx("rounded-full px-2 py-0.5 text-[10px] font-bold", ok ? "bg-[var(--color-success)]/12 text-[var(--color-success)]" : "bg-[var(--color-danger)]/12 text-[var(--color-danger)]")}>
              {ok ? t("通过") : t("失败")}
            </span>
          ) : null}
          <Button size="small" disabled={busy} onClick={() => void runProbe()} icon={<ArrowSyncRegular />}>
            {busy ? t("检测中") : t("立即检测")}
          </Button>
        </div>
      </div>

      {!probe ? (
        <div className="text-xs text-gray-400 dark:text-[var(--color-text-muted)]">
          {t("尚无探测记录。点「立即检测」会按当前 SIM 推导 ePDG，走已绑定的 SOCKS5 或本机默认路由发送 IKE_SA_INIT，探测 UDP/500 与 UDP/4500。")}
        </div>
      ) : (
        <>
          <div className="space-y-1.5">
            <div className="flex items-start justify-between gap-3 text-xs">
              <span className="shrink-0 text-gray-500 dark:text-[var(--color-text-muted)]">{t("ePDG 主机")}</span>
              <span className="min-w-0 break-all text-right font-mono text-gray-700 dark:text-[var(--color-text)]">{probe.epdg || "--"}</span>
            </div>
            <div className="flex items-start justify-between gap-3 text-xs">
              <span className="shrink-0 text-gray-500 dark:text-[var(--color-text-muted)]">{t("检查时间")}</span>
              <span className="min-w-0 break-all text-right text-gray-700 dark:text-[var(--color-text)]">{checked}</span>
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
        </>
      )}
    </div>
  );
}
