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
          DEFAULT: "#007AFF",
          dark: "#0A84FF",
        },
        apple: {
          blue: "#007AFF",
          "blue-dark": "#0A84FF",
          green: "#34C759",
          red: "#FF3B30",
          orange: "#FF9500",
          gray: "#8E8E93",
          grouped: "#F2F2F7",
          "grouped-dark": "#000000",
        },
        // Map leftover indigo-* classes to iOS system blue.
        indigo: {
          50: "#E8F1FF",
          100: "#D6E8FF",
          200: "#A8CDFF",
          300: "#7AB3FF",
          400: "#4C98FF",
          500: "#007AFF",
          600: "#0066D6",
          700: "#0055B3",
          800: "#004080",
          900: "#002B57",
          950: "#001633",
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
        "fade-slide-in": "fade-slide-in 0.3s cubic-bezier(0.4, 0, 0.2, 1)",
      },
    },
  },
  plugins: [],
};
