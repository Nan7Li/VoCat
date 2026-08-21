import type { ReactNode } from "react";
import {
  AlertRegular,
  CheckmarkRegular,
  InfoRegular,
  KeyRegular,
} from "@fluentui/react-icons";
import type { AutoUpdateSettings, SystemInfo, UpstreamVocatStatus } from "../../types";
import { useI18n } from "../../lib/i18n";
import { Button } from "../ui/Button";
import { Switch } from "../ui/Switch";
import { Select } from "../ui/Select";
import { FieldRow, PasswordInput } from "./controls";

export interface PasswordForm {
  oldPassword: string;
  newPassword: string;
  confirmPassword: string;
}

export interface UpdateInfo {
  hasUpdate?: boolean;
  latestVersion?: string;
  releaseNote?: string;
  isDocker?: boolean;
}

function CardDecor() {
  return null;
}

function CardIcon({ children, small }: { children: ReactNode; small?: boolean }) {
  return (
    <div
      className={
        small
          ? "flex h-9 w-9 items-center justify-center rounded-full bg-[var(--color-primary-soft)] text-[var(--color-primary)]"
          : "flex h-11 w-11 items-center justify-center rounded-full bg-[var(--color-primary-soft)] text-[var(--color-primary)]"
      }
    >
      {children}
    </div>
  );
}

function CardTitle({ title, subtitle }: { title: string; subtitle: string }) {
  return (
    <div>
      <h3 className="text-[17px] font-semibold tracking-tight text-black dark:text-white">{title}</h3>
      <p className="text-[13px] text-black/40 dark:text-white/45">{subtitle}</p>
    </div>
  );
}

const PASSWORD_LABEL = "text-xs font-bold uppercase tracking-wider text-gray-500";

export function SecurityCard({
  value,
  onChange,
  loading,
  onSubmit,
}: {
  value: PasswordForm;
  onChange: (patch: Partial<PasswordForm>) => void;
  loading: boolean;
  onSubmit: () => void;
}) {
  const { t } = useI18n();
  return (
    <div className="ui-card group relative overflow-hidden p-8">
      <CardDecor />
      <div className="relative z-10 mb-6 flex items-center gap-3">
        <CardIcon>
          <KeyRegular className="text-[24px]" />
        </CardIcon>
        <CardTitle title={t("安全")} subtitle={t("更新访问凭证")} />
      </div>
      <div className="relative z-10 space-y-4">
        <div className="space-y-1">
          <label className={PASSWORD_LABEL}>{t("当前密码")}</label>
          <PasswordInput
            inputSize="large"
            placeholder="••••••••"
            autoComplete="current-password"
            value={value.oldPassword}
            onChange={(oldPassword) => onChange({ oldPassword })}
          />
        </div>
        <div className="space-y-1">
          <label className={PASSWORD_LABEL}>{t("新密码")}</label>
          <PasswordInput
            inputSize="large"
            placeholder="••••••••"
            autoComplete="new-password"
            value={value.newPassword}
            onChange={(newPassword) => onChange({ newPassword })}
          />
        </div>
        <div className="space-y-1">
          <label className={PASSWORD_LABEL}>{t("确认新密码")}</label>
          <PasswordInput
            inputSize="large"
            placeholder="••••••••"
            autoComplete="new-password"
            value={value.confirmPassword}
            onChange={(confirmPassword) => onChange({ confirmPassword })}
          />
        </div>
        <div className="pt-4">
          <Button variant="primary" size="large" loading={loading} onClick={onSubmit} className="w-full !border-0" icon={<CheckmarkRegular />}>
            {t("更新凭证")}
          </Button>
        </div>
      </div>
    </div>
  );
}

export function SystemInfoCard({
  info,
  updateInfo,
  checkingUpdate,
  applyingUpdate,
  autoUpdate,
  savingAutoUpdate,
  onCheckUpdate,
  onApplyUpdate,
  onAutoUpdateChange,
  upstream,
  checkingUpstream,
  markingUpstream,
  onCheckUpstream,
  onMarkUpstreamSynced,
}: {
  info: SystemInfo;
  updateInfo: UpdateInfo | null;
  checkingUpdate: boolean;
  applyingUpdate: boolean;
  autoUpdate: AutoUpdateSettings | null;
  savingAutoUpdate: boolean;
  onCheckUpdate: () => void;
  onApplyUpdate: () => void;
  onAutoUpdateChange: (patch: Partial<AutoUpdateSettings>) => void;
  upstream: UpstreamVocatStatus | null;
  checkingUpstream: boolean;
  markingUpstream: boolean;
  onCheckUpstream: () => void;
  onMarkUpstreamSynced: () => void;
}) {
  const { t } = useI18n();
  return (
    <div className="ui-card group relative overflow-hidden p-8">
      <CardDecor />
      <div className="relative z-10 mb-6 flex items-center gap-3">
        <CardIcon>
          <InfoRegular className="text-[24px]" />
        </CardIcon>
        <CardTitle title={t("系统信息")} subtitle={t("运行环境")} />
      </div>
      <div className="relative z-10 space-y-4 text-sm">
        <div className="rounded-lg bg-gray-50 p-3 dark:bg-white/5">
          <FieldRow label={t("版本")} value={info.version} monospace>
            <span className="font-mono text-sm">{info.version || "Unknown"}</span>
            <Button size="small" variant="primary" className="!border-0" loading={checkingUpdate} onClick={onCheckUpdate}>
              {checkingUpdate ? t("正在检查更新") : t("检查更新")}
            </Button>
          </FieldRow>
        </div>
        {updateInfo?.hasUpdate ? (
          <div className="rounded-lg border border-amber-200 bg-amber-50 p-4 dark:border-amber-500/20 dark:bg-amber-500/10">
            <div className="mb-2 flex items-center gap-2 text-[13px] font-bold text-amber-800 dark:text-amber-200">
              <AlertRegular /> {t("发现新版本:")} {updateInfo.latestVersion}
            </div>
            <div className="mb-4 max-h-32 overflow-y-auto whitespace-pre-wrap pr-2 text-xs text-amber-700 dark:text-amber-300/80">
              {updateInfo.releaseNote || t("暂无更新说明")}
            </div>
            <Button variant="warning" loading={applyingUpdate} onClick={onApplyUpdate} className="w-full !border-0">
              {applyingUpdate ? t("正在更新...") : t("立即更新并重启")}
            </Button>
          </div>
        ) : updateInfo ? (
          <p className="text-[12px] text-black/45 dark:text-white/50">{t("当前已是最新版本")}</p>
        ) : null}
        <div className="space-y-3 rounded-lg bg-gray-50 p-3 dark:bg-white/5">
          <div className="flex items-center justify-between gap-3">
            <div>
              <div className="text-[13px] font-semibold text-black/80 dark:text-white/80">{t("自动检查更新")}</div>
              <p className="text-[12px] text-black/40 dark:text-white/45">{t("后台定期查询 Halo 发行版")}</p>
            </div>
            <Switch
              checked={!!autoUpdate?.enabled}
              disabled={!autoUpdate || savingAutoUpdate}
              loading={savingAutoUpdate}
              onChange={(enabled) => onAutoUpdateChange({ enabled })}
            />
          </div>
          <div className="flex items-center justify-between gap-3">
            <div>
              <div className="text-[13px] font-semibold text-black/80 dark:text-white/80">{t("自动安装并重启")}</div>
              <p className="text-[12px] text-black/40 dark:text-white/45">{t("发现新版本后下载校验并替换本机程序")}</p>
            </div>
            <Switch
              checked={!!autoUpdate?.apply}
              disabled={!autoUpdate || savingAutoUpdate || !!autoUpdate?.isDocker}
              loading={savingAutoUpdate}
              onChange={(apply) => onAutoUpdateChange({ apply })}
            />
          </div>
          <div className="flex items-center justify-between gap-3">
            <div className="text-[13px] font-semibold text-black/80 dark:text-white/80">{t("检查间隔")}</div>
            <Select
              value={String(autoUpdate?.intervalHours || 6)}
              disabled={!autoUpdate || savingAutoUpdate}
              onChange={(value) => onAutoUpdateChange({ intervalHours: Number(value) })}
              options={[
                { value: "1", label: t("每 1 小时") },
                { value: "6", label: t("每 6 小时") },
                { value: "12", label: t("每 12 小时") },
                { value: "24", label: t("每 24 小时") },
              ]}
            />
          </div>
          {autoUpdate?.lastCheckAt ? (
            <p className="text-[12px] text-black/40 dark:text-white/45">
              {t("上次检查")} {autoUpdate.lastCheckAt.replace("T", " ").replace("Z", " UTC")}
              {autoUpdate.lastError
                ? ` · ${autoUpdate.lastError}`
                : autoUpdate.lastAvailable
                  ? ` · ${t("发现新版本:")} ${autoUpdate.lastVersion || ""}`
                  : ` · ${t("当前已是最新版本")}`}
            </p>
          ) : null}
          {autoUpdate?.repository ? (
            <p className="font-mono text-[11px] text-black/35 dark:text-white/35">{autoUpdate.repository}</p>
          ) : null}
        </div>
        <div className="rounded-lg bg-gray-50 p-3 dark:bg-white/5">
          <FieldRow label={t("构建时间")} value={info.buildTime} monospace />
        </div>
        <div className="rounded-lg bg-gray-50 p-3 dark:bg-white/5">
          <FieldRow label={t("配置路径")} value={info.config} monospace copyable />
        </div>
        <div className="rounded-lg bg-gray-50 p-3 dark:bg-white/5">
          <FieldRow label={t("运行时长")} value={info.uptime} monospace />
        </div>
        <div className="rounded-lg bg-gray-50 p-3 dark:bg-white/5">
          <FieldRow label="OS" value={info.os} monospace />
        </div>
        <div className="rounded-lg bg-gray-50 p-3 dark:bg-white/5">
          <FieldRow label={t("架构")} value={info.architecture} monospace />
        </div>
        <div className="space-y-3 rounded-lg bg-gray-50 p-3 dark:bg-white/5">
          <div className="flex items-center justify-between gap-3">
            <div>
              <div className="text-[13px] font-semibold text-black/80 dark:text-white/80">{t("VoCat 上游")}</div>
              <p className="text-[12px] text-black/40 dark:text-white/45">
                {t("跟踪官方 VoCat 的新功能和说明。Halo 程序更新仍从本仓库 GitHub Releases 安装。")}
              </p>
            </div>
            <Button size="small" loading={checkingUpstream} onClick={onCheckUpstream}>
              {t("检查 VoCat")}
            </Button>
          </div>
          <FieldRow label={t("已同步")} value={upstream?.syncedVersion || "0.2.7"} monospace />
          {upstream?.latestVersion ? <FieldRow label={t("官方最新")} value={upstream.latestVersion} monospace /> : null}
          {upstream?.available ? (
            <div className="rounded-lg border border-amber-200 bg-amber-50 p-3 dark:border-amber-500/20 dark:bg-amber-500/10">
              <div className="mb-2 text-[13px] font-bold text-amber-800 dark:text-amber-200">
                {t("官方 VoCat 有新内容:")} {upstream.latestVersion}
              </div>
              <div className="mb-3 max-h-32 overflow-y-auto whitespace-pre-wrap text-xs text-amber-700 dark:text-amber-300/80">
                {upstream.releaseNotes || t("暂无更新说明")}
              </div>
              {upstream.htmlUrl ? (
                <a className="mb-3 block text-[12px] text-[var(--color-primary)] underline-offset-2 hover:underline" href={upstream.htmlUrl} target="_blank" rel="noreferrer">
                  {upstream.repository}
                </a>
              ) : null}
              <Button size="small" loading={markingUpstream} onClick={onMarkUpstreamSynced} className="w-full">
                {t("已合并进 Halo")}
              </Button>
            </div>
          ) : upstream?.lastCheckAt ? (
            <p className="text-[12px] text-black/40 dark:text-white/45">{t("官方 VoCat 暂无更新于已同步版本之后")}</p>
          ) : null}
          {upstream?.lastError ? <p className="text-[12px] text-red-600 dark:text-red-400">{upstream.lastError}</p> : null}
        </div>
        <p className="text-[12px] leading-relaxed text-black/40 dark:text-white/45">
          {t("Halo 是基于 VoCat 的个人界面与发行版。模组、IMS、WiFi Calling 等核心能力来自原项目。")}
          {" "}
          <a
            className="text-[var(--color-primary)] underline-offset-2 hover:underline"
            href="https://github.com/MengMengCode/VoCat"
            target="_blank"
            rel="noreferrer"
          >
            MengMengCode/VoCat
          </a>
        </p>
      </div>
    </div>
  );
}

export { CardDecor, CardIcon, CardTitle };
