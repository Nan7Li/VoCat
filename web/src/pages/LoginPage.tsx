import { useState, type FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { LockClosedRegular, PersonRegular } from "@fluentui/react-icons";
import { useAuth } from "../store/auth";
import { useI18n } from "../lib/i18n";
import { message } from "../components/ui/message";
import { BrandLogo } from "../components/shell/BrandLogo";

export default function LoginPage() {
  const { login } = useAuth();
  const { t } = useI18n();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [working, setWorking] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!username || !password) {
      message.warning(t("请输入用户名和密码"));
      return;
    }
    setWorking(true);
    await new Promise((resolve) => setTimeout(resolve, 600));
    const ok = await login(username, password);
    setWorking(false);
    if (ok) {
      message.success(t("欢迎回来"));
      const redirect = searchParams.get("redirect");
      navigate(redirect ? decodeURIComponent(redirect) : "/", { replace: true });
    } else {
      message.error(t("登录失败，请检查凭证"));
    }
  }

  return (
    <div className="relative flex h-full w-full items-center justify-center">
      <div className="relative w-full max-w-[400px] px-5">
        <div className="vocat-enter vocat-enter-1 mb-8 text-center">
          <div className="mx-auto mb-5 flex h-[72px] w-[72px] items-center justify-center rounded-[22px] bg-white/70 shadow-[0_10px_30px_rgba(0,0,0,0.08)] ring-1 ring-black/5 backdrop-blur-xl dark:bg-white/10 dark:ring-white/10">
            <BrandLogo className="h-12 w-12" />
          </div>
          <h1 className="font-display text-[34px] font-bold leading-none tracking-[-0.04em] text-black dark:text-white">
            vocat
          </h1>
          <p className="mt-2 text-[15px] tracking-tight text-black/40 dark:text-white/45">{t("高通模块专业测试工具")}</p>
        </div>

        <form onSubmit={submit} className="space-y-4">
          <div className="vocat-enter vocat-enter-2 overflow-hidden rounded-[22px] bg-white/80 shadow-[0_8px_28px_rgba(0,0,0,0.05)] ring-1 ring-black/[0.04] backdrop-blur-2xl dark:bg-[#1c1c1e]/80 dark:ring-white/10">
            <label className="flex items-center gap-3 px-4 py-3">
              <PersonRegular className="h-5 w-5 shrink-0 text-black/30 dark:text-white/35" />
              <input
                className="h-11 w-full bg-transparent text-[17px] tracking-tight text-black outline-none placeholder:text-black/30 dark:text-white dark:placeholder:text-white/30"
                placeholder={t("用户名")}
                type="text"
                autoComplete="username"
                value={username}
                onChange={(event) => setUsername(event.target.value)}
              />
            </label>
            <div className="mx-4 h-px bg-black/[0.08] dark:bg-white/10" />
            <label className="flex items-center gap-3 px-4 py-3">
              <LockClosedRegular className="h-5 w-5 shrink-0 text-black/30 dark:text-white/35" />
              <input
                className="h-11 w-full bg-transparent text-[17px] tracking-tight text-black outline-none placeholder:text-black/30 dark:text-white dark:placeholder:text-white/30"
                placeholder={t("密码")}
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
              />
            </label>
          </div>

          <button
            type="submit"
            disabled={working}
            className="vocat-enter vocat-enter-3 flex h-[50px] w-full items-center justify-center rounded-full bg-[#007AFF] text-[17px] font-semibold text-white shadow-[0_8px_22px_rgba(0,122,255,0.32)] transition-transform duration-150 active:scale-[0.96] disabled:cursor-not-allowed disabled:opacity-70 dark:bg-[#0A84FF]"
          >
            {working ? (
              <span className="h-5 w-5 animate-spin rounded-full border-2 border-white/30 border-t-white" />
            ) : (
              t("登录")
            )}
          </button>
        </form>

        <p className="vocat-enter vocat-enter-4 mt-8 text-center text-[12px] tracking-tight text-black/30 dark:text-white/30">vocat © 2026</p>
      </div>
    </div>
  );
}
