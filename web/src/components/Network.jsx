import { useCallback, useEffect, useMemo, useState } from 'react';

const API = __API_BASE__;
const REFRESH_MS = 15000;

function short(addr) {
  if (!addr) return '';
  return `${addr.slice(0, 6)}…${addr.slice(-6)}`;
}

// Convert an ISO-3166 alpha-2 code to its flag emoji via regional indicators.
function flag(iso) {
  if (!iso || iso.length !== 2) return '🌐';
  const base = 0x1f1e6;
  return iso
    .toUpperCase()
    .split('')
    .map((c) => String.fromCodePoint(base + c.charCodeAt(0) - 65))
    .join('');
}

function ago(iso) {
  if (!iso) return '—';
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return '—';
  const secs = Math.max(0, Math.floor((Date.now() - then) / 1000));
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  return `${Math.floor(mins / 60)}h ago`;
}

async function getJSON(path) {
  const res = await fetch(API + path, { headers: { Accept: 'application/json' } });
  if (!res.ok) throw new Error(`${res.status}`);
  return res.json();
}

function Stat({ label, value }) {
  return (
    <div className="stat">
      <div className="stat-value">{value}</div>
      <div className="stat-label">{label}</div>
    </div>
  );
}

export default function Network() {
  const [stats, setStats] = useState(null);
  const [nodes, setNodes] = useState([]);
  const [error, setError] = useState(null);
  const [updated, setUpdated] = useState(null);

  const load = useCallback(async () => {
    try {
      const [s, n] = await Promise.all([
        getJSON('/api/v1/public/stats'),
        getJSON('/api/v1/nodes'),
      ]);
      setStats(s);
      setNodes(n.nodes || []);
      setUpdated(new Date());
      setError(null);
    } catch (e) {
      setError(e.message || 'unreachable');
    }
  }, []);

  useEffect(() => {
    load();
    const id = setInterval(load, REFRESH_MS);
    return () => clearInterval(id);
  }, [load]);

  // Distinct locations for the location chip row.
  const locations = useMemo(() => {
    const map = new Map();
    for (const n of nodes) {
      const key = n.country_iso || n.country || 'unknown';
      if (!map.has(key)) {
        map.set(key, { iso: n.country_iso, name: n.country || 'Unknown' });
      }
    }
    return [...map.values()];
  }, [nodes]);

  const online = stats ? stats.online_nodes : 0;

  return (
    <section id="network">
      <div className="wrap">
        <h2>Live network</h2>
        <p className="section-sub">
          Read directly from the testnet coordinator every {REFRESH_MS / 1000} seconds — node count
          and locations update in real time.
        </p>

        <div className="stat-row">
          <Stat label="Online nodes" value={stats ? stats.online_nodes : '—'} />
          <Stat label="Locations" value={stats ? stats.countries : '—'} />
          <Stat label="Residential" value={stats ? stats.residential : '—'} />
          <Stat label="Datacenter" value={stats ? stats.datacenter : '—'} />
        </div>

        {locations.length > 0 && (
          <div className="locations">
            {locations.map((l) => (
              <span className="loc-chip" key={l.iso || l.name}>
                <span className="loc-flag">{flag(l.iso)}</span>
                {l.name}
              </span>
            ))}
          </div>
        )}

        <div className="card table-card">
          <div className="table-head">
            <span className="badge live">
              <span className={`dot ${online > 0 ? 'pulse' : ''}`} />
              {online > 0 ? `${online} node${online === 1 ? '' : 's'} online` : 'offline'}
            </span>
            <span className="updated">
              {error ? `retrying (${error})` : updated ? `updated ${ago(updated.toISOString())}` : 'loading…'}
            </span>
          </div>

          {nodes.length === 0 ? (
            <div className="empty">
              No online nodes yet. Register one with the CLI and it appears here in real time.
            </div>
          ) : (
            <table>
              <thead>
                <tr>
                  <th>Status</th>
                  <th>Node</th>
                  <th>Location</th>
                  <th>Tier</th>
                  <th>Network</th>
                  <th>Last seen</th>
                </tr>
              </thead>
              <tbody>
                {nodes.map((n) => (
                  <tr key={n.mesh_address}>
                    <td>
                      <span className="status-dot pulse" title="online" />
                    </td>
                    <td className="mono-cell">{short(n.mesh_address)}</td>
                    <td>
                      <span className="country">
                        <span className="row-flag">{flag(n.country_iso)}</span>
                        {n.country || '—'}
                      </span>
                    </td>
                    <td>
                      <span className={`tier ${n.tier}`}>
                        {n.tier === 'dc' ? 'Datacenter' : 'Residential'}
                      </span>
                    </td>
                    <td>{n.asn_org || '—'}</td>
                    <td>{ago(n.last_seen)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      <style>{`
        .stat-row {
          display: grid;
          grid-template-columns: repeat(4, 1fr);
          gap: 16px;
          margin-bottom: 18px;
        }
        .stat {
          background: #fff;
          border: 1px solid var(--line);
          border-radius: var(--radius-md);
          padding: 22px;
          text-align: center;
        }
        .stat-value { font-size: 34px; font-weight: 700; letter-spacing: -0.02em; }
        .stat-label { color: var(--ink-soft); font-size: 13px; margin-top: 4px; }
        .locations { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 18px; }
        .loc-chip {
          display: inline-flex; align-items: center; gap: 7px;
          background: #fff; border: 1px solid var(--line);
          border-radius: 980px; padding: 6px 14px;
          font-size: 13px; font-weight: 500;
        }
        .loc-flag, .row-flag { font-size: 15px; }
        .table-card { padding: 0; overflow: hidden; }
        .table-head {
          display: flex; align-items: center; justify-content: space-between;
          padding: 16px 20px; border-bottom: 1px solid var(--line);
        }
        .updated { color: var(--ink-soft); font-size: 13px; }
        .empty { padding: 44px 20px; text-align: center; color: var(--ink-soft); }
        .mono-cell { font-family: ui-monospace, Menlo, monospace; font-size: 13px; }
        .country { display: inline-flex; align-items: center; gap: 8px; }
        .tier { font-size: 12px; font-weight: 600; padding: 3px 10px; border-radius: 980px; }
        .tier.res { background: rgba(52,199,89,.14); color: #1a7f37; }
        .tier.dc { background: rgba(0,113,227,.12); color: var(--accent); }
        .status-dot {
          width: 9px; height: 9px; border-radius: 50%;
          background: #34c759; display: inline-block;
        }
        .pulse { animation: pulse 2s ease-in-out infinite; }
        @keyframes pulse {
          0%, 100% { box-shadow: 0 0 0 0 rgba(52,199,89,.5); }
          50% { box-shadow: 0 0 0 6px rgba(52,199,89,0); }
        }
        @media (max-width: 760px) {
          .stat-row { grid-template-columns: repeat(2, 1fr); }
        }
      `}</style>
    </section>
  );
}
