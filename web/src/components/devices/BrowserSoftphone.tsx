import { useEffect, useRef, useState } from "react";
import { api, apiMessage } from "../../api";
import type { VoWiFiCall } from "../../types";
import { Button, Input, message } from "../ui";
import { useI18n } from "../../lib/i18n";

interface CallsResponse { deviceId: string; transport: string; calls: VoWiFiCall[] }

export interface SoftphoneProps {
  deviceId: string;
  deviceName?: string;
  /** IMS is registered and the softphone can dial/answer. */
  ready: boolean;
  /** Human-readable reason shown when ready is false. */
  reason?: string;
}

// BrowserSoftphone is the phone page's softphone. Unlike the device overview
// variant it is ALWAYS visible: when IMS is not ready it explains why instead
// of disappearing, and dial/answer/hangup stay prominent.
export function BrowserSoftphone({ deviceId, deviceName, ready, reason }: SoftphoneProps) {
  const { t } = useI18n();
  const [number, setNumber] = useState("");
  const [calls, setCalls] = useState<VoWiFiCall[]>([]);
  const [busy, setBusy] = useState(false);
  const [audioStatus, setAudioStatus] = useState<"off" | "connecting" | "ready" | "error">("off");
  const previous = useRef(new Map<string, string>());

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
      if (name === "dial") setNumber("");
    } catch (error) {
      message.error(apiMessage(error));
    } finally {
      setBusy(false);
    }
  };

  const ringingIncoming = calls.find((call) => call.state === "ringing" && call.direction === "incoming");
  const liveCall = calls.find((call) => !["ended", "failed"].includes(call.state));

  return (
    <section className="ui-panel-muted p-4">
      <div className="mb-3 flex items-center justify-between">
        <div>
          <div className="text-xs font-bold uppercase tracking-wider text-gray-500">{t("浏览器软电话")}</div>
          <div className="mt-1 text-xs text-gray-400">
            {deviceName ? `${deviceName} · ` : ""}{t("VoWiFi IMS、浏览器音频与来电通知")}
          </div>
        </div>
        <span className={`rounded-full px-2 py-1 text-[10px] font-bold ${ready ? "bg-emerald-500/10 text-emerald-600" : "bg-orange-500/10 text-orange-500"}`}>
          {ready ? "IMS" : t("IMS 未就绪")}
        </span>
      </div>

      {!ready ? (
        <div className="mb-3 rounded-lg border border-orange-200 bg-orange-50/60 px-3 py-2 text-xs text-orange-700 dark:border-orange-500/30 dark:bg-orange-500/10 dark:text-orange-300">
          <div className="font-bold">{t("软电话暂不可用")}</div>
          <div className="mt-1">{reason || t("IMS 尚未注册，请先在设备页开启 VoWiFi 并等待注册完成。")}</div>
        </div>
      ) : activeMediaCall ? (
        <div className="mb-2 text-xs text-gray-500">
          {audioStatus === "ready" ? t("浏览器音频已连接") : audioStatus === "connecting" ? t("正在连接浏览器音频…") : audioStatus === "error" ? t("浏览器音频不可用，请检查麦克风权限") : ""}
        </div>
      ) : null}

      <div className="flex gap-2">
        <Input
          value={number}
          onChange={(event) => setNumber(event.target.value)}
          placeholder={t("电话号码")}
          className="min-w-0 flex-1"
          disabled={!ready}
          onKeyDown={(event) => { if (event.key === "Enter" && ready && number.trim() && !busy) void action("dial"); }}
        />
        <Button disabled={busy || !ready || !number.trim()} onClick={() => void action("dial")} variant="primary" size="large">
          {t("拨号")}
        </Button>
      </div>

      {!ready ? (
        <div className="mt-3 space-y-2">
          {calls.length === 0 ? <div className="text-xs text-gray-400">{t("当前没有通话")}</div> : null}
        </div>
      ) : (
        <div className="mt-3 space-y-2">
          {calls.length === 0 ? (
            <div className="text-xs text-gray-400">{t("当前没有通话")}</div>
          ) : (
            calls.map((call) => (
              <div key={call.id} className="flex items-center gap-2 rounded-lg border border-gray-200 px-3 py-2 text-sm dark:border-white/10">
                <div className="min-w-0 flex-1">
                  <div className="truncate font-semibold">{call.number || t("未知号码")}</div>
                  <div className="text-xs text-gray-500">
                    {call.direction === "incoming" ? t("来电") : t("去电")} · {call.state}
                    {call.codec ? ` · ${call.codec}` : ""}
                  </div>
                </div>
                {call.state === "ringing" && call.direction === "incoming" ? (
                  <Button size="large" variant="success" onClick={() => void action("answer", call.id)} disabled={busy}>
                    {t("接听")}
                  </Button>
                ) : null}
                {!["ended", "failed"].includes(call.state) ? (
                  <Button size="large" variant="danger" onClick={() => void action("hangup", call.id)} disabled={busy}>
                    {t("挂断")}
                  </Button>
                ) : null}
              </div>
            ))
          )}
          {ringingIncoming && !liveCall?.mediaReady ? (
            <div className="text-xs text-emerald-600">{t("有来电，点击接听开始通话")}</div>
          ) : null}
        </div>
      )}
    </section>
  );
}
