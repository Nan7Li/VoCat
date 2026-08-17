import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  PlayRegular,
  PauseRegular,
  DeleteRegular,
  ArrowDownloadRegular,
  DismissRegular,
} from "@fluentui/react-icons";
import { api, eventStreamURL } from "../api";
import type { LogEntry } from "../types";
import { cx } from "../lib/utils";
import { useI18n } from "../lib/i18n";
import { PageHeader } from "../components/ui/PageHeader";
import { Button } from "../components/ui/Button";
import { Switch } from "../components/ui/Switch";
import { Select } from "../components/ui/Select";
import { Input } from "../components/ui/Input";
import { message } from "../components/ui/message";
import { LogRetentionCard } from "../components/logs/LogRetentionCard";

const MAX_LOGS = 1000;

type Level = "all" | "debug" | "info" | "warn" | "error";

const LEVEL_OPTIONS: { value: Level; label: string }[] = [
  { value: "all", label: "全部" },
  { value: "debug", label: "DEBUG" },
  { value: "info", label: "INFO" },
  { value: "warn", label: "WARN" },
  { value: "error", label: "ERROR" },
];

function levelColor(level: string): string {
  switch (level.toLowerCase()) {
    case "debug":
      return "text-purple-500";
    case "info":
      return "text-[var(--color-primary)]";
    case "warn":
      return "text-yellow-500";
    case "error":
      return "text-red-500";
    case "fatal":
      return "text-red-600 font-bold";
    default:
      return "text-gray-500";
  }
}

function fieldsText(fields: LogEntry["fields"]): string {
  if (fields === undefined || fields === null) return "";
  return typeof fields === "string" ? fields : JSON.stringify(fields);
}

// Reference renders a fixed YYYY-MM-DD HH:mm:ss timestamp.
function displayTime(time: string): string {
  try {
    const d = new Date(time);
    const pad = (n: number) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  } catch {
    return time;
  }
}

export default function LogsPage() {
  const { t } = useI18n();
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [connected, setConnected] = useState(false);
  const [paused, setPaused] = useState(false);
  const [autoTail, setAutoTail] = useState(true);
  const [level, setLevel] = useState<Level>("all");
  const [search, setSearch] = useState("");
  const [connError, setConnError] = useState("");

  const esRef = useRef<EventSource | null>(null);
  const logContainerRef = useRef<HTMLDivElement>(null);
  const pausedRef = useRef(false);
  const levelRef = useRef<Level>("all");

  const appendLog = useCallback((entry: LogEntry) => {
    setLogs((prev) => {
      const next = [...prev, entry];
      return next.length > MAX_LOGS ? next.slice(-MAX_LOGS) : next;
    });
  }, []);

  const connect = useCallback(() => {
    esRef.current?.close();
    const params = new URLSearchParams();
    if (levelRef.current !== "all") params.set("level", levelRef.current);
    const es = new EventSource(eventStreamURL("/logs/stream", params));
    esRef.current = es;

    es.onopen = () => {
      setConnected(true);
      setConnError("");
    };
    es.onerror = () => {
      setConnected(false);
      setConnError(t("连接中断，正在尝试重连…"));
    };
    const handle = (ev: MessageEvent<string>) => {
      if (pausedRef.current) return;
      try {
        appendLog(JSON.parse(ev.data) as LogEntry);
      } catch {
        /* 忽略无法解析的日志帧 */
      }
    };
    es.onmessage = handle;
    es.addEventListener("log", handle);
  }, [appendLog]);

  const loadHistory = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api<LogEntry[] | { logs?: LogEntry[] }>("/logs/history?lines=500");
      const list = Array.isArray(res) ? res : (res?.logs ?? []);
      setLogs(list.slice(-MAX_LOGS));
    } catch {
      /* 历史回填失败不阻塞实时流 */
    } finally {
      setLoading(false);
    }
  }, []);

  // 挂载：回填历史并建立实时流；卸载：断开。
  useEffect(() => {
    pausedRef.current = false;
    void (async () => {
      await loadHistory();
      if (!pausedRef.current) connect();
    })();
    return () => {
      esRef.current?.close();
      esRef.current = null;
    };
  }, [connect, loadHistory]);

  // 级别变化：更新服务端过滤并（未暂停时）重连。
  useEffect(() => {
    levelRef.current = level;
    if (!pausedRef.current) connect();
  }, [level, connect]);

  // 自动追尾：新日志到达且未暂停时滚动到底部。
  useEffect(() => {
    if (!autoTail || paused) return;
    const el = logContainerRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [logs, autoTail, paused]);

  const togglePause = useCallback(() => {
    const next = !pausedRef.current;
    pausedRef.current = next;
    setPaused(next);
    if (next) {
      esRef.current?.close();
      setConnected(false);
    } else {
      connect();
    }
  }, [connect]);

  const clearLogs = useCallback(() => setLogs([]), []);

  const filtered = useMemo(() => {
    let list = logs;
    if (level !== "all") {
      list = list.filter((e) => e.level.toLowerCase() === level.toLowerCase());
    }
    if (search.trim()) {
      const q = search.toLowerCase();
      list = list.filter(
        (e) =>
          e.message.toLowerCase().includes(q) ||
          (e.caller ?? "").toLowerCase().includes(q) ||
          fieldsText(e.fields).toLowerCase().includes(q),
      );
    }
    return list;
  }, [logs, level, search]);

  const exportLogs = useCallback(() => {
    const text = filtered
      .map((v) => {
        const time = new Date(v.time).toLocaleString();
        const fields = v.fields ? ` ${fieldsText(v.fields)}` : "";
        return `[${time}] ${v.level.toUpperCase().padEnd(5)} ${v.caller ?? ""} ${v.message}${fields}`;
      })
      .join("\n");
    const blob = new Blob([text], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `logs-${new Date().toISOString().slice(0, 10)}.txt`;
    a.click();
    URL.revokeObjectURL(url);
    message.success(t("已导出日志"));
  }, [filtered]);

  return (
    <div className="max-w-7xl mx-auto">
      <PageHeader
        title={t("实时日志")}
        subtitle={t("查看系统运行日志，支持过滤和搜索")}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Button
              onClick={togglePause}
              variant={paused ? "success" : "warning"}
              className="!border-0 flex-1 justify-center sm:flex-none"
              icon={paused ? <PlayRegular /> : <PauseRegular />}
            >
              {paused ? t("继续") : t("暂停")}
            </Button>
            <Button onClick={clearLogs} className="!border-0 flex-1 justify-center sm:flex-none" icon={<DeleteRegular />}>
              {t("清空")}
            </Button>
            <Button onClick={exportLogs} variant="primary" className="!border-0 flex-1 justify-center sm:flex-none" icon={<ArrowDownloadRegular />}>
              {t("导出")}
            </Button>
          </div>
        }
      />

      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 mb-4">
        <div className="flex items-center gap-2">
          <span
            className={cx("w-2 h-2 rounded-full", connected ? "bg-green-500 animate-pulse" : "bg-red-500")}
          />
          <span className="text-sm text-gray-500">{connected ? t("已连接") : t("未连接")}</span>
        </div>
        <span className="text-sm text-gray-400">{logs.length} {t("条日志")}</span>
        {!connected && connError ? (
          <span className="text-sm text-red-500 truncate" title={connError}>
            {connError}
          </span>
        ) : null}
        <div className="hidden sm:block flex-1" />
        <label className="flex items-center gap-2">
          <Switch checked={autoTail} onChange={setAutoTail} ariaLabel={t("自动追尾")} />
          <span className="text-sm text-gray-500 dark:text-gray-400">{t("自动追尾")}</span>
        </label>
      </div>

      <div className="ui-card p-4 mb-4">
        <div className="flex flex-col gap-3 sm:flex-row sm:flex-wrap sm:items-center">
          <Select
            value={level}
            onChange={(v) => setLevel(v as Level)}
            placeholder={t("日志级别")}
            className="w-full sm:w-40"
            options={LEVEL_OPTIONS.map((o) => ({ ...o, label: t(o.label) }))}
          />
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t("搜索日志内容...")}
            className="w-full sm:w-64"
            suffix={
              search ? (
                <button
                  type="button"
                  aria-label={t("清除搜索")}
                  onClick={() => setSearch("")}
                  className="flex items-center text-gray-400 transition-colors hover:text-gray-600 dark:hover:text-gray-300"
                >
                  <DismissRegular />
                </button>
              ) : undefined
            }
          />
          <span className="text-sm text-gray-400 sm:ml-auto">
            {t("显示")} {filtered.length} / {logs.length} {t("条")}
          </span>
        </div>
      </div>

      <LogRetentionCard />

      <div className="ui-card overflow-hidden">
        <div
          ref={logContainerRef}
          className="h-[60vh] min-h-[280px] overflow-auto font-mono text-sm bg-gray-900 dark:bg-black text-gray-100 p-4"
        >
          {filtered.length === 0 ? (
            <div className="text-gray-500 text-center py-8">
              {loading ? t("等待日志...") : connected ? t("等待日志...") : t("未连接到日志流")}
            </div>
          ) : null}
          {filtered.map((entry, i) => (
            <div key={i} className="py-0.5 hover:bg-white/5 px-2 -mx-2 rounded whitespace-nowrap">
              <span className="text-gray-500">[{displayTime(entry.time)}]</span>
              <span className={cx("font-bold ml-1.5", levelColor(entry.level))}>
                {entry.level.toUpperCase()}
              </span>
              <span
                className="text-indigo-400 inline-block max-w-48 truncate align-bottom ml-1.5"
                title={entry.caller ?? ""}
              >
                {entry.caller ?? ""}
              </span>
              <span className="text-gray-100 ml-1.5">{entry.message}</span>
              {entry.fields ? (
                <span className="text-amber-300/70 ml-1.5">{fieldsText(entry.fields)}</span>
              ) : null}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
