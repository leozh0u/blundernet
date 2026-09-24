// A small animated search tree shown while the engine is thinking. It is
// decorative, but it is honest about what the worker is doing: walking a
// tree from the root, evaluating a leaf, carrying the value back up.
export default function SearchTree() {
  const edges = [
    ['M60,14 L26,48', 0],
    ['M60,14 L60,48', 0.35],
    ['M60,14 L94,48', 0.7],
    ['M26,48 L14,84', 1.05],
    ['M26,48 L38,84', 1.4],
    ['M60,48 L60,84', 1.75],
    ['M94,48 L82,84', 2.1],
    ['M94,48 L106,84', 2.45],
  ]
  const nodes = [
    [60, 14, 0],
    [26, 48, 0.35],
    [60, 48, 0.7],
    [94, 48, 1.05],
    [14, 84, 1.4],
    [38, 84, 1.75],
    [60, 84, 2.1],
    [82, 84, 2.45],
    [106, 84, 2.8],
  ]
  return (
    <svg className="tree" viewBox="0 0 120 98" aria-hidden="true">
      {edges.map(([d, delay], i) => (
        <path key={i} d={d} className="tree-edge" style={{ animationDelay: `${delay}s` }} />
      ))}
      {nodes.map(([cx, cy, delay], i) => (
        <circle
          key={i}
          cx={cx}
          cy={cy}
          r={i === 0 ? 5 : 3.5}
          className="tree-node"
          style={{ animationDelay: `${delay}s` }}
        />
      ))}
    </svg>
  )
}
