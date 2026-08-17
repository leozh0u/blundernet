// The mark: a rook going over.
//
// Every chess site puts an upright piece in a square, usually a knight or a
// king, and they all read the same. This one is a rook mid-topple, because the
// site is called BlunderNet and a piece falling over is what a blunder looks
// like from the other side of the board. It is also the one joke chess players
// all know, the rook sacrifice, drawn straight rather than explained.
//
// One path, no gradients, and it still reads at 20 pixels because the
// silhouette is the whole idea.
export default function Logo({ size = 32 }) {
  return (
    <svg
      className="logo"
      width={size}
      height={size}
      viewBox="0 0 32 32"
      role="img"
      aria-label="BlunderNet"
    >
      <rect width="32" height="32" rx="4" className="logo-bg" />
      {/* The square it lands on, so the rook is falling rather than floating. */}
      <rect x="6" y="24" width="20" height="2.5" rx="1" className="logo-floor" />
      <g transform="translate(17 14.5) rotate(-50) scale(0.78) translate(-16 -14)">
        <path
          className="logo-piece"
          d="M9 6h3v2.2h2.5V6h3v2.2H20V6h3v5.4l-2.4 2.2 1 6.4 2.4 2.6v1.6H8v-1.6l2.4-2.6 1-6.4L9 11.4Z"
        />
      </g>
    </svg>
  )
}
