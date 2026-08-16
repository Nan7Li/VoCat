import { useI18n } from "../../lib/i18n";

// 中英文切换按钮：使用「文/A」翻译图标。图标颜色跟随按钮 currentColor，
// 与 hover / dark 模式样式一致。英文模式 Hover 提示切中文，反之亦然。
// 选择经 useI18n 写入数据库（已登录时），未登录仅本会话生效。
export function LanguageSwitch() {
  const { lang, setLanguage, t } = useI18n();
  const next = lang === "zh" ? "en" : "zh";
  const label = lang === "zh" ? "Switch to English" : t("切换到中文");
  return (
    <button
      type="button"
      onClick={() => setLanguage(next)}
      aria-label={label}
      title={label}
      className="vocat-glass-btn"
    >
      <svg viewBox="0 0 1024 1024" className="h-5 w-5" fill="currentColor" aria-hidden="true">
        <path d="M554.666667 659.626667l-118.186667-116.906667a817.152 817.152 0 0 0 170.666667-304.213333h136.106666V144.213333h-324.266666V51.2H325.973333v93.013333H0v92.586667h520.106667a739.029333 739.029333 0 0 1-147.626667 249.6 715.136 715.136 0 0 1-107.52-155.733333H170.666667a810.752 810.752 0 0 0 138.666666 213.333333L73.386667 776.533333l66.133333 66.133334 232.96-232.96 144.64 144.64 37.546667-94.72z m261.973333-235.946667h-95.146667L512 981.333333h93.013333l52.053334-139.52h221.44L930.986667 981.333333H1024L816.64 423.68z m-122.026667 325.546667L768 547.84l75.52 201.386667h-148.906667z" />
      </svg>
    </button>
  );
}
