import { AddRegular, ArrowDownloadRegular } from "@fluentui/react-icons";
import { cx } from "../../lib/utils";
import { Button, Input, Select } from "../ui";
import type { EsimChipInfo, EsimDownloadForm } from "./types";
import { tf, useI18n } from "../../lib/i18n";

export interface EsimDownloadFormProps {
  form: EsimDownloadForm;
  chipInfo: EsimChipInfo | null;
  downloading: boolean;
  downloadPct: number;
  downloadMsg: string;
  downloadErr: string;
  onFormChange: (next: EsimDownloadForm) => void;
  onDownload: () => void;
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <div className="text-[11px] font-bold uppercase tracking-wider text-gray-500">{label}</div>
      {children}
    </div>
  );
}

export function EsimDownloadForm(props: EsimDownloadFormProps) {
  const { t } = useI18n();
  const { form, chipInfo } = props;
  const set = (patch: Partial<EsimDownloadForm>) => props.onFormChange({ ...form, ...patch });
  const eidOptions = (chipInfo?.eids || []).map((e, i) => ({
    value: e.aid || "",
    label: tf("eUICC #{n} (...{tail}) — {free} 可用", { n: i + 1, tail: e.eid.slice(-4), free: e.freeNvram }),
  }));
  return (
    <div className="ui-panel-muted p-4">
      <div className="mb-3 flex items-center gap-2">
        <div className="flex h-7 w-7 items-center justify-center rounded-lg bg-indigo-50 text-indigo-600 dark:bg-indigo-500/10 dark:text-indigo-400">
          <AddRegular className="text-[16px]" />
        </div>
        <div className="text-sm font-bold text-gray-900 dark:text-white">{t("下载新 Profile")}</div>
      </div>
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <Field label={t("SM-DP+ 地址 *")}>
          <Input value={form.smdp} onChange={(e) => set({ smdp: e.target.value })} placeholder={t("例如 rsp.truphone.com")} />
        </Field>
        <Field label="Matching ID">
          <Input value={form.matchingId} onChange={(e) => set({ matchingId: e.target.value })} placeholder={t("可选")} />
        </Field>
        <Field label={t("确认码")}>
          <Input value={form.confirmationCode} onChange={(e) => set({ confirmationCode: e.target.value })} placeholder={t("可选")} />
        </Field>
        <Field label={t("目标 eUICC")}>
          <Select value={form.aidHex} onChange={(v) => set({ aidHex: v })} placeholder={t("选择目标 eUICC")} options={eidOptions} />
        </Field>
      </div>
      {props.downloading || props.downloadErr ? (
        <div className="mt-4 space-y-1.5">
          <div className="h-2.5 w-full overflow-hidden rounded-full bg-gray-200 dark:bg-white/10">
            <div
              className={cx(
                "h-full w-full origin-left rounded-full transition-transform duration-500 ease-[cubic-bezier(0.32,0.72,0,1)]",
                props.downloadErr ? "bg-red-500" : props.downloadPct >= 100 ? "bg-green-500" : "bg-[var(--color-primary)]",
              )}
              style={{ transform: `scaleX(${Math.max(0, Math.min(100, props.downloadPct)) / 100})` }}
            />
          </div>
          <div className={cx("text-xs", props.downloadErr ? "text-red-500" : "text-gray-500 dark:text-gray-400")}>
            {props.downloadErr || props.downloadMsg}
          </div>
        </div>
      ) : null}
      <div className="mt-4 flex justify-end">
        <Button variant="primary" loading={props.downloading} disabled={props.downloading} onClick={props.onDownload} className="!border-0" icon={<ArrowDownloadRegular />}>
          {t("开始下载")}
        </Button>
      </div>
    </div>
  );
}
