export function BrandLogo({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 64 64"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      aria-hidden="true"
    >
      <circle cx="32" cy="32" r="27" stroke="currentColor" strokeWidth="2.4" opacity="0.22" />
      <circle cx="32" cy="32" r="19.5" stroke="currentColor" strokeWidth="2.8" opacity="0.5" />
      <circle cx="32" cy="32" r="11.5" stroke="currentColor" strokeWidth="3.2" />
      <circle cx="32" cy="32" r="5.2" fill="currentColor" />
    </svg>
  );
}
