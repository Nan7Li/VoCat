import { ArrowSyncRegular, AlertRegular, EyeRegular, EyeOffRegular } from "@fluentui/react-icons";
import { cx } from "../../lib/utils";
import { Button, Tooltip } from "../ui";
import type { EsimChipInfo } from "./types";
import { useI18n } from "../../lib/i18n";

export interface EsimChipHeaderProps {
  chipInfo: EsimChipInfo;
  showSensitive: boolean;
  refreshing: boolean;
  notificationsLoading: boolean;
  onRefresh: () => void;
  onOpenNotifications: () => void;
  onToggleSensitive: () => void;
}

export function EsimChipHeader(props: EsimChipHeaderProps) {
  const { t } = useI18n();
  const { chipInfo } = props;
  return (
    <div className="ui-panel-muted relative p-3 sm:p-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex min-w-0 items-start gap-3 sm:items-center">
          <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-indigo-50 text-indigo-600 dark:bg-indigo-500/10 dark:text-indigo-400">
            <svg viewBox="0 0 1024 1024" className="h-[22px] w-[22px]" fill="currentColor" aria-hidden="true">
              <path d="M381.397333 107.392v112.256h93.866667V107.392h61.226667v112.256h93.866666V107.392h61.226667v112.256h114.218667v106.112h110.933333V386.986667h-110.933333v93.866666h110.933333v61.226667h-110.933333v93.866667h110.933333v61.226666h-110.933333v107.136h-114.304v112.341334h-61.184v-112.341334h-93.866667v112.341334h-61.184v-112.341334h-93.866667v112.341334H320.213333v-112.341334H221.098667V697.173333H107.392v-61.184h113.664v-93.866666H107.392v-61.226667h113.664v-93.866667H107.392V325.802667h113.664V219.690667h99.114667V107.392z m363.136 173.482667H282.282667v462.250666H744.533333z m-93.866666 93.866666v274.474667H376.192V374.741333z m-61.226667 61.226667h-152.064v152.064h152.064z" />
            </svg>
          </div>
          <div className="min-w-0">
            <div className="truncate text-base font-bold text-gray-900 dark:text-white">{chipInfo.skuName || "eUICC"}</div>
            <div className="mt-0.5 break-all font-mono text-xs text-gray-500 dark:text-gray-400">
              {chipInfo.firmware ? <>{t("固件")} {chipInfo.firmware}</> : null}
              {chipInfo.serialNumber ? (
                <>
                  {chipInfo.firmware ? " · " : null}
                  {"SN: "}
                  <span className={cx("transition-all", !props.showSensitive && "select-none blur-sm")}>{chipInfo.serialNumber}</span>
                </>
              ) : null}
            </div>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1 self-end sm:self-auto">
          <Tooltip content={t("手动刷新")}>
            <Button variant="text" loading={props.refreshing} onClick={props.onRefresh} icon={<ArrowSyncRegular className="text-[18px]" />} />
          </Tooltip>
          <Tooltip content={t("当前通知")}>
            <Button variant="text" loading={props.notificationsLoading} onClick={props.onOpenNotifications} icon={<AlertRegular className="text-[18px]" />} />
          </Tooltip>
          <Tooltip content={props.showSensitive ? t("隐藏敏感信息") : t("显示敏感信息")}>
            <Button
              variant="text"
              onClick={props.onToggleSensitive}
              icon={props.showSensitive ? <EyeRegular className="text-[18px]" /> : <EyeOffRegular className="text-[18px]" />}
            />
          </Tooltip>
        </div>
      </div>
    </div>
  );
}
