import type { PointerEvent } from "react";
import { ChatMultipleRegular, DeleteRegular, SearchRegular } from "@fluentui/react-icons";
import { Button, EmptyState, Input, ListSkeleton, Select, type SelectOption } from "../ui";
import { cx } from "../../lib/utils";
import { timeLabel, type SmsThread } from "./smsText";
import { tl, useI18n } from "../../lib/i18n";

export interface ContactListProps {
  isMobile: boolean;
  isDesktop: boolean;
  selectedDevice: string;
  deviceOptions: SelectOption[];
  onSelectDevice: (id: string) => void;
  searchQuery: string;
  onSearchChange: (value: string) => void;
  loading: boolean;
  contacts: SmsThread[];
  activeKey: string;
  isUnread: (t: SmsThread) => boolean;
  deletingKey: string | null;
  canHover: boolean;
  onSelect: (key: string) => void;
  onDelete: (t: SmsThread) => void;
  onRowPointerDown: (t: SmsThread, e: PointerEvent) => void;
  onRowPointerMove: (e: PointerEvent) => void;
  onRowPointerEnd: (e: PointerEvent) => void;
}

export function ContactList(props: ContactListProps) {
  const { t } = useI18n();
  const {
    isMobile,
    isDesktop,
    selectedDevice,
    deviceOptions,
    onSelectDevice,
    searchQuery,
    onSearchChange,
    loading,
    contacts,
    activeKey,
    isUnread,
    deletingKey,
    canHover,
    onSelect,
    onDelete,
    onRowPointerDown,
    onRowPointerMove,
    onRowPointerEnd,
  } = props;

  return (
    <div className={cx("flex min-h-0 min-w-0 flex-col", isDesktop && "border-r border-gray-100 dark:border-white/10")}>
      <div className="border-b border-gray-100 p-4 dark:border-white/10">
        <div className="space-y-3">
          {isMobile && (
            <Select value={selectedDevice} onChange={onSelectDevice} options={deviceOptions} placeholder={t("选择设备")} />
          )}
          <Input
            value={searchQuery}
            onChange={(e) => onSearchChange(e.target.value)}
            placeholder={t("搜索联系人/内容")}
            prefix={<SearchRegular />}
          />
        </div>
      </div>
      {loading && contacts.length === 0 ? (
        <ListSkeleton rows={10} />
      ) : contacts.length === 0 ? (
        <div className="flex flex-1 items-center justify-center p-6">
          <EmptyState title={t("暂无会话")} subtitle={t("等待设备收到测试短信，或点击“发送测试短信”")} icon={<ChatMultipleRegular />} />
        </div>
      ) : (
        <div className="min-h-0 flex-1 overflow-auto">
          {contacts.map((t) => (
            <div
              key={t.key}
              className={cx(
                "sms-thread-item-shell border-b border-gray-100 dark:border-white/10",
                activeKey === t.key && "sms-thread-item-shell-active",
              )}
            >
              <div
                className={cx(
                  "sms-thread-row group flex items-start gap-2 px-4 py-3 transition-all",
                  activeKey === t.key ? "sms-thread-row-active" : "hover:bg-gray-50/60 dark:hover:bg-white/5",
                )}
                onPointerDown={(e) => onRowPointerDown(t, e)}
                onPointerMove={onRowPointerMove}
                onPointerUp={onRowPointerEnd}
                onPointerCancel={onRowPointerEnd}
              >
                <button type="button" className="min-w-0 flex-1 text-left" onClick={() => onSelect(t.key)}>
                  <div className="flex items-start justify-between gap-3">
<div className="min-w-0 flex-1">
  <div className="flex items-center gap-2">
    <div className="truncate font-semibold text-gray-900 dark:text-[var(--color-text)]">{t.displayName || t.peer}</div>
    {isUnread(t) ? <span className="h-2 w-2 shrink-0 rounded-full bg-[var(--color-primary)]" /> : null}
  </div>
  {t.displayName && t.displayName !== t.peer ? (
    <div className="mt-0.5 truncate text-[11px] text-gray-400 dark:text-[var(--color-text-muted)]">{t.peer}</div>
  ) : null}
  <div className="mt-1 truncate text-xs text-gray-500 dark:text-[var(--color-text-body)]">{t.lastMessage}</div>
</div>
<div className="sms-thread-meta shrink-0 text-right">
  <div className="font-mono text-[11px] text-gray-400 dark:text-[var(--color-text-weak)]">{timeLabel(t.lastTs)}</div>
  {t.localPhone || t.lastDeviceName ? (
    <div className="mt-1 max-w-[7rem] truncate text-[10px] text-gray-400 dark:text-[var(--color-text-weak)]">{t.localPhone || t.lastDeviceName}</div>
  ) : null}
</div>
                  </div>
                </button>
{!isMobile && (
  <Button
    variant="text"
    size="small"
    loading={deletingKey === t.key}
    aria-label={tl("删除与 ") + t.peer + tl(" 的对话")}
    title={tl("删除对话")}
    onClick={(e) => {
      e.stopPropagation();
      onDelete(t);
    }}
    className={cx(
      "sms-danger-ghost-btn sms-delete-trigger sms-thread-delete-btn",
      !canHover && "sms-delete-visible",
    )}
    icon={<DeleteRegular />}
  />
)}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
