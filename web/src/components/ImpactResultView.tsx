import React from "react";
import {
  AlertTriangle,
  GitBranch,
  ArrowRight,
  ShieldAlert,
  Info,
} from "lucide-react";
import type { ImpactAnswer } from "../types";

interface ImpactResultViewProps {
  answer: ImpactAnswer | null;
  error: string | null;
  loading: boolean;
}

export const ImpactResultView: React.FC<ImpactResultViewProps> = ({
  answer,
  error,
  loading,
}) => {
  if (loading) {
    return (
      <div className="panel" style={{ minHeight: "360px", justifyContent: "center" }}>
        <div className="empty-state">
          <div className="pulse-dot" style={{ width: "16px", height: "16px", marginBottom: "8px" }} />
          <div className="empty-state-title">Traversing Dependency Graph</div>
          <p className="empty-state-desc">
            Evaluating multi-hop propagation, enforcing boundary constraints, and synthesizing factual evidence...
          </p>
        </div>
      </div>
    );
  }

  if (error) {
    return (
      <div className="panel">
        <div className="callout callout-danger">
          <ShieldAlert size={20} style={{ color: "var(--status-danger)", flexShrink: 0 }} />
          <div>
            <strong style={{ display: "block", marginBottom: "4px" }}>
              Analysis Execution Failure
            </strong>
            <div>{error}</div>
          </div>
        </div>
      </div>
    );
  }

  if (!answer) {
    return (
      <div className="panel" style={{ minHeight: "360px", justifyContent: "center" }}>
        <div className="empty-state">
          <GitBranch size={36} style={{ color: "var(--text-dim)" }} />
          <div className="empty-state-title">No Analysis Executed Yet</div>
          <p className="empty-state-desc">
            Select a discovered resource and click "Analyze Impact" above to inspect the blast radius and supporting evidence.
          </p>
        </div>
      </div>
    );
  }

  const { summary, impacted_resources, paths, change_assessment } = answer;
  const hasImpacts = summary.impacted_count > 0;

  return (
    <div className="panel">
      <div className="panel-header">
        <div className="panel-title">
          <ShieldAlert size={18} style={{ color: "var(--status-warning)" }} />
          <span>Blast Radius Analysis Results</span>
        </div>
        {change_assessment && (
          <span
            className="mono"
            style={{
              fontSize: "0.75rem",
              fontWeight: 600,
              padding: "2px 8px",
              borderRadius: "4px",
              background: "var(--bg-surface-elevated)",
              border: "1px solid var(--border-subtle)",
              color: "var(--accent-primary)",
            }}
          >
            {change_assessment.change_type} EVALUATION
          </span>
        )}
      </div>

      {/* Summary KPI Cards */}
      <div className="summary-stats-grid">
        <div className="stat-card">
          <span className="stat-label">Total Impacted</span>
          <span className={`stat-value ${summary.impacted_count > 0 ? "danger" : ""}`}>
            {summary.impacted_count}
          </span>
        </div>
        <div className="stat-card">
          <span className="stat-label">Direct Dependents</span>
          <span className="stat-value warning">{summary.direct_count}</span>
        </div>
        <div className="stat-card">
          <span className="stat-label">Transitive Dependents</span>
          <span className="stat-value info">{summary.indirect_count}</span>
        </div>
        <div className="stat-card">
          <span className="stat-label">Traversed Depth</span>
          <span className="stat-value">{summary.max_depth}</span>
        </div>
      </div>

      {/* Change Assessment / Nature Banner */}
      {change_assessment && (
        <div
          className="callout callout-info"
          style={{ flexDirection: "column", gap: "6px" }}
        >
          <div style={{ display: "flex", alignItems: "center", gap: "8px", fontWeight: 600 }}>
            <Info size={16} />
            <span>Impact Nature & Behavioral Semantics</span>
          </div>
          <div style={{ fontSize: "0.82rem", color: "var(--text-main)" }}>
            {change_assessment.impact_nature}
          </div>
        </div>
      )}

      {/* Empty Result State or Impact List */}
      {!hasImpacts ? (
        <div className="callout callout-warning" style={{ alignItems: "flex-start" }}>
          <AlertTriangle size={20} style={{ color: "var(--status-warning)", flexShrink: 0, marginTop: "2px" }} />
          <div>
            <strong style={{ display: "block", marginBottom: "4px" }}>
              Zero Graph Dependents Identified
            </strong>
            <p style={{ fontSize: "0.8rem", color: "var(--text-main)" }}>
              No active downstream dependency paths were reached from target <code>{answer.target.provider_id}</code>.
              Note that zero identified graph impacts does NOT guarantee zero real-world operational risk.
            </p>
          </div>
        </div>
      ) : (
        <div>
          <div style={{ fontSize: "0.82rem", fontWeight: 600, color: "var(--text-muted)", marginBottom: "8px" }}>
            IMPACTED INFRASTRUCTURE RESOURCES ({impacted_resources.length})
          </div>
          <div style={{ display: "flex", flexDirection: "column", gap: "8px" }}>
            {impacted_resources.map((res, idx) => {
              const isDirect = res.depth === 1;
              const typeClass = res.resource.resource_type.toLowerCase();
              return (
                <div
                  key={idx}
                  style={{
                    padding: "10px 14px",
                    borderRadius: "var(--radius-sm)",
                    background: "var(--bg-surface-elevated)",
                    border: "1px solid var(--border-subtle)",
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "space-between",
                    gap: "12px",
                  }}
                >
                  <div style={{ display: "flex", alignItems: "center", gap: "10px" }}>
                    <span
                      className="mono"
                      style={{
                        fontSize: "0.72rem",
                        padding: "2px 6px",
                        borderRadius: "4px",
                        fontWeight: 600,
                        background: isDirect ? "rgba(244, 63, 94, 0.15)" : "rgba(56, 189, 248, 0.15)",
                        color: isDirect ? "var(--status-danger)" : "var(--accent-primary)",
                      }}
                    >
                      DEPTH {res.depth}
                    </span>
                    <span className={`type-pill ${typeClass}`}>
                      {res.resource.resource_type}
                    </span>
                    <span className="mono" style={{ fontSize: "0.82rem", color: "var(--text-main)" }}>
                      {res.resource.provider_id}
                    </span>
                  </div>

                  <div style={{ display: "flex", alignItems: "center", gap: "8px" }}>
                    {res.impact_type && (
                      <span
                        className="mono"
                        style={{
                          fontSize: "0.7rem",
                          fontWeight: 600,
                          padding: "2px 7px",
                          borderRadius: "4px",
                          background:
                            res.impact_type === "BREAKAGE_RISK"
                              ? "var(--status-danger-bg)"
                              : "var(--status-warning-bg)",
                          color:
                            res.impact_type === "BREAKAGE_RISK"
                              ? "var(--status-danger)"
                              : "var(--status-warning)",
                        }}
                      >
                        {res.impact_type}
                      </span>
                    )}
                    {res.impact_reason && (
                      <span style={{ fontSize: "0.75rem", color: "var(--text-dim)" }}>
                        {res.impact_reason}
                      </span>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      )}

      {/* Traversed Dependency Paths */}
      {paths && paths.length > 0 && (
        <div style={{ borderTop: "1px solid var(--border-subtle)", paddingTop: "14px" }}>
          <div style={{ fontSize: "0.82rem", fontWeight: 600, color: "var(--text-muted)", marginBottom: "8px" }}>
            VERIFIED TRAVERSAL PATHS ({paths.length})
          </div>
          <div className="path-container">
            {paths.map((p, pIdx) => (
              <div key={pIdx} className="path-row">
                <span className="mono" style={{ fontSize: "0.72rem", color: "var(--text-dim)", marginRight: "4px" }}>
                  #{pIdx + 1}
                </span>
                {p.resources.map((node, nIdx) => (
                  <React.Fragment key={nIdx}>
                    <span className="mono" style={{ fontWeight: 500, color: nIdx === 0 ? "var(--accent-primary)" : "var(--text-main)" }}>
                      {node.provider_id}
                    </span>
                    {nIdx < p.relationships.length && (
                      <>
                        <ArrowRight size={12} style={{ color: "var(--text-dim)" }} />
                        <span className="path-edge-badge mono">
                          {p.relationships[nIdx].kind}
                        </span>
                        <ArrowRight size={12} style={{ color: "var(--text-dim)" }} />
                      </>
                    )}
                  </React.Fragment>
                ))}
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Assumptions & Limitations Disclosures */}
      {change_assessment && (
        <div
          style={{
            borderTop: "1px solid var(--border-subtle)",
            paddingTop: "14px",
            display: "grid",
            gridTemplateColumns: "1fr 1fr",
            gap: "16px",
          }}
        >
          <div>
            <div style={{ fontSize: "0.75rem", fontWeight: 600, color: "var(--text-dim)", textTransform: "uppercase", marginBottom: "6px" }}>
              Engine Assumptions
            </div>
            <ul style={{ paddingLeft: "16px", fontSize: "0.78rem", color: "var(--text-muted)", display: "flex", flexDirection: "column", gap: "4px" }}>
              {change_assessment.assumptions.map((item, idx) => (
                <li key={idx}>{item}</li>
              ))}
            </ul>
          </div>

          <div>
            <div style={{ fontSize: "0.75rem", fontWeight: 600, color: "var(--text-dim)", textTransform: "uppercase", marginBottom: "6px" }}>
              Engine Limitations
            </div>
            <ul style={{ paddingLeft: "16px", fontSize: "0.78rem", color: "var(--text-muted)", display: "flex", flexDirection: "column", gap: "4px" }}>
              {change_assessment.limitations.map((item, idx) => (
                <li key={idx}>{item}</li>
              ))}
            </ul>
          </div>
        </div>
      )}
    </div>
  );
};
