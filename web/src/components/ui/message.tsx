import { useEffect, useState } from "react";
import {
  CheckmarkCircleFilled,
  DismissCircleFilled,
  InfoFilled,
  WarningFilled,
} from "@fluentui/react-icons";
import { cx } from "../../lib/utils";

export type MessageKind = "success" | "error" | "warning" | "info";

interface MessageItem {
  id: number;
  kind: MessageKind;
  text: string;
  leaving?: boolean;
}

type Listener = (items: MessageItem[]) => void;

let seq = 1;
let items: MessageItem[] = [];
const listeners = new Set<Listener>();

function emit() {
  for (const listener of listeners) listener([...items]);
}

function push(kind: MessageKind, text: string, duration = 3000) {
  const id = seq++;
  items = [...items, { id, kind, text }];
  emit();
  window.setTimeout(() => dismiss(id), duration);
}

function dismiss(id: number) {
  items = items.map((item) => (item.id === id ? { ...item, leaving: true } : item));
  emit();
  window.setTimeout(() => {
    items = items.filter((item) => item.id !== id);
    emit();
  }, 200);
}

export const message = {
  success: (text: string, duration?: number) => push("success", text, duration),
  error: (text: string, duration?: number) => push("error", text, duration ?? 4000),
  warning: (text: string, duration?: number) => push("warning", text, duration),
  info: (text: string, duration?: number) => push("info", text, duration),
};

const ICONS: Record<MessageKind, typeof CheckmarkCircleFilled> = {
  success: CheckmarkCircleFilled,
  error: DismissCircleFilled,
  warning: WarningFilled,
  info: InfoFilled,
};

const TONE: Record<MessageKind, string> = {
  success: "text-green-500",
  error: "text-red-500",
  warning: "text-amber-500",
  info: "text-indigo-500",
};

export function MessageHost() {
  const [current, setCurrent] = useState<MessageItem[]>(items);
  useEffect(() => {
    const listener: Listener = (next) => setCurrent(next);
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  }, []);

  return (
    <div className="pointer-events-none fixed left-1/2 top-4 z-[10000] flex w-full max-w-md -translate-x-1/2 flex-col items-center gap-2 px-4">
      {current.map((item) => {
        const Icon = ICONS[item.kind];
        return (
          <div
            key={item.id}
            className={cx(
              "pointer-events-auto flex items-center gap-2 rounded-full border px-4 py-2.5 text-[13px] font-medium shadow-[0_12px_40px_rgba(0,0,0,0.12)] backdrop-blur-2xl transition-all duration-200",
              "border-white/60 bg-white/75 text-black dark:border-white/10 dark:bg-[#1c1c1e]/80 dark:text-white",
              item.leaving ? "-translate-y-2 opacity-0" : "translate-y-0 opacity-100",
            )}
            role="status"
          >
            <Icon className={cx("h-4.5 w-4.5 shrink-0 text-[18px]", TONE[item.kind])} />
            <span className="break-words">{item.text}</span>
          </div>
        );
      })}
    </div>
  );
}
