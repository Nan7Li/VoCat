import { flushSync } from "react-dom";

export function prefersReducedMotion() {
  return typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

export function withViewTransition(update: () => void) {
  const start = document.startViewTransition?.bind(document);
  if (!start || prefersReducedMotion()) {
    update();
    return;
  }
  start(() => {
    flushSync(update);
  });
}
