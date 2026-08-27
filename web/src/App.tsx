import { useEffect, useState, type ReactElement } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { AuthProvider, useAuth } from "./store/auth";
import { LanguageProvider } from "./lib/i18n";
import { AuthenticatedShell } from "./components/shell/AuthenticatedShell";
import { UnauthenticatedShell } from "./components/shell/UnauthenticatedShell";
import { Disclaimer } from "./components/Disclaimer";
import { MessageHost } from "./components/ui/message";
import { ConfirmHost } from "./components/ui/MessageBox";
import { LoadingScreen } from "./components/ui/LoadingScreen";
import { applyAccent, readStoredAccent } from "./lib/accent";
import {
  THEME_CHANGE_EVENT,
  THEME_KEY,
  readThemePreference,
  systemPrefersDark,
  writeThemePreference,
  type ThemePreference,
} from "./lib/theme";
import LoginPage from "./pages/LoginPage";
import DashboardPage from "./pages/DashboardPage";
import DevicesPage from "./pages/DevicesPage";
import PhonePage from "./pages/PhonePage";
import ProxyPage from "./pages/ProxyPage";
import ExportProxyPage from "./pages/ExportProxyPage";
import SmsPage from "./pages/SmsPage";
import AutomaticTasksPage from "./pages/AutomaticTasksPage";
import LogsPage from "./pages/LogsPage";
import SettingsPage from "./pages/SettingsPage";
import WireGuardPage from "./pages/WireGuardPage";
import ExtensionPage from "./pages/ExtensionPage";

const DISCLAIMER_KEY = "vocat_disclaimer_agreed_at";
const SEVEN_DAYS = 7 * 24 * 60 * 60 * 1000;

function useTheme() {
  const [preference, setPreference] = useState<ThemePreference>(readThemePreference);
  const [systemDark, setSystemDark] = useState(systemPrefersDark);
  const isDark = preference === "dark" || (preference === "system" && systemDark);

  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const query = window.matchMedia("(prefers-color-scheme: dark)");
    const update = (event: MediaQueryListEvent) => setSystemDark(event.matches);
    setSystemDark(query.matches);
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);

  useEffect(() => {
    const onThemeChange = (event: Event) => {
      const next = (event as CustomEvent<ThemePreference>).detail;
      setPreference(next === "light" || next === "dark" || next === "system" ? next : readThemePreference());
    };
    const onStorage = (event: StorageEvent) => {
      if (event.key === THEME_KEY) setPreference(readThemePreference());
    };
    window.addEventListener(THEME_CHANGE_EVENT, onThemeChange);
    window.addEventListener("storage", onStorage);
    return () => {
      window.removeEventListener(THEME_CHANGE_EVENT, onThemeChange);
      window.removeEventListener("storage", onStorage);
    };
  }, []);

  useEffect(() => {
    document.documentElement.classList.toggle("dark", isDark);
    document.documentElement.style.background = isDark ? "#1A1610" : "#F7F4EF";
    applyAccent(readStoredAccent(), false);
  }, [isDark]);
  return { isDark, toggle: () => writeThemePreference(isDark ? "light" : "dark") };
}

function RequireAuth({ children }: { children: ReactElement }) {
  const { ready, isAuthenticated } = useAuth();
  const location = useLocation();
  if (!ready) return <LoadingScreen />;
  if (!isAuthenticated) {
    const redirect = `${location.pathname}${location.search}${location.hash}`;
    return <Navigate to={`/login?redirect=${encodeURIComponent(redirect)}`} replace />;
  }
  return children;
}

function LoginLayout({ isDark, onToggleTheme }: { isDark: boolean; onToggleTheme: () => void }) {
  const { ready, isAuthenticated } = useAuth();
  if (ready && isAuthenticated) return <Navigate to="/" replace />;
  return <UnauthenticatedShell isDark={isDark} onToggleTheme={onToggleTheme} />;
}

function AppRoot() {
  const { isDark, toggle } = useTheme();
  const { ready, isAuthenticated } = useAuth();
  const [showDisclaimer, setShowDisclaimer] = useState(false);
  const [firstTime, setFirstTime] = useState(true);

  useEffect(() => {
    if (!isAuthenticated) {
      setShowDisclaimer(false);
      return;
    }
    let ts: number | null = null;
    try {
      const raw = localStorage.getItem(DISCLAIMER_KEY);
      ts = raw === null ? null : Number(raw);
    } catch {
      ts = null;
    }
    const expired = ts === null || Number.isNaN(ts) || Date.now() - ts >= SEVEN_DAYS;
    if (expired) {
      setFirstTime(ts === null || Number.isNaN(ts));
      setShowDisclaimer(true);
    }
  }, [isAuthenticated]);

  function agree() {
    try {
      localStorage.setItem(DISCLAIMER_KEY, String(Date.now()));
    } catch {
      /* ignore */
    }
    setShowDisclaimer(false);
  }

  if (!ready) return <LoadingScreen />;

  return (
    <div className="h-screen w-screen overflow-hidden bg-[#F7F4EF] font-sans text-[#2C2C2C] transition-colors duration-300 ease-[cubic-bezier(0.22,1,0.36,1)] dark:bg-[#17140F] dark:text-[#F4F0E8]">
      <Routes>
        <Route path="/login" element={<LoginLayout isDark={isDark} onToggleTheme={toggle} />}>
          <Route index element={<LoginPage />} />
        </Route>
        <Route
          path="/"
          element={
            <RequireAuth>
              <AuthenticatedShell isDark={isDark} onToggleTheme={toggle} />
            </RequireAuth>
          }
        >
          <Route index element={<DashboardPage />} />
          <Route path="phone" element={<PhonePage />} />
          <Route path="devices/*" element={<DevicesPage />} />
          <Route path="proxy" element={<ProxyPage />} />
          <Route path="wireguard" element={<WireGuardPage />} />
          <Route path="export-proxy" element={<ExportProxyPage />} />
          <Route path="sms" element={<SmsPage />} />
          <Route path="automatic-tasks" element={<AutomaticTasksPage />} />
          <Route path="extensions/:pluginId/:contributionId" element={<ExtensionPage />} />
          <Route path="logs" element={<LogsPage />} />
          <Route path="settings" element={<SettingsPage />} />
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
      {showDisclaimer && <Disclaimer firstTime={firstTime} onAgree={agree} />}
    </div>
  );
}

export default function App() {
  return (
    <AuthProvider>
      <LanguageProvider>
        <MessageHost />
        <ConfirmHost />
        <AppRoot />
      </LanguageProvider>
    </AuthProvider>
  );
}
