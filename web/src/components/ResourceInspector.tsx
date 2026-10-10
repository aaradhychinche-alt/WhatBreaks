import React, { useEffect, useState } from "react";
import { GitBranch, Info, ArrowRight, Layers, RefreshCw } from "lucide-react";
import type { ResourceDetailResponse, ResourceItem } from "../types";
import { getResourceDetail } from "../api/client";

interface ResourceInspectorProps {
  workspaceId: string;
  resource: ResourceItem;
}

export const ResourceInspector: React.FC<ResourceInspectorProps> = ({
  workspaceId,
  resource,
}) => {
  const [detail, setDetail] = useState<ResourceDetailResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let active = true;
    const fetchDetail = async () => {
      setLoading(true);
      setError(null);
      try {
        const res = await getResourceDetail(workspaceId, {
          provider: resource.provider,
          resource_type: resource.resource_type,
          provider_id: resource.provider_id,
        });
        if (active) {
          setDetail(res);
        }
      } catch (err: unknown) {
        if (active) {
          const message = err instanceof Error ? err.message : "Failed to load resource details";
          setError(message);
        }
      } finally {
        if (active) {
          setLoading(false);
        }
      }
    };

    fetchDetail();
    return () => {
      active = false;
    };
  }, [workspaceId, resource]);

  return (
    <div className="panel" style={{ background: "var(--bg-surface-elevated)" }}>
      <div className="panel-header">
        <div className="panel-title">
          <Info size={16} style={{ color: "var(--accent-primary)" }} />
          <span>Selected Resource Identity</span>
        </div>
        <span className="type-pill mono">{resource.resource_type}</span>
      </div>

      <div style={{ display: "flex", flexDirection: "column", gap: "10px" }}>
        <div>
          <div style={{ fontSize: "0.72rem", color: "var(--text-dim)", textTransform: "uppercase" }}>
            Provider ID
          </div>
          <div className="mono" style={{ fontSize: "0.85rem", color: "var(--text-main)", wordBreak: "break-all" }}>
            {resource.provider_id}
          </div>
        </div>

        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "10px" }}>
          <div>
            <div style={{ fontSize: "0.72rem", color: "var(--text-dim)", textTransform: "uppercase" }}>
              First Observed
            </div>
            <div className="mono" style={{ fontSize: "0.75rem", color: "var(--text-muted)" }}>
              {new Date(resource.first_observed_at).toLocaleString()}
            </div>
          </div>
          <div>
            <div style={{ fontSize: "0.72rem", color: "var(--text-dim)", textTransform: "uppercase" }}>
              Last Verified
            </div>
            <div className="mono" style={{ fontSize: "0.75rem", color: "var(--text-muted)" }}>
              {new Date(resource.last_observed_at).toLocaleString()}
            </div>
          </div>
        </div>
      </div>

      <div style={{ borderTop: "1px solid var(--border-subtle)", paddingTop: "14px" }}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: "8px" }}>
          <div style={{ fontSize: "0.82rem", fontWeight: 600, display: "flex", alignItems: "center", gap: "6px" }}>
            <GitBranch size={14} style={{ color: "var(--status-success)" }} />
            <span>Active Relationships</span>
          </div>
          {detail && (
            <span className="panel-badge">{detail.relationships.length}</span>
          )}
        </div>

        {loading && (
          <div style={{ display: "flex", alignItems: "center", gap: "8px", fontSize: "0.78rem", color: "var(--text-dim)", padding: "12px 0" }}>
            <RefreshCw size={14} className="animate-spin" />
            <span>Resolving graph edges...</span>
          </div>
        )}

        {error && (
          <div className="callout callout-danger" style={{ padding: "8px 12px", fontSize: "0.75rem" }}>
            {error}
          </div>
        )}

        {!loading && detail && detail.relationships.length === 0 && (
          <div style={{ fontSize: "0.78rem", color: "var(--text-dim)", padding: "8px 0" }}>
            No incoming or outgoing relationships found for this resource.
          </div>
        )}

        {!loading && detail && detail.relationships.length > 0 && (
          <div style={{ display: "flex", flexDirection: "column", gap: "6px", maxHeight: "200px", overflowY: "auto" }}>
            {detail.relationships.map((rel, idx) => (
              <div
                key={idx}
                style={{
                  padding: "8px 10px",
                  borderRadius: "var(--radius-sm)",
                  background: "var(--bg-app)",
                  border: "1px solid var(--border-subtle)",
                  fontSize: "0.75rem",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "space-between",
                  gap: "8px",
                }}
              >
                <div style={{ display: "flex", alignItems: "center", gap: "6px", overflow: "hidden" }}>
                  <span
                    className="mono"
                    style={{
                      fontSize: "0.68rem",
                      padding: "1px 5px",
                      borderRadius: "3px",
                      background: rel.direction === "OUTGOING" ? "rgba(56, 189, 248, 0.15)" : "rgba(168, 85, 247, 0.15)",
                      color: rel.direction === "OUTGOING" ? "var(--accent-primary)" : "var(--status-purple)",
                    }}
                  >
                    {rel.direction === "OUTGOING" ? "TO" : "FROM"}
                  </span>
                  <span className="path-edge-badge mono">{rel.kind}</span>
                  <ArrowRight size={12} style={{ color: "var(--text-dim)", flexShrink: 0 }} />
                  <span className="mono" style={{ textOverflow: "ellipsis", overflow: "hidden", whiteSpace: "nowrap" }}>
                    {rel.peer.provider_id}
                  </span>
                </div>

                {rel.evidence_ids && rel.evidence_ids.length > 0 && (
                  <span
                    className="mono"
                    style={{
                      fontSize: "0.65rem",
                      color: "var(--text-dim)",
                      flexShrink: 0,
                    }}
                    title={`Supporting Evidence IDs: ${rel.evidence_ids.join(", ")}`}
                  >
                    <Layers size={10} style={{ display: "inline", marginRight: "3px" }} />
                    {rel.evidence_ids.length}
                  </span>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
};
