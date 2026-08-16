import { useEffect, useState } from "react";
import { api } from "../../api";
import type { SystemInfo } from "../../types";

export function VersionBadge() {
  const [version, setVersion] = useState<string>("");

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
    return () => {
      cancelled = true;
    };
  }, []);

  const label = version ? `v${version}` : "vdev";
  return (
    <span
      className="flex h-[34px] items-center justify-center rounded-full px-3 font-mono text-[11px] tracking-tight text-black/35 select-none dark:text-white/40"
      title={version ? `vocat v${version}` : "vocat dev build"}
    >
      {label}
    </span>
  );
}
