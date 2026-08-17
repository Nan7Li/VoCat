import { useCallback, useEffect, useState } from "react";
import { AddRegular, DeleteRegular, EditRegular, ShieldLockRegular } from "@fluentui/react-icons";
import { api, apiMessage } from "../api";
import type { WireGuardSnapshot, WireGuardTunnel } from "../types";
import {
  Button,
  EmptyState,
  Input,
  Modal,
  PageHeader,
  StatusDot,
  Switch,
  Textarea,
  confirmDialog,
  message,
} from "../components/ui";
import { useI18n } from "../lib/i18n";

const EMPTY_FORM = {
  id: "",
  name: "",
  interface: "",
  config: "",
  autostart: true,
};

function formatBytes(value = 0) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`;
  if (value < 1024 * 1024 * 1024) return `${(value / 1024 / 1024).toFixed(1)} MB`;
  return `${(value / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

export default function WireGuardPage() {
  const { t } = useI18n();
  const [snapshot, setSnapshot] = useState<WireGuardSnapshot>({ available: false, tunnels: [] });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState(EMPTY_FORM);
  const [saving, setSaving] = useState(false);
  const [busy, setBusy] = useState("");

  const load = useCallback(async (initial = false) => {
    if (initial) setLoading(true);
    try {
      const data = await api<WireGuardSnapshot>("/wireguard-tunnels");
      setSnapshot({
        available: !!data.available,
        hint: data.hint,
        tunnels: Array.isArray(data.tunnels) ? data.tunnels : [],
      });
      setError("");
    } catch (err) {
      setError(apiMessage(err));
    } finally {
      if (initial) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load(true);
    const timer = window.setInterval(() => void load(), 5000);
    return () => window.clearInterval(timer);
  }, [load]);

  function openCreate() {
    setForm(EMPTY_FORM);
    setOpen(true);
  }

  function openEdit(tunnel: WireGuardTunnel) {
    setForm({
      id: tunnel.id,
      name: tunnel.name,
      interface: tunnel.interface,
      config: tunnel.config,
      autostart: tunnel.autostart,
    });
    setOpen(true);
  }

  async function onSave() {
    if (!form.name.trim() || !form.config.trim()) {
      message.error(t("请填写名称和配置"));
      return;
    }
    setSaving(true);
    try {
      const payload = {
        name: form.name.trim(),
        interface: form.interface.trim(),
        config: form.config,
        autostart: form.autostart,
      };
      if (form.id) {
        await api(`/wireguard-tunnels/${encodeURIComponent(form.id)}`, { method: "PUT", body: payload });
        message.success(t("隧道已保存"));
      } else {
        await api("/wireguard-tunnels", { method: "POST", body: payload });
        message.success(t("隧道已添加"));
      }
      setOpen(false);
      await load();
    } catch (err) {
      message.error(apiMessage(err) || t("保存隧道失败"));
    } finally {
      setSaving(false);
    }
  }

  async function onToggle(tunnel: WireGuardTunnel) {
    setBusy(tunnel.id);
    try {
      await api(`/wireguard-tunnels/${encodeURIComponent(tunnel.id)}/${tunnel.running ? "down" : "up"}`, { method: "POST" });
      message.success(tunnel.running ? t("隧道已关闭") : t("隧道已连接"));
      await load();
    } catch (err) {
      message.error(apiMessage(err) || t("切换隧道失败"));
    } finally {
      setBusy("");
    }
  }

  async function onDelete(tunnel: WireGuardTunnel) {
    const ok = await confirmDialog(
      t("删除后配置会从本机移除。若隧道正在运行，会先断开。"),
      t("删除 WG 隧道"),
      { confirmText: t("删除"), cancelText: t("取消"), type: "warning" },
    );
    if (!ok) return;
    setBusy(tunnel.id);
    try {
      await api(`/wireguard-tunnels/${encodeURIComponent(tunnel.id)}`, { method: "DELETE" });
      message.success(t("隧道已删除"));
      await load();
    } catch (err) {
      message.error(apiMessage(err) || t("删除隧道失败"));
    } finally {
      setBusy("");
    }
  }

  return (
    <div className="mx-auto max-w-5xl">
      <PageHeader
        title={t("WG 隧道")}
        subtitle={t("在本机用 wg-quick 管理 WireGuard 配置，可开机自动拉起")}
        actions={
          <Button variant="primary" icon={<AddRegular />} onClick={openCreate} className="!border-0">
            {t("添加隧道")}
          </Button>
        }
      />

      {!snapshot.available ? (
        <div className="mb-6 rounded-2xl border border-amber-200 bg-amber-50 px-4 py-3 text-[13px] text-amber-800 dark:border-amber-500/20 dark:bg-amber-500/10 dark:text-amber-200">
          {snapshot.hint || t("当前主机未安装 WireGuard。Linux 上安装 kmod-wireguard、wg 和 ip 后即可连接。")}
        </div>
      ) : null}

      {loading ? (
        <div className="ui-card p-8 text-sm text-black/40 dark:text-white/45">{t("正在加载隧道…")}</div>
      ) : error ? (
        <div className="ui-card p-8 text-sm text-red-600">{error}</div>
      ) : snapshot.tunnels.length === 0 ? (
        <EmptyState
          icon={<ShieldLockRegular className="text-[22px]" />}
          title={t("还没有 WG 隧道")}
          subtitle={t("粘贴一份标准 wg-quick 配置即可保存，之后可一键连接或开机自动启动。")}
          actions={
            <Button variant="primary" icon={<AddRegular />} onClick={openCreate} className="!border-0">
              {t("添加隧道")}
            </Button>
          }
        />
      ) : (
        <div className="grid grid-cols-1 gap-4">
          {snapshot.tunnels.map((tunnel) => (
            <div key={tunnel.id} className="ui-card p-6">
              <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <StatusDot tone={tunnel.running ? "success" : "neutral"} animated={tunnel.running} />
                    <h3 className="text-[17px] font-semibold tracking-tight">{tunnel.name}</h3>
                    <span className="rounded-full bg-black/5 px-2 py-0.5 font-mono text-[11px] text-black/50 dark:bg-white/10 dark:text-white/50">
                      {tunnel.interface}
                    </span>
                    {tunnel.external ? (
                      <span className="rounded-full bg-black/5 px-2 py-0.5 text-[11px] font-medium text-black/50 dark:bg-white/10 dark:text-white/50">
                        {t("系统隧道")}
                      </span>
                    ) : null}
                    {tunnel.autostart && !tunnel.external ? (
                      <span className="rounded-full bg-[var(--color-primary-soft)] px-2 py-0.5 text-[11px] font-medium text-[var(--color-primary)]">
                        {t("开机启动")}
                      </span>
                    ) : null}
                  </div>
                  <div className="mt-2 space-y-1 text-[13px] text-black/45 dark:text-white/45">
                    <div>{tunnel.running ? t("已连接") : t("未连接")}</div>
                    {tunnel.endpoint ? <div>Endpoint {tunnel.endpoint}</div> : null}
                    {tunnel.publicKey ? <div className="break-all font-mono text-[11px]">{tunnel.publicKey}</div> : null}
                    {tunnel.running ? (
                      <div>
                        {t("流量")} {formatBytes(tunnel.transferRx)} ↓ / {formatBytes(tunnel.transferTx)} ↑
                        {tunnel.peers ? ` · ${tunnel.peers} peer` : ""}
                      </div>
                    ) : null}
                    {tunnel.error ? <div className="text-red-600 dark:text-red-400">{tunnel.error}</div> : null}
                  </div>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  {tunnel.external ? (
                    <span className="text-[12px] text-black/40 dark:text-white/40">{t("由路由器网络配置管理")}</span>
                  ) : (
                    <>
                      <Button
                        size="small"
                        variant={tunnel.running ? "default" : "primary"}
                        loading={busy === tunnel.id}
                        disabled={!snapshot.available && !tunnel.running}
                        onClick={() => void onToggle(tunnel)}
                      >
                        {tunnel.running ? t("断开") : t("连接")}
                      </Button>
                      <Button size="small" icon={<EditRegular />} onClick={() => openEdit(tunnel)}>
                        {t("修改")}
                      </Button>
                      <Button size="small" icon={<DeleteRegular />} disabled={busy === tunnel.id} onClick={() => void onDelete(tunnel)}>
                        {t("删除")}
                      </Button>
                    </>
                  )}
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      <Modal
        open={open}
        onClose={() => setOpen(false)}
        title={form.id ? t("修改 WG 隧道") : t("添加 WG 隧道")}
        width="max-w-2xl"
        footer={
          <div className="flex justify-end gap-2">
            <Button onClick={() => setOpen(false)}>{t("取消")}</Button>
            <Button variant="primary" loading={saving} onClick={() => void onSave()} className="!border-0">
              {t("保存")}
            </Button>
          </div>
        }
      >
        <div className="space-y-4">
          <div className="space-y-1">
            <label className="text-xs font-bold uppercase tracking-wider text-gray-500">{t("名称")}</label>
            <Input value={form.name} onChange={(event) => setForm((prev) => ({ ...prev, name: event.target.value }))} placeholder="Home" />
          </div>
          <div className="space-y-1">
            <label className="text-xs font-bold uppercase tracking-wider text-gray-500">{t("网卡名")}</label>
            <Input
              value={form.interface}
              onChange={(event) => setForm((prev) => ({ ...prev, interface: event.target.value }))}
              placeholder="halo0"
            />
            <p className="text-[12px] text-black/40 dark:text-white/45">{t("留空则自动分配 halo0、halo1…")}</p>
          </div>
          <div className="flex items-center justify-between gap-3 rounded-xl bg-black/[0.03] px-3 py-2 dark:bg-white/5">
            <div>
              <div className="text-[13px] font-semibold">{t("开机自动连接")}</div>
              <p className="text-[12px] text-black/40 dark:text-white/45">{t("Halo 启动后会尝试 wg-quick up")}</p>
            </div>
            <Switch checked={form.autostart} onChange={(autostart) => setForm((prev) => ({ ...prev, autostart }))} />
          </div>
          <div className="space-y-1">
            <label className="text-xs font-bold uppercase tracking-wider text-gray-500">{t("配置")}</label>
            <Textarea
              rows={14}
              className="font-mono text-[13px]"
              value={form.config}
              onChange={(event) => setForm((prev) => ({ ...prev, config: event.target.value }))}
              placeholder={"[Interface]\nPrivateKey = ...\nAddress = 10.8.0.2/32\n\n[Peer]\nPublicKey = ...\nEndpoint = x.x.x.x:51820\nAllowedIPs = 0.0.0.0/0"}
            />
            <p className="text-[12px] text-black/40 dark:text-white/45">
              {t("使用标准 wg-quick 配置。私钥不会回显到页面；修改时保留 ******** 即可沿用原密钥。")}
            </p>
          </div>
        </div>
      </Modal>
    </div>
  );
}
