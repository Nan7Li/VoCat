import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "../../api";
import type { AutoUpdateSettings, SystemInfo } from "../../types";

export function VersionBadge() {
  const navigate = useNavigate();
  const [version, setVersion] = useState<string>("");
  const [hasUpdate, setHasUpdate] = useState(false);

  useEffect(() => {
    let cancelled = false;
    api<SystemInfo>("/system/info")
      .then((info) => {
        if (!cancelled) setVersion(info?.version ?? "");
      })
      .catch(() => {
        // A failed info probe leaves the badge at its dev fallback; the
        // shell still renders and other clusters are unaffected.
      });
    const checkUpdate = () => {
      api<AutoUpdateSettings>("/settings/auto-update")
        .then((info) => {
          if (cancelled) return;
          if (info?.lastAvailable) {
            setHasUpdate(true);
            return;
          }
          return api<{ available?: boolean }>("/system/update/check", { timeoutMs: 30000 }).then((check) => {
            if (!cancelled) setHasUpdate(!!check?.available);
          });
        })
        .catch(() => undefined);
    };
    void checkUpdate();
    const timer = window.setInterval(checkUpdate, 5 * 60 * 1000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, []);

  const label = version ? `v${version}` : "vdev";
  return (
    <button
      type="button"
      onClick={() => navigate("/settings")}
      className="relative flex h-[34px] items-center justify-center rounded-full px-3 font-mono text-[11px] tracking-tight text-black/35 transition-colors hover:bg-black/5 hover:text-black/60 dark:text-white/40 dark:hover:bg-white/10 dark:hover:text-white/70"
      title={hasUpdate ? "有新版本，点击打开设置更新" : version ? `Halo v${version}` : "Halo preview"}
    >
      {label}
      {hasUpdate ? <span className="absolute right-1 top-1 h-2 w-2 rounded-full bg-[#FF3B30]" aria-label="Update available" /> : null}
    </button>
  );
}
