import { useCallback, useEffect, useId, useState } from "react";
import { api, apiMessage } from "../../api";
import type { DeviceListItem } from "../../types";
import { Button, Input, Select, Switch, Tag, message } from "../ui";

export interface CellBridgeConfig {
  sipEnabled: boolean;
  sipDeviceId: string;
  listenAddr: string;
  advertisedIp: string;
  rtpListenAddr: string;
  username: string;
  hasPassword: boolean;
  cellularEnabled: boolean;
  cellularDeviceId: string;
  captureDevice: string;
  playbackDevice: string;
  runtimeDir: string;
  adbPath: string;
  adbSocket: string;
  bootstrap: boolean;
}

interface BridgeStatus {
  phase: string;
  reason?: string;
  sip?: { running: boolean; registered: boolean; listenAddr?: string; rtpListenAddr?: string; advertisedIp?: string; username?: string; reason?: string };
  cellular?: { ready: boolean; deviceId?: string; reason?: string };
}
interface BridgeDocument { config: CellBridgeConfig; status: BridgeStatus }

const defaults: CellBridgeConfig = {
  sipEnabled: false, sipDeviceId: "", listenAddr: "0.0.0.0:5060", advertisedIp: "",
  rtpListenAddr: "0.0.0.0:40000", username: "halo", hasPassword: false,
  cellularEnabled: false, cellularDeviceId: "", captureDevice: "plughw:1,0",
  playbackDevice: "plughw:1,0", runtimeDir: "/opt/halo/qdc507", adbPath: "adb",
  adbSocket: "tcp:127.0.0.1:5038", bootstrap: false,
};

export function CellBridgeSettings({ devices }: { devices: DeviceListItem[] }) {
  const prefix = useId();
  const [config, setConfig] = useState<CellBridgeConfig | null>(null);
  const [status, setStatus] = useState<BridgeStatus>({ phase: "loading" });
  const [password, setPassword] = useState("");
  const [saving, setSaving] = useState(false);
  const [loadError, setLoadError] = useState("");
  const current = config ?? defaults;
  const options = devices.map(device => ({ value: device.id, label: device.name || device.id }));
  const cellularOptions = devices.filter(device => device.deviceType === "dji_4g")
    .map(device => ({ value: device.id, label: device.name || device.id }));
  const liveIP = status.sip?.advertisedIp || current.advertisedIp;
  const livePort = (status.sip?.listenAddr || current.listenAddr).match(/:(\d+)$/)?.[1] || "5060";
  const clientAddress = `${liveIP.includes(":") ? `[${liveIP}]` : liveIP}:${livePort}`;

  const load = useCallback(async () => {
    try {
      const result = await api<BridgeDocument>("/settings/cellbridge");
      if (!result.config || !result.status) throw new Error("通话桥配置不可用");
      setConfig(previous => previous
        ? { ...previous, hasPassword: result.config.hasPassword }
        : { ...defaults, ...result.config });
      setStatus(result.status);
      setLoadError("");
    } catch (error) { setLoadError(apiMessage(error)); }
  }, []);

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 3000);
    return () => window.clearInterval(timer);
  }, [load]);

  function change<K extends keyof CellBridgeConfig>(key: K, value: CellBridgeConfig[K]) {
    setConfig(previous => ({ ...(previous ?? defaults), [key]: value }));
  }
  async function save() {
    setSaving(true);
    try {
      const { hasPassword: _hasPassword, ...editable } = current;
      const result = await api<BridgeDocument>("/settings/cellbridge", {
        method: "PUT", body: { ...editable, password },
      });
      setConfig({ ...defaults, ...result.config });
      setStatus(result.status);
      setPassword("");
      message.success("通话桥配置已保存");
    } catch (error) { message.error(apiMessage(error)); }
    finally { setSaving(false); }
  }
  function field(key: keyof CellBridgeConfig, label: string, placeholder = "") {
    return <label className="grid min-w-0 gap-1.5 text-xs text-gray-500" htmlFor={`${prefix}-${key}`}>
      {label}
      <Input id={`${prefix}-${key}`} value={String(current[key])} placeholder={placeholder}
        disabled={!config || saving} onChange={event => change(key, event.target.value)} />
    </label>;
  }

  return <section className="ui-panel-muted space-y-3 p-4" aria-label="CellBridge 通话桥">
    <div className="flex flex-wrap items-center justify-between gap-2">
      <div className="flex items-center gap-2">
        <h2 className="text-sm font-semibold">CellBridge · 通话桥</h2>
        <Tag type={status.phase === "failed" ? "danger" : status.phase === "ready" ? "success" : "info"}>
          {status.phase === "ready" ? "已运行" : status.phase === "applying" ? "正在应用" : status.phase === "disabled" ? "已关闭" : status.phase === "loading" ? "加载中" : "未就绪"}
        </Tag>
      </div>
      <Button variant="text" size="small" onClick={() => void load()}>刷新状态</Button>
    </div>
    <p className="text-xs text-gray-500">大疆模块可使用浏览器音频；SIP 客户端可选择大疆或 SIM 读卡器的通话线路。</p>
    {(loadError || status.reason) && <p className="break-words text-xs text-amber-600 dark:text-amber-400" role="status">{loadError || status.reason}</p>}
    <div className="grid gap-2 text-xs sm:grid-cols-2">
      <div className="rounded-lg border border-gray-200 p-2.5 dark:border-white/10">
        <span className="font-medium">SIP · </span>
        {status.sip?.registered ? "客户端已注册" : status.sip?.running ? "等待客户端注册" : "已停止"}
        {status.sip?.reason && <p className="mt-1 break-words text-gray-500">{status.sip.reason}</p>}
      </div>
      <div className="rounded-lg border border-gray-200 p-2.5 dark:border-white/10">
        <span className="font-medium">大疆音频 · </span>{status.cellular?.ready ? "已就绪" : "未就绪"}
        {status.cellular?.reason && <p className="mt-1 break-words text-gray-500">{status.cellular.reason}</p>}
      </div>
    </div>
    {status.sip?.running && status.phase !== "applying" && <p className="break-all text-xs text-gray-500">
      客户端连接：{clientAddress} · 账号 {status.sip.username || current.username} · UDP / PCMU
    </p>}
    <details className="rounded-lg border border-gray-200 p-3 dark:border-white/10">
      <summary className="cursor-pointer text-xs font-semibold">连接和音频设置</summary>
      <div className="mt-4 grid gap-5 md:grid-cols-2">
        <div className="min-w-0 space-y-3">
          <div className="flex items-center justify-between text-sm"><span>启用 SIP 服务</span>
            <Switch ariaLabel="启用 SIP 服务" checked={current.sipEnabled} disabled={!config || saving} onChange={value => change("sipEnabled", value)} />
          </div>
          <div className="grid gap-1.5 text-xs text-gray-500">SIP 通话线路
            <Select value={current.sipDeviceId} options={options} onChange={value => change("sipDeviceId", value)} placeholder="请选择设备" />
          </div>
          {field("advertisedIp", "Halo 连接地址", "局域网、WireGuard 或 Tailscale IP")}
          {field("username", "SIP 账号")}
          <label className="grid gap-1.5 text-xs text-gray-500" htmlFor={`${prefix}-password`}>SIP 密码
            <Input id={`${prefix}-password`} type="password" autoComplete="new-password" value={password}
              placeholder={current.hasPassword ? "已设置，留空保留" : "设置 SIP 密码"} disabled={!config || saving}
              onChange={event => setPassword(event.target.value)} />
          </label>
          {field("listenAddr", "SIP 监听地址")}
          {field("rtpListenAddr", "音频 UDP 监听地址")}
        </div>
        <div className="min-w-0 space-y-3">
          <div className="flex items-center justify-between text-sm"><span>启用大疆通话音频</span>
            <Switch ariaLabel="启用大疆通话音频" checked={current.cellularEnabled} disabled={!config || saving} onChange={value => change("cellularEnabled", value)} />
          </div>
          <div className="grid gap-1.5 text-xs text-gray-500">蜂窝音频设备
            <Select value={current.cellularDeviceId} options={cellularOptions} onChange={value => change("cellularDeviceId", value)} placeholder="请选择大疆模块" />
          </div>
          <p className="text-xs text-gray-500">读卡器的音频通过现有 VoWiFi 提供，需要 IMS 注册成功。</p>
          <details className="space-y-3 rounded-lg border border-gray-200 p-3 dark:border-white/10">
            <summary className="cursor-pointer text-xs font-medium">大疆音频高级设置</summary>
            {field("captureDevice", "ALSA 录音设备")}
            {field("playbackDevice", "ALSA 播放设备")}
            <p className="text-xs text-gray-500">按 NAS 检查工具列出的模块声卡填写录音和播放设备，声卡编号可能变化。</p>
            {field("runtimeDir", "模块运行文件目录", "/opt/halo/qdc507")}
            <p className="text-xs text-gray-500">NAS 安装器将通过校验的三个运行文件放在此目录。文件齐全后仍需确认模块 UAC、ADB 和双向通话。</p>
            {field("adbPath", "ADB 程序", "adb")}
            {field("adbSocket", "ADB 服务地址")}
            <div className="flex items-center justify-between gap-2 text-xs"><span>允许初始化匹配的 QDC507 音频</span>
              <Switch ariaLabel="允许初始化 QDC507 音频" checked={current.bootstrap} disabled={!config || saving} onChange={value => change("bootstrap", value)} />
            </div>
          </details>
        </div>
      </div>
      <div className="mt-4 flex justify-end"><Button variant="primary" loading={saving} disabled={!config} onClick={() => void save()}>保存通话桥设置</Button></div>
    </details>
  </section>;
}
