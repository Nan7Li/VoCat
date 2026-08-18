import { useCallback, useEffect, useMemo, useState } from "react";
import {
  ArrowDownloadRegular,
  ArrowLeftRegular,
  ArrowRightRegular,
  ArrowSyncRegular,
  CallRegular,
  HistoryRegular,
} from "@fluentui/react-icons";
import { api, apiMessage } from "../api";
import { useI18n } from "../lib/i18n";
import { BrowserSoftphone } from "../components/devices/BrowserSoftphone";
import { softphoneReadyReason } from "../components/devices/shared";
import type { CallRecord, CallRecordsResponse, DeviceListItem, DevicesResponse } from "../types";
import { Button, Select, Tag, message } from "../components/ui";
import { PageHeader } from "../components/ui/PageHeader";
import { cx } from "../lib/utils";

function formatDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "--";
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  return minutes > 0 ? `${minutes}:${String(rest).padStart(2, "0")}` : `0:${String(rest).padStart(2, "0")}`;
}

function formatTime(value?: string | null): string {
  if (!value) return "--";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const now = new Date();
  const sameDay = date.toDateString() === now.toDateString();
  const time = date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  if (sameDay) return time;
  const monthDay = date.toLocaleDateString([], { month: "numeric", day: "numeric" });
  const sameYear = date.getFullYear() === now.getFullYear();
  return sameYear ? `${monthDay} ${time}` : `${date.toLocaleDateString([], { year: "numeric", month: "numeric", day: "numeric" })} ${time}`;
}

function recordTag(t: (text: string) => string, record: CallRecord) {
  switch (record.state) {
    case "answered":
      return <Tag type="success">{t("接通")}</Tag>;
    case "missed":
      return <Tag type="warning">{t("未接")}</Tag>;
    case "failed":
      return <Tag type="danger">{t("失败")}</Tag>;
    case "active":
      return <Tag type="primary">{t("进行中")}</Tag>;
    default:
      return <Tag type="info">{t("已结束")}</Tag>;
  }
}

export default function PhonePage() {
  const { t } = useI18n();
  const [devices, setDevices] = useState<DeviceListItem[]>([]);
  const [deviceId, setDeviceId] = useState<string>("");
  const [records, setRecords] = useState<CallRecord[]>([]);
  const [loading, setLoading] = useState(false);
  const [recordsLoading, setRecordsLoading] = useState(false);

  const loadDevices = useCallback(async () => {
    try {
      const result = await api<DevicesResponse>("/devices");
      const list = result?.devices || [];
      setDevices(list);
      setDeviceId((current) => {
        if (current && list.some((device) => device.id === current)) return current;
        const preferred = list.find((device) => device.vowifiEnabled && device.running) || list[0];
        return preferred?.id || "";
      });
    } catch (error) {
      message.error(apiMessage(error));
    }
  }, []);

  const loadRecords = useCallback(async () => {
    try {
      const query = deviceId ? `?device_id=${encodeURIComponent(deviceId)}&limit=100` : "?limit=100";
      const result = await api<CallRecordsResponse>(`/calls/history${query}`);
      setRecords(result?.records || []);
    } catch {
      // History is auxiliary; the softphone stays usable when it fails.
      setRecords([]);
    }
  }, [deviceId]);

  useEffect(() => {
    void loadDevices();
  }, [loadDevices]);

  useEffect(() => {
    if (!deviceId) return;
    setRecordsLoading(true);
    void loadRecords().finally(() => setRecordsLoading(false));
    const timer = window.setInterval(() => void loadRecords(), 10_000);
    return () => window.clearInterval(timer);
  }, [deviceId, loadRecords]);

  const selectedDevice = useMemo(() => devices.find((device) => device.id === deviceId), [devices, deviceId]);
  const ready = !!selectedDevice && !!selectedDevice.vowifiEnabled && !!selectedDevice.vowifiRuntime?.imsReady;
  const reason = selectedDevice ? softphoneReadyReason(selectedDevice) : "";

  const deviceOptions = useMemo(
    () => devices.map((device) => ({
      value: device.id,
      label: (
        <span className="flex items-center gap-2">
          <span className="min-w-0 flex-1 truncate">{device.name || device.id}</span>
          {device.vowifiEnabled && device.vowifiRuntime?.imsReady ? (
            <span className="rounded-full bg-emerald-500/10 px-1.5 py-0.5 text-[10px] font-bold text-emerald-600">IMS</span>
          ) : null}
        </span>
      ),
    })),
    [devices],
  );

  return (
    <div className="mx-auto max-w-4xl space-y-4">
      <PageHeader
        title={t("电话")}
        actions={
          <Button variant="text" onClick={() => { void loadDevices(); void loadRecords(); }} icon={<ArrowSyncRegular />}>
            {t("刷新")}
          </Button>
        }
      />

      {devices.length === 0 ? (
        <div className="ui-card p-10 text-center text-sm text-gray-400">
          {t("暂无设备，请先在「设备管理」中添加设备。")}
        </div>
      ) : (
        <>
          <div className="ui-panel-muted flex flex-wrap items-center gap-3 p-4">
            <span className="text-sm font-semibold text-gray-600 dark:text-gray-300">{t("通话设备")}</span>
            <Select
              value={deviceId}
              onChange={setDeviceId}
              options={deviceOptions}
              className="min-w-56 flex-1"
              placeholder={t("请选择设备")}
            />
            {selectedDevice ? (
              <span className="text-xs text-gray-500">
                {selectedDevice.name || selectedDevice.id} · {selectedDevice.modem?.operator || "--"}
              </span>
            ) : null}
          </div>

          {selectedDevice ? (
            <BrowserSoftphone deviceId={deviceId} deviceName={selectedDevice.name} ready={ready} reason={reason} />
          ) : null}

          <section className="ui-panel-muted p-4">
            <div className="mb-3 flex items-center justify-between">
              <div className="flex items-center gap-2">
                <HistoryRegular className="h-4 w-4 text-gray-500" />
                <div className="text-xs font-bold uppercase tracking-wider text-gray-500">{t("通话记录")}</div>
                {recordsLoading ? <span className="text-[10px] text-gray-400">{t("刷新中…")}</span> : null}
              </div>
              <span className="text-[10px] text-gray-400">{t("号码 · 方向 · 时间 · 时长 · 接通/未接/失败 · 设备")}</span>
            </div>

            {records.length === 0 ? (
              <div className="py-6 text-center text-xs text-gray-400">{t("暂无通话记录")}</div>
            ) : (
              <div className="space-y-2">
                {records.map((record) => (
                  <div key={record.id} className="flex flex-wrap items-center gap-3 rounded-lg border border-gray-200 px-3 py-2 text-sm dark:border-white/10">
                    <span className={cx(
                      "flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-full",
                      record.direction === "incoming" ? "bg-emerald-500/10 text-emerald-600" : "bg-blue-500/10 text-blue-500",
                    )}>
                      {record.direction === "incoming" ? <ArrowLeftRegular /> : <ArrowRightRegular />}
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="truncate font-semibold">{record.number || t("未知号码")}</span>
                        {recordTag(t, record)}
                      </div>
                      <div className="text-xs text-gray-500">
                        {formatTime(record.startedAt)}
                        {record.transport === "cellular" ? ` · ${t("蜂窝")}` : " · VoWiFi"}
                        {record.durationSeconds > 0 ? ` · ${formatDuration(record.durationSeconds)}` : ""}
                      </div>
                    </div>
                    {record.recording ? (
                      <div className="flex flex-wrap items-center gap-2">
                        <audio
                          controls
                          preload="none"
                          className="h-9 w-56 max-w-full"
                          src={`/api/calls/history/${record.id}/recording`}
                        />
                        <a
                          href={`/api/calls/history/${record.id}/recording`}
                          download
                          className="inline-flex h-9 items-center gap-1 rounded-lg border border-gray-300 px-3 text-xs font-semibold text-gray-600 hover:bg-gray-100 dark:border-white/15 dark:text-gray-300 dark:hover:bg-white/5"
                        >
                          <ArrowDownloadRegular className="h-4 w-4" />
                          {t("下载")}
                        </a>
                      </div>
                    ) : null}
                  </div>
                ))}
              </div>
            )}
          </section>
        </>
      )}
    </div>
  );
}
