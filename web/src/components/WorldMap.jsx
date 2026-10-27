import { useMemo } from 'react';
import land from '../data/land.json';
import { centroid } from '../data/centroids.js';

const W = 1000;
const H = 500;

// Equirectangular projection: uniform lon/lat mapping onto the viewBox.
function project(lon, lat) {
  return [((lon + 180) / 360) * W, ((90 - lat) / 180) * H];
}

function ringToPath(ring) {
  return ring
    .map(([lon, lat], i) => {
      const [x, y] = project(lon, lat);
      return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(' ') + ' Z';
}

function geometryPath(geometry) {
  if (!geometry) return '';
  if (geometry.type === 'Polygon') {
    return geometry.coordinates.map(ringToPath).join(' ');
  }
  if (geometry.type === 'MultiPolygon') {
    return geometry.coordinates.flat().map(ringToPath).join(' ');
  }
  return '';
}

// Spiral offset so several nodes in one country do not overlap.
function spiral(index) {
  if (index === 0) return [0, 0];
  const angle = index * 2.4;
  const radius = 7 + index * 3.2;
  return [Math.cos(angle) * radius, Math.sin(angle) * radius];
}

export default function WorldMap({ nodes }) {
  const landPath = useMemo(
    () => land.features.map((f) => geometryPath(f.geometry)).join(' '),
    [],
  );

  // Count nodes per country to spread their markers around the centroid.
  const perCountry = {};
  const markers = (nodes || []).map((n) => {
    const c = centroid(n.country_iso);
    const key = n.country_iso || 'unknown';
    const idx = perCountry[key] || 0;
    perCountry[key] = idx + 1;
    if (!c) {
      // Unknown geography: park along the bottom strip rather than dropping it.
      const [ox, oy] = spiral(idx);
      return { node: n, x: 30 + idx * 22, y: H - 24 + oy, placed: false, offset: [ox, oy] };
    }
    const [px, py] = project(c[0], c[1]);
    const [ox, oy] = spiral(idx);
    return { node: n, x: px + ox, y: py + oy, placed: true };
  });

  return (
    <div className="map-card">
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label="World map of dmesh nodes" className="map">
        <rect x="0" y="0" width={W} height={H} className="ocean" />
        <path d={landPath} className="land" />
        {markers.map(({ node, x, y }) => (
          <g key={node.mesh_address} className="marker">
            <title>{`${node.country || 'Unknown'} · ${
              node.tier === 'dc' ? 'datacenter' : 'residential'
            } · ${node.connectivity || 'unknown connectivity'}`}</title>
            <circle cx={x} cy={y} r="11" className={`halo ${node.tier}`} />
            <circle
              cx={x}
              cy={y}
              r={node.connectivity === 'relay-required' ? 3.2 : 4.6}
              className={`dot ${node.tier} ${node.connectivity === 'relay-required' ? 'relay' : ''}`}
            />
          </g>
        ))}
      </svg>
      <div className="legend">
        <span><i className="swatch res" /> residential</span>
        <span><i className="swatch dc" /> datacenter</span>
        <span><i className="swatch relay-sw" /> relay required (symmetric NAT / CGNAT)</span>
      </div>

      <style>{`
        .map-card {
          background: #fff;
          border: 1px solid var(--line);
          border-radius: var(--radius-md);
          padding: 16px;
          margin-bottom: 22px;
        }
        .map { width: 100%; height: auto; display: block; }
        .ocean { fill: #f5f8fb; }
        .land { fill: #e6ebf1; stroke: #d4dce5; stroke-width: 0.6; }
        .halo { fill: none; stroke-width: 1.4; opacity: 0.85; animation: map-pulse 2.6s ease-out infinite; }
        .halo.res { stroke: #34c759; }
        .halo.dc { stroke: #0071e3; }
        .dot.res { fill: #34c759; }
        .dot.dc { fill: #0071e3; }
        .dot.relay { fill: #fff; stroke-width: 1.8; }
        .dot.relay.res { stroke: #34c759; }
        .dot.relay.dc { stroke: #0071e3; }
        @keyframes map-pulse {
          0% { opacity: 0.9; transform: scale(0.55); transform-box: fill-box; transform-origin: center; }
          70% { opacity: 0; transform: scale(1.5); transform-box: fill-box; transform-origin: center; }
          100% { opacity: 0; }
        }
        .legend {
          display: flex; gap: 18px; flex-wrap: wrap;
          padding-top: 12px; font-size: 12.5px; color: var(--ink-soft);
        }
        .legend span { display: inline-flex; align-items: center; gap: 7px; }
        .swatch { width: 10px; height: 10px; border-radius: 50%; display: inline-block; }
        .swatch.res { background: #34c759; }
        .swatch.dc { background: #0071e3; }
        .swatch.relay-sw { background: #fff; border: 2px solid #8a8f98; width: 9px; height: 9px; }
      `}</style>
    </div>
  );
}
