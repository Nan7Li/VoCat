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
          <div className="mx-auto mb-5 flex h-[72px] w-[72px] items-center justify-center rounded-[20px] bg-[#FDF8F2] shadow-[0_4px_20px_rgba(180,140,100,0.08)] ring-1 ring-[#E8D9C8] dark:bg-white/10 dark:ring-white/10">
            <BrandLogo className="h-12 w-12 text-[var(--color-primary)]" />
          </div>
          <h1 className="font-display text-[28px] font-semibold leading-none tracking-[-0.04em] text-[#2C2C2C] dark:text-[#F3EADF]">
            Halo
          </h1>
          <p className="mt-2 text-[14px] tracking-tight text-[#8A7A6A] dark:text-white/45">{t("高通模块专业测试工具")}</p>
        </div>

        <form onSubmit={submit} className="space-y-3">
          <label className="vocat-enter vocat-enter-2 halo-login-field flex items-center gap-3 px-4">
            <PersonRegular className="h-5 w-5 shrink-0 text-[#A08B7A] dark:text-white/35" />
            <input
              className="h-12 w-full bg-transparent text-[16px] tracking-tight text-[#2C2C2C] outline-none placeholder:text-[#A08B7A] dark:text-white dark:placeholder:text-white/30"
              placeholder={t("用户名")}
              type="text"
              autoComplete="username"
              value={username}
              onChange={(event) => setUsername(event.target.value)}
            />
          </label>
          <label className="vocat-enter vocat-enter-2 halo-login-field flex items-center gap-3 px-4">
            <LockClosedRegular className="h-5 w-5 shrink-0 text-[#A08B7A] dark:text-white/35" />
            <input
              className="h-12 w-full bg-transparent text-[16px] tracking-tight text-[#2C2C2C] outline-none placeholder:text-[#A08B7A] dark:text-white dark:placeholder:text-white/30"
              placeholder={t("密码")}
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
            />
          </label>

          <button
            type="submit"
            disabled={working}
            className="vocat-enter vocat-enter-3 flex h-[50px] w-full items-center justify-center rounded-full bg-[var(--color-primary)] text-[17px] font-semibold text-[var(--color-on-primary)] shadow-[0_8px_22px_rgb(var(--color-primary-rgb)/0.32)] transition-transform duration-[180ms] ease-[cubic-bezier(0.32,0.72,0,1)] active:scale-[0.97] disabled:cursor-not-allowed disabled:opacity-70"
          >
            {t("登录")}
          </button>
        </form>

        <p className="vocat-enter vocat-enter-4 mt-8 text-center text-[12px] leading-relaxed tracking-tight text-black/30 dark:text-white/30">
          Halo 1.1.5
          <br />
          {t("基于 VoCat，原作者 Vocat Project Authors")}
        </p>
      </div>
    </div>
  );
}
