/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  darkMode: "class",
  theme: {
    extend: {
      fontFamily: {
        sans: [
          "-apple-system",
          "BlinkMacSystemFont",
          "SF Pro Text",
          "SF Pro Display",
          "SF Pro",
          "PingFang SC",
          "Hiragino Sans GB",
          "Helvetica Neue",
          "Segoe UI",
          "system-ui",
          "sans-serif",
        ],
        display: [
          "-apple-system",
          "BlinkMacSystemFont",
          "SF Pro Display",
          "SF Pro Text",
          "SF Pro",
          "PingFang SC",
          "Hiragino Sans GB",
          "Helvetica Neue",
          "system-ui",
          "sans-serif",
        ],
        mono: [
          "SF Mono",
          "ui-monospace",
          "SFMono-Regular",
          "Menlo",
          "Consolas",
          "monospace",
        ],
      },
      colors: {
        brand: {
          DEFAULT: "rgb(var(--color-primary-rgb) / 1)",
          dark: "var(--color-primary-hover)",
        },
        apple: {
          blue: "var(--color-primary)",
          "blue-dark": "var(--color-primary-hover)",
          green: "#34C759",
          red: "#FF3B30",
          orange: "var(--color-primary)",
          gray: "#8A7A6A",
          grouped: "#F7F4EF",
          "grouped-dark": "#1A1610",
        },
        // Old indigo/sky brand classes follow the live accent.
        indigo: {
          50: "rgb(var(--color-primary-rgb) / 0.08)",
          100: "rgb(var(--color-primary-rgb) / 0.14)",
          200: "rgb(var(--color-primary-rgb) / 0.28)",
          300: "rgb(var(--color-primary-rgb) / 0.45)",
          400: "rgb(var(--color-primary-rgb) / 0.7)",
          500: "rgb(var(--color-primary-rgb) / 1)",
          600: "var(--color-primary-hover)",
          700: "var(--color-primary-active)",
          800: "var(--color-primary-ink)",
          900: "var(--color-primary-ink)",
          950: "var(--color-primary-ink)",
        },
        sky: {
          50: "rgb(var(--color-primary-rgb) / 0.08)",
          100: "rgb(var(--color-primary-rgb) / 0.14)",
          200: "rgb(var(--color-primary-rgb) / 0.28)",
          300: "rgb(var(--color-primary-rgb) / 0.45)",
          400: "rgb(var(--color-primary-rgb) / 0.7)",
          500: "rgb(var(--color-primary-rgb) / 1)",
          600: "var(--color-primary-hover)",
          700: "var(--color-primary-active)",
          800: "var(--color-primary-ink)",
          900: "var(--color-primary-ink)",
        },
      },
      keyframes: {
        // VoHive overrides the default pulse to a slow scale+fade (glow blobs)
        pulse: {
          "0%, 100%": { opacity: "0.3", transform: "scale(1)" },
          "50%": { opacity: "0.6", transform: "scale(1.1)" },
        },
        "loader-shimmer": {
          "0%": { transform: "translate(-60%)" },
          "100%": { transform: "translate(60%)" },
        },
        "fade-slide-in": {
          from: { opacity: "0", transform: "translateY(20px)" },
          to: { opacity: "1", transform: "translateY(0)" },
        },
      },
      animation: {
        "pulse-slow": "pulse 8s cubic-bezier(0.4, 0, 0.6, 1) infinite",
        "loader-shimmer": "loader-shimmer 1.05s ease-in-out infinite",
        "fade-slide-in": "fade-slide-in 0.32s cubic-bezier(0.22, 1, 0.36, 1)",
      },
    },
  },
  plugins: [],
};
