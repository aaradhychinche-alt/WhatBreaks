import React, { useState, useEffect } from "react";
import { Zap, Trash2, Edit3, Sliders, RefreshCw, Play } from "lucide-react";
import type { ChangeType, ResourceItem } from "../types";

interface ChangeProposalFormProps {
  selectedResource: ResourceItem | null;
  onAnalyze: (
    changeType: ChangeType,
    direction: "incoming" | "outgoing",
    maxDepth: number,
    details: string
  ) => void;
  analyzing: boolean;
}

export const ChangeProposalForm: React.FC<ChangeProposalFormProps> = ({
  selectedResource,
  onAnalyze,
  analyzing,
}) => {
  const [changeType, setChangeType] = useState<ChangeType>("DELETE");
  const [direction, setDirection] = useState<"incoming" | "outgoing">("incoming");
  const [depth, setDepth] = useState<number>(3);
  const [details, setDetails] = useState<string>("");

  // Auto-clamp depth policy for UPDATE and REPLACE
  useEffect(() => {
    if (changeType === "UPDATE" || changeType === "REPLACE") {
      setDepth(1);
    } else if (depth === 1) {
      setDepth(3);
    }
  }, [changeType]);

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!selectedResource) return;
    onAnalyze(changeType, direction, depth, details);
  };

  if (!selectedResource) {
    return (
      <div className="panel" style={{ opacity: 0.6 }}>
        <div className="empty-state" style={{ padding: "32px 16px" }}>
          <Zap size={28} style={{ color: "var(--text-dim)" }} />
          <div className="empty-state-title">Select a Resource</div>
          <p className="empty-state-desc">
            Choose a resource from the inventory on the left to configure a proposed change and analyze potential blast radius.
          </p>
        </div>
      </div>
    );
  }

  const isDepthLocked = changeType === "UPDATE" || changeType === "REPLACE";

  return (
    <form className="panel" onSubmit={handleSubmit}>
      <div className="panel-header">
        <div className="panel-title">
          <Zap size={18} style={{ color: "var(--accent-primary)" }} />
          <span>Propose Infrastructure Change</span>
        </div>
        <span className="mono" style={{ fontSize: "0.75rem", color: "var(--accent-primary)" }}>
          {selectedResource.provider_id}
        </span>
      </div>

      <div>
        <label style={{ fontSize: "0.78rem", fontWeight: 600, color: "var(--text-muted)", display: "block", marginBottom: "8px" }}>
          CHANGE OPERATION
        </label>
        <div className="change-type-grid">
          <button
            type="button"
            className={`change-type-btn delete ${changeType === "DELETE" ? "active" : ""}`}
            onClick={() => setChangeType("DELETE")}
          >
            <Trash2 size={16} />
            <span>DELETE</span>
          </button>

          <button
            type="button"
            className={`change-type-btn update ${changeType === "UPDATE" ? "active" : ""}`}
            onClick={() => setChangeType("UPDATE")}
          >
            <Edit3 size={16} />
            <span>UPDATE</span>
          </button>

          <button
            type="button"
            className={`change-type-btn scale ${changeType === "SCALE" ? "active" : ""}`}
            onClick={() => setChangeType("SCALE")}
          >
            <Sliders size={16} />
            <span>SCALE</span>
          </button>

          <button
            type="button"
            className={`change-type-btn replace ${changeType === "REPLACE" ? "active" : ""}`}
            onClick={() => setChangeType("REPLACE")}
          >
            <RefreshCw size={16} />
            <span>REPLACE</span>
          </button>
        </div>
      </div>

      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: "16px" }}>
        <div>
          <label style={{ fontSize: "0.75rem", fontWeight: 600, color: "var(--text-muted)", display: "block", marginBottom: "6px" }}>
            TRAVERSAL DIRECTION
          </label>
          <select
            className="select-field"
            style={{ width: "100%" }}
            value={direction}
            onChange={(e) => setDirection(e.target.value as "incoming" | "outgoing")}
          >
            <option value="incoming">Incoming (Dependents / Callers)</option>
            <option value="outgoing">Outgoing (Dependencies)</option>
          </select>
        </div>

        <div>
          <div style={{ display: "flex", justifyContent: "space-between", marginBottom: "6px" }}>
            <label style={{ fontSize: "0.75rem", fontWeight: 600, color: "var(--text-muted)" }}>
              MAX DEPTH: <strong className="mono" style={{ color: "var(--text-main)" }}>{depth}</strong>
            </label>
            {isDepthLocked && (
              <span className="mono" style={{ fontSize: "0.68rem", color: "var(--status-warning)" }}>
                (Ceiling 1 enforced)
              </span>
            )}
          </div>
          <input
            type="range"
            min="1"
            max={isDepthLocked ? "1" : "5"}
            value={depth}
            disabled={isDepthLocked}
            onChange={(e) => setDepth(parseInt(e.target.value, 10))}
            style={{ width: "100%", accentColor: "var(--accent-primary)", cursor: isDepthLocked ? "not-allowed" : "pointer" }}
          />
        </div>
      </div>

      <div>
        <label style={{ fontSize: "0.75rem", fontWeight: 600, color: "var(--text-muted)", display: "block", marginBottom: "6px" }}>
          OPTIONAL CHANGE DETAILS
        </label>
        <input
          type="text"
          className="input-field mono"
          placeholder="e.g. updating database replica endpoint..."
          value={details}
          onChange={(e) => setDetails(e.target.value)}
        />
      </div>

      <button
        type="submit"
        className="btn btn-primary"
        disabled={analyzing}
        style={{ padding: "10px 18px", fontSize: "0.9rem", fontWeight: 600 }}
      >
        <Play size={15} className={analyzing ? "animate-spin" : ""} />
        {analyzing ? "Computing Blast Radius..." : `Analyze ${changeType} Impact`}
      </button>
    </form>
  );
};
