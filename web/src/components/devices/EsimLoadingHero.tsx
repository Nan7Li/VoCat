import { tl } from "../../lib/i18n";
import { Spinner } from "../ui";

// eSIM tab initial loading — centered spinner.
export function EsimLoadingHero() {
  return (
    <div className="ui-card flex items-center justify-center gap-3 p-10 text-gray-400 dark:text-gray-500 sm:p-16">
      <Spinner className="h-6 w-6 text-[var(--color-primary)]" />
      <span className="text-sm">{tl("正在加载 eSIM...")}</span>
    </div>
  );
}
