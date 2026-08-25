import logo from '../assets/brand/logo-256.png'

/**
 * The full logo is detailed — shelf, globe, plant — and turns to mush below
 * about 40px, so the top bar gets a simplified glyph traced from it (roofline
 * plus open book) and the full raster is kept for large placements.
 */
export function BrandMark({ size = 26 }: { size?: number }) {
  return (
    <svg
      className="brand__mark"
      width={size}
      height={size}
      viewBox="0 0 48 48"
      fill="none"
      aria-hidden="true"
      style={{ width: size, height: size }}
    >
      <circle cx="24" cy="24" r="22.5" stroke="var(--v-400)" strokeWidth="2" />
      {/* roofline */}
      <path
        d="M11 23.5 24 12.5l13 11"
        stroke="var(--v-600)"
        strokeWidth="3"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      {/* open book */}
      <path
        d="M11 30.5c4.6-2.4 8.7-2.4 13 0 4.3-2.4 8.4-2.4 13 0v6c-4.6-2.4-8.7-2.4-13 0-4.3-2.4-8.4-2.4-13 0z"
        fill="var(--v-100)"
        stroke="var(--v-700)"
        strokeWidth="2"
        strokeLinejoin="round"
      />
      <path d="M24 30.5v6" stroke="var(--v-700)" strokeWidth="2" strokeLinecap="round" />
    </svg>
  )
}

export function BrandLogo({ className = 'auth__logo' }: { className?: string }) {
  return <img className={className} src={logo} alt="" width={88} height={88} />
}
