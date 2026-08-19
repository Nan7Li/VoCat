import { useEffect, useRef, useState } from "react";
import { BackspaceRegular, CallEndRegular, CallRegular } from "@fluentui/react-icons";
import { api, apiMessage } from "../../api";
import type { VoWiFiCall } from "../../types";
import { Button, message } from "../ui";
import { useI18n } from "../../lib/i18n";
import { cx } from "../../lib/utils";

interface CallsResponse { deviceId: string; transport: string; calls: VoWiFiCall[] }

const PAD_KEYS: Array<{ value: string; letters?: string; longPress?: string }> = [
  { value: "1" },
  { value: "2", letters: "ABC" },
  { value: "3", letters: "DEF" },
  { value: "4", letters: "GHI" },
  { value: "5", letters: "JKL" },
  { value: "6", letters: "MNO" },
  { value: "7", letters: "PQRS" },
  { value: "8", letters: "TUV" },
  { value: "9", letters: "WXYZ" },
  { value: "*" },
  { value: "0", letters: "+", longPress: "+" },
  { value: "#" },
];

export interface SoftphoneProps {
  deviceId: string;
  deviceName?: string;
  /** IMS is registered and the softphone can dial/answer. */
  ready: boolean;
  /** Human-readable reason shown when ready is false. */
  reason?: string;
  /** `pad` is the phone page (keypad always on). `compact` is the device overview. */
  layout?: "pad" | "compact";
  /** When this changes, the dialer number is replaced (used for redial from history). */
  seedNumber?: string;
  seedToken?: number;
}

function sanitizeDial(value: string): string {
  const next = value.replace(/[^\d+*#]/g, "").slice(0, 32);
  if (!next.includes("+")) return next;
  return next[0] === "+" ? `+${next.slice(1).replace(/\+/g, "")}` : next.replace(/\+/g, "");
}

type CallPhase = "incoming" | "dialing" | "connecting" | "active";

function callPhase(call: VoWiFiCall): CallPhase {
  if (call.direction === "incoming" && call.state === "ringing") return "incoming";
  if (call.state === "active") return "active";
  if (call.state === "early_media") return "connecting";
  return "dialing";
}

function formatElapsed(from?: string | null): string {
  if (!from) return "00:00";
  const start = new Date(from).getTime();
  if (!Number.isFinite(start)) return "00:00";
  const total = Math.max(0, Math.floor((Date.now() - start) / 1000));
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
}

function CallSessionView({
  call,
  audioStatus,
  busy,
  onAnswer,
  onHangup,
}: {
  call: VoWiFiCall;
  audioStatus: "off" | "connecting" | "ready" | "error";
  busy: boolean;
  onAnswer: () => void;
  onHangup: () => void;
}) {
  const { t } = useI18n();
  const phase = callPhase(call);
  const [, setTick] = useState(0);
  useEffect(() => {
    if (phase !== "active" && phase !== "connecting") return;
    const timer = window.setInterval(() => setTick((value) => value + 1), 1000);
    return () => window.clearInterval(timer);
  }, [phase]);

  const title =
    phase === "incoming" ? t("来电") :
    phase === "dialing" ? t("拨号中") :
    phase === "connecting" ? t("正在接通") :
    t("已接通");
  const hint =
    phase === "incoming" ? t("点击接听开始通话") :
    phase === "dialing" ? t("正在呼叫对方，请稍候") :
    phase === "connecting" ? t("对方已振铃，正在建立通话") :
    audioStatus === "ready" ? t("浏览器音频已连接") :
    audioStatus === "connecting" ? t("正在连接浏览器音频…") :
    audioStatus === "error" ? t("浏览器音频不可用，请检查麦克风权限") :
    t("通话进行中");
  const ringClass =
    phase === "incoming" ? "bg-[var(--color-success)]/20 text-[var(--color-success)]" :
    phase === "active" ? "bg-[var(--color-success)]/15 text-[var(--color-success)]" :
    "bg-[var(--color-primary)]/18 text-[var(--color-primary)]";

  return (
    <div className="mx-auto flex w-full max-w-[320px] flex-col items-center py-2">
      <div className={cx("flex h-20 w-20 items-center justify-center rounded-full", ringClass, (phase === "incoming" || phase === "dialing") && "animate-pulse")}>
        <CallRegular className="h-10 w-10" />
      </div>
      <div className="mt-4 text-[13px] font-semibold tracking-wide text-[var(--color-primary)] dark:text-[var(--color-primary)]">
        {title}
      </div>
      <div className="mt-2 break-all text-center text-[26px] font-semibold tracking-tight text-[#2C2C2C] dark:text-[var(--color-text)]">
        {call.number || t("未知号码")}
      </div>
      {phase === "active" || phase === "connecting" ? (
        <div className="mt-1 font-mono text-sm tabular-nums text-[var(--color-success)]">
          {formatElapsed(call.answeredAt || call.startedAt)}
        </div>
      ) : null}
      <div className="mt-2 text-center text-xs text-gray-500 dark:text-[var(--color-text-muted)]">{hint}</div>
      <div className="mt-6 flex w-full items-center justify-center gap-4">
        {phase === "incoming" ? (
          <Button className="min-w-[120px]" size="large" variant="success" onClick={onAnswer} disabled={busy} icon={<CallRegular />}>
            {t("接听")}
          </Button>
        ) : null}
        <Button className="min-w-[120px]" size="large" variant="danger" onClick={onHangup} disabled={busy} icon={<CallEndRegular />}>
          {t("挂断")}
        </Button>
      </div>
    </div>
  );
}

// BrowserSoftphone is the phone page's softphone. Unlike the device overview
// variant it is ALWAYS visible: when IMS is not ready it explains why instead
// of disappearing, and dial/answer/hangup stay prominent.
export function BrowserSoftphone({ deviceId, deviceName, ready, reason, layout = "pad", seedNumber, seedToken }: SoftphoneProps) {
  const { t } = useI18n();
  const [number, setNumber] = useState("");
  const [calls, setCalls] = useState<VoWiFiCall[]>([]);
  const [busy, setBusy] = useState(false);
  const [audioStatus, setAudioStatus] = useState<"off" | "connecting" | "ready" | "error">("off");
  const [padOpen, setPadOpen] = useState(layout === "pad");
  const previous = useRef(new Map<string, string>());
  const longPressTimer = useRef<number | null>(null);
  const longPressFired = useRef(false);

  useEffect(() => {
    if (seedNumber) setNumber(sanitizeDial(seedNumber));
  }, [seedNumber, seedToken]);

  useEffect(() => {
    if (!ready) {
      setCalls([]);
      setAudioStatus("off");
      return;
    }
    let active = true;
    const load = async () => {
      try {
        const result = await api<CallsResponse>(`/devices/${encodeURIComponent(deviceId)}/calls`);
        if (!active) return;
        const next = result?.calls || [];
        for (const call of next) {
          const before = previous.current.get(call.id);
          if (call.state === "ringing" && call.direction === "incoming" && before !== "ringing") {
            if (typeof Notification !== "undefined") {
              if (Notification.permission === "granted") {
                new Notification(t("VoWiFi 来电"), { body: call.number || t("未知号码") });
              } else if (Notification.permission === "default") {
                void Notification.requestPermission();
              }
            }
          }
          previous.current.set(call.id, call.state);
        }
        setCalls(next);
      } catch {
        // The softphone keeps rendering when the call capability is offline.
      }
    };
    void load();
    const timer = window.setInterval(() => void load(), 2000);
    return () => { active = false; window.clearInterval(timer); };
  }, [deviceId, ready, t]);

  const activeMediaCall = calls.find((call) => call.mediaReady && ["active", "early_media"].includes(call.state));

  useEffect(() => {
    if (!ready || !activeMediaCall) {
      setAudioStatus("off");
      return;
    }
    let closed = false;
    let socket: WebSocket | undefined;
    let audioContext: AudioContext | undefined;
    let stream: MediaStream | undefined;
    let source: MediaStreamAudioSourceNode | undefined;
    let processor: ScriptProcessorNode | undefined;
    let muteNode: GainNode | undefined;
    let playbackTime = 0;
    setAudioStatus("connecting");

    const setup = async () => {
      try {
        stream = await navigator.mediaDevices.getUserMedia({ audio: true });
        if (closed) return;
        audioContext = new AudioContext();
        await audioContext.resume();
        const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
        socket = new WebSocket(`${protocol}//${window.location.host}/api/devices/${encodeURIComponent(deviceId)}/calls/media?call_id=${encodeURIComponent(activeMediaCall.id)}`);
        socket.binaryType = "arraybuffer";
        socket.onopen = () => { if (!closed) setAudioStatus("ready"); };
        socket.onerror = () => { if (!closed) setAudioStatus("error"); };
        socket.onmessage = (event) => {
          if (!audioContext || typeof event.data === "string") return;
          const bytes = new Uint8Array(event.data as ArrayBuffer);
          const samples = new Int16Array(bytes.buffer, bytes.byteOffset, Math.floor(bytes.byteLength / 2));
          const buffer = audioContext.createBuffer(1, samples.length, 8000);
          const channel = buffer.getChannelData(0);
          for (let index = 0; index < samples.length; index += 1) channel[index] = samples[index] / 32768;
          const player = audioContext.createBufferSource();
          player.buffer = buffer;
          player.connect(audioContext.destination);
          const startAt = Math.max(audioContext.currentTime, playbackTime);
          playbackTime = startAt + buffer.duration;
          player.start(startAt);
        };
        source = audioContext.createMediaStreamSource(stream);
        processor = audioContext.createScriptProcessor(2048, 1, 1);
        processor.onaudioprocess = (event) => {
          if (!socket || socket.readyState !== WebSocket.OPEN || !audioContext) return;
          const input = event.inputBuffer.getChannelData(0);
          const ratio = audioContext.sampleRate / 8000;
          const output = new Int16Array(Math.floor(input.length / ratio));
          for (let index = 0; index < output.length; index += 1) {
            const sample = Math.max(-1, Math.min(1, input[Math.floor(index * ratio)]));
            output[index] = sample < 0 ? sample * 32768 : sample * 32767;
          }
          socket.send(output.buffer);
        };
        source.connect(processor);
        muteNode = audioContext.createGain();
        muteNode.gain.value = 0;
        processor.connect(muteNode);
        muteNode.connect(audioContext.destination);
      } catch {
        if (!closed) setAudioStatus("error");
      }
    };
    void setup();
    return () => {
      closed = true;
      processor?.disconnect();
      muteNode?.disconnect();
      source?.disconnect();
      stream?.getTracks().forEach((track) => track.stop());
      socket?.close();
      void audioContext?.close();
    };
  }, [activeMediaCall?.id, activeMediaCall?.mediaReady, activeMediaCall?.state, deviceId, ready]);

  const action = async (name: "dial" | "answer" | "hangup", callId?: string) => {
    setBusy(true);
    try {
      await api(`/devices/${encodeURIComponent(deviceId)}/calls/${name}`, {
        method: "POST",
        body: name === "dial" ? { number: number.trim() } : { call_id: callId },
      });
    } catch (error) {
      message.error(apiMessage(error));
    } finally {
      setBusy(false);
    }
  };

  const appendDigit = (digit: string) => {
    setNumber((current) => sanitizeDial(current + digit));
  };

  const clearLongPress = () => {
    if (longPressTimer.current !== null) {
      window.clearTimeout(longPressTimer.current);
      longPressTimer.current = null;
    }
  };

  const startKey = (key: (typeof PAD_KEYS)[number]) => {
    longPressFired.current = false;
    clearLongPress();
    if (!key.longPress) return;
    longPressTimer.current = window.setTimeout(() => {
      longPressFired.current = true;
      appendDigit(key.longPress!);
    }, 420);
  };

  const endKey = (key: (typeof PAD_KEYS)[number]) => {
    clearLongPress();
    if (!longPressFired.current) appendDigit(key.value);
  };

  const liveCall = calls.find((call) => !["ended", "failed"].includes(call.state));
  const canDial = ready && !busy && number.trim().length >= 2 && !liveCall;

  const keypad = (
    <div className="phone-keypad mx-auto w-full max-w-[280px]">
      <div className="grid grid-cols-3 justify-items-center gap-x-4 gap-y-3">
        {PAD_KEYS.map((key) => (
          <button
            key={key.value}
            type="button"
            aria-label={key.longPress ? t("长按输入 +") : key.value}
            onPointerDown={() => startKey(key)}
            onPointerUp={() => endKey(key)}
            onPointerLeave={clearLongPress}
            onPointerCancel={clearLongPress}
            className="phone-key flex h-16 w-16 flex-col items-center justify-center rounded-full bg-white text-[#2C2C2C] shadow-[0_1px_0_rgba(180,140,100,0.12)] ring-1 ring-[#E8D9C8]/80 transition-[transform,background-color] duration-150 active:scale-95 dark:bg-[var(--color-card-2)] dark:text-[var(--color-text)] dark:ring-[var(--color-border)]"
          >
            <span className="text-[26px] font-medium leading-none tracking-tight">{key.value}</span>
            {key.letters ? <span className="mt-0.5 text-[9px] font-semibold tracking-[0.14em] text-[#8A7A6A] dark:text-white/45">{key.letters}</span> : <span className="mt-0.5 h-[11px]" />}
          </button>
        ))}
      </div>
      <div className="mt-5 flex items-center justify-center gap-10">
        <span className="h-14 w-14" aria-hidden="true" />
        <button
          type="button"
          disabled={!canDial}
          onClick={() => void action("dial")}
          aria-label={t("拨号")}
          title={!ready ? (reason || t("IMS 尚未注册，请先在设备页开启 VoWiFi 并等待注册完成。")) : t("拨号")}
          className="flex h-16 w-16 items-center justify-center rounded-full bg-[var(--color-primary)] text-white shadow-[0_8px_18px_rgb(var(--color-primary-rgb)/0.35)] transition-transform duration-150 active:scale-95 disabled:cursor-not-allowed disabled:shadow-none"
        >
          <CallRegular className="h-8 w-8" />
        </button>
        <button
          type="button"
          disabled={!number}
          onClick={() => setNumber((current) => current.slice(0, -1))}
          onContextMenu={(event) => { event.preventDefault(); setNumber(""); }}
          aria-label={t("退格")}
          title={t("长按或右键清空")}
          className={cx(
            "flex h-14 w-14 items-center justify-center rounded-full text-[#8A7A6A] transition-colors dark:text-white/50",
            number ? "hover:bg-black/5 dark:hover:bg-white/10" : "invisible",
          )}
        >
          <BackspaceRegular className="h-7 w-7" />
        </button>
      </div>
    </div>
  );

  return (
    <section className="ui-panel-muted p-4">
      <div className="mb-3 flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="text-xs font-bold uppercase tracking-wider text-gray-500">{t("浏览器软电话")}</div>
          <div className="mt-1 text-xs text-gray-400">
            {deviceName ? `${deviceName} · ` : ""}{t("VoWiFi IMS、浏览器音频与来电通知")}
          </div>
        </div>
        <span className={`shrink-0 rounded-full px-2 py-1 text-[10px] font-bold ${ready ? "bg-emerald-500/10 text-emerald-600" : "bg-orange-500/10 text-orange-500"}`}>
          {ready ? "IMS" : t("IMS 未就绪")}
        </span>
      </div>

      {!ready ? (
        <div className="mb-3 rounded-lg border border-orange-200 bg-orange-50/60 px-3 py-2 text-xs text-orange-700 dark:border-orange-500/30 dark:bg-orange-500/10 dark:text-orange-300">
          <div className="font-bold">{t("软电话暂不可用")}</div>
          <div className="mt-1">{reason || t("IMS 尚未注册，请先在设备页开启 VoWiFi 并等待注册完成。")}</div>
        </div>
      ) : null}

      {liveCall ? (
        <CallSessionView
          call={liveCall}
          audioStatus={audioStatus}
          busy={busy}
          onAnswer={() => void action("answer", liveCall.id)}
          onHangup={() => void action("hangup", liveCall.id)}
        />
      ) : (
        <>
          <div className="mb-3">
            <input
              value={number}
              onChange={(event) => setNumber(sanitizeDial(event.target.value))}
              placeholder={t("电话号码")}
              inputMode="tel"
              autoComplete="tel"
              aria-label={t("电话号码")}
              onKeyDown={(event) => { if (event.key === "Enter" && canDial) void action("dial"); }}
              className="w-full rounded-[12px] border border-[#E8D9C8] bg-[#FDF8F2] px-3 py-2 text-center text-[28px] font-semibold tracking-[0.04em] text-[#2C2C2C] outline-none placeholder:text-[#716A61] placeholder:tracking-normal dark:border-[var(--color-border)] dark:bg-[var(--color-input)] dark:text-[var(--color-text)] dark:placeholder:text-[var(--color-text-weak)]"
            />
          </div>

          {layout === "compact" && !padOpen ? (
            <div className="flex items-center justify-center gap-3">
              <Button type="button" onClick={() => setPadOpen(true)}>
                {t("拨号盘")}
              </Button>
              <button
                type="button"
                disabled={!canDial}
                onClick={() => void action("dial")}
                aria-label={t("拨号")}
                className="flex h-12 w-12 items-center justify-center rounded-full bg-[var(--color-primary)] text-white shadow-[0_6px_14px_rgb(var(--color-primary-rgb)/0.32)] transition-transform duration-150 active:scale-95 disabled:cursor-not-allowed disabled:shadow-none"
              >
                <CallRegular className="h-6 w-6" />
              </button>
            </div>
          ) : keypad}

          {layout === "compact" && padOpen ? (
            <div className="mt-3 text-center">
              <Button type="button" variant="text" onClick={() => setPadOpen(false)}>
                {t("收起拨号盘")}
              </Button>
            </div>
          ) : null}

          <div className="mt-4 text-center text-xs text-gray-400">{t("当前没有通话")}</div>
        </>
      )}
    </section>
  );
}
