import { useState } from "react";
import { ChevronDownRegular } from "@fluentui/react-icons";
import { cx } from "../../lib/utils";
import { CountryFlag } from "../CountryFlag";
import { EsimProfileRow } from "./EsimProfileRow";
import type { EsimChipInfo, EsimEid, EsimProfileGroup } from "./types";
import { useI18n } from "../../lib/i18n";

export interface SpaceNotice {
  aidHex: string;
  message: string;
}

export interface EsimEuiccGroupProps {
  deviceId: string;
  deviceOnline: boolean;
  group: EsimProfileGroup;
  index: number;
  chipInfo: EsimChipInfo | null;
  showSensitive: boolean;
  spaceNotice: SpaceNotice | null;
  renamingIccid: string | null;
  renameValue: string;
  switchingIccid: string | null;
  deletingIccid: string | null;
  policyIccid: string | null;
  onRenameValueChange: (v: string) => void;
  onSwitch: (iccid: string, state: number | undefined, aidHex?: string) => void;
  onStartRename: (iccid: string, name?: string) => void;
  onSubmitRename: (iccid: string, aidHex?: string) => void;
  onCancelRename: () => void;
  onTogglePolicy: (iccid: string) => void;
  onDelete: (iccid: string, name: string | undefined, aidHex?: string) => void;
  onPolicyChanged: () => void;
}

function normAid(aid?: string): string {
  return (aid || "").trim().toUpperCase();
}

function manufacturerCountryCode(manufacturer?: string): string {
  const value = (manufacturer || "").toLowerCase();
  if (value.includes("eastcompeace") || value.includes("watchdata") || value.includes("hutopt")) return "CN";
  if (value.includes("giesecke") || value.includes("g+d")) return "DE";
  if (value.includes("thales") || value.includes("idemia")) return "FR";
  if (value.includes("gemalto")) return "CH";
  return "";
}

function MetaItem({ label, value, mono }: { label: string; value?: string; mono?: boolean }) {
  if (!value) return null;
  return (
    <div className="min-w-0">
      <div className="text-[10px] uppercase tracking-wider text-gray-400">{label}</div>
      <div className={cx("mt-0.5 break-all text-[11px] text-gray-600 dark:text-gray-300", mono && "font-mono")}>{value}</div>
    </div>
  );
}

function PkiInfo({ eid }: { eid: EsimEid }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const extras = [
    eid.defaultSmdpAddress,
    eid.rootDsAddress,
    eid.sasAccreditationNumber,
    eid.aid,
    eid.trustedCiKeyIds?.length ? eid.trustedCiKeyIds.join(" · ") : "",
  ].some(Boolean);
  const hasSummary = eid.manufacturer || (eid.certificates && eid.certificates.length) || extras;
  if (!hasSummary) return null;
  return (
    <div className="mt-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-gray-400 dark:text-gray-500">
        {eid.manufacturer ? (
          <span className="inline-flex items-center gap-1">
            <span className="text-[10px]">{t("生产商:")}</span>
            <span>{eid.manufacturer}</span>
            <CountryFlag countryCode={manufacturerCountryCode(eid.manufacturer)} />
          </span>
        ) : null}
        {eid.certificates && eid.certificates.length ? (
          <span className="inline-flex min-w-0 items-center gap-1">
            <span className="shrink-0 text-[10px]">{t("证书:")}</span>
            <span className="break-all">{eid.certificates.join(" · ")}</span>
          </span>
        ) : null}
        {extras ? (
          <button
            type="button"
            onClick={() => setOpen((value) => !value)}
            className="inline-flex items-center gap-0.5 text-[11px] font-semibold text-[var(--color-primary)]"
          >
            {open ? t("收起芯片详情") : t("芯片详情")}
            <ChevronDownRegular className={cx("h-3.5 w-3.5 transition-transform", open && "rotate-180")} />
          </button>
        ) : null}
      </div>
      {open ? (
        <div className="mt-2 grid grid-cols-1 gap-2 rounded-lg bg-black/[0.03] p-2.5 dark:bg-white/5 sm:grid-cols-2">
          <MetaItem label="Default SM-DP+" value={eid.defaultSmdpAddress} />
          <MetaItem label="Root SM-DS" value={eid.rootDsAddress} />
          <MetaItem label="SAS" value={eid.sasAccreditationNumber} />
          <MetaItem label="ISD-R AID" value={eid.aid} mono />
          <MetaItem label="Trusted CI" value={eid.trustedCiKeyIds?.join(" · ")} mono />
        </div>
      ) : null}
    </div>
  );
}

export function EsimEuiccGroup(props: EsimEuiccGroupProps) {
  const { t } = useI18n();
  const { group, index, chipInfo } = props;
  const eidEntry =
    chipInfo?.eids?.find((e) => e.eid === group.eid) ||
    (chipInfo?.eids?.length === 1 ? chipInfo.eids[0] : chipInfo?.eids?.[index]);
  return (
    <div className="ui-panel-muted overflow-hidden">
      <div className="border-b border-gray-100 px-3 py-3 dark:border-white/10 sm:px-4">
        <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
          <div className="min-w-0">
            <div className="text-sm font-bold text-gray-900 dark:text-white">eUICC #{index + 1}</div>
            <div className={cx("mt-0.5 break-all font-mono text-xs text-gray-400 transition-all", !props.showSensitive && "select-none blur-sm")}>
              {group.eid}
            </div>
          </div>
          {eidEntry ? (
            <div className="shrink-0 text-xs text-gray-500 sm:text-right">
              <span className="inline-flex items-center gap-1">
                <span className={cx("h-2 w-2 rounded-full", (eidEntry.freeNvramBytes ?? 0) > 1e5 ? "bg-green-500" : "bg-yellow-500")} />
                {t("可用")} {eidEntry.freeNvram}
              </span>
              {props.spaceNotice && normAid(group.aidHex) === props.spaceNotice.aidHex ? (
                <div className="mt-1 text-[11px] text-emerald-600 dark:text-emerald-400">{props.spaceNotice.message}</div>
              ) : null}
            </div>
          ) : null}
        </div>
        {eidEntry ? <PkiInfo eid={eidEntry} /> : null}
      </div>
      {(group.profiles || []).length === 0 ? (
        <div className="p-4 text-sm text-gray-400">{t("暂无 Profile")}</div>
      ) : (
        <div className="divide-y divide-gray-100 dark:divide-white/10">
          {(group.profiles || []).map((p) => (
            <EsimProfileRow
              key={p.iccid}
              deviceId={props.deviceId}
              deviceOnline={props.deviceOnline}
              aidHex={group.aidHex}
              profile={p}
              showSensitive={props.showSensitive}
              renaming={props.renamingIccid === p.iccid}
              renameValue={props.renameValue}
              switching={props.switchingIccid === p.iccid}
              deleting={props.deletingIccid === p.iccid}
              policyOpen={props.policyIccid === p.iccid}
              onRenameValueChange={props.onRenameValueChange}
              onSwitch={() => props.onSwitch(p.iccid, p.state, group.aidHex)}
              onStartRename={() => props.onStartRename(p.iccid, p.name)}
              onSubmitRename={() => props.onSubmitRename(p.iccid, group.aidHex)}
              onCancelRename={props.onCancelRename}
              onTogglePolicy={() => props.onTogglePolicy(p.iccid)}
              onDelete={() => props.onDelete(p.iccid, p.name, group.aidHex)}
              onPolicyChanged={props.onPolicyChanged}
            />
          ))}
        </div>
      )}
    </div>
  );
}
