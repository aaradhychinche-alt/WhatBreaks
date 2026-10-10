import React, { useState } from "react";
import { RefreshCw, Shield, Layers, CheckCircle2, AlertTriangle } from "lucide-react";
import type { DiscoverySyncResponse } from "../types";
import { syncDiscovery } from "../api/client";

interface HeaderProps {
  workspaceId: string;
  onWorkspaceChange: (ws: string) => void;
  onSyncComplete: () => void;
  clusterId?: string;
}

export const Header: React.FC<HeaderProps> = ({
  workspaceId,
  onWorkspaceChange,
  onSyncComplete,
  clusterId = "k8s-cluster",
}) => {
  const [syncing, setSyncing] = useState(false);
  const [syncResult, setSyncResult] = useState<DiscoverySyncResponse | null>(null);
  const [syncError, setSyncError] = useState<string | null>(null);

  const handleSync = async () => {
    setSyncing(true);
    setSyncError(null);
    setSyncResult(null);
    try {
      const res = await syncDiscovery(workspaceId);
      setSyncResult(res);
      onSyncComplete();
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : "Sync failed";
      setSyncError(message);
    } finally {
      setSyncing(false);
    }
  };

  return (
    <header className="top-bar">
      <div className="brand-section">
        <div className="brand-logo">
          <Layers size={22} />
        </div>
        <div className="brand-titles">
          <h1>
            WhatBreaks <span className="brand-tag">Impact v1</span>
          </h1>
          <p className="brand-subtitle">
            Infrastructure Dependency & Blast Radius Engine
          </p>
        </div>
      </div>

      <div className="environment-controls">
        <div className="env-badge">
          <span className={`pulse-dot ${syncError ? "error" : ""}`} />
          <span className="mono" style={{ color: "var(--text-muted)" }}>
            Cluster:
          </span>
          <strong className="mono" style={{ color: "var(--text-main)" }}>
            {clusterId}
          </strong>
        </div>

        <div className="env-badge">
          <Shield size={14} style={{ color: "var(--accent-primary)" }} />
          <span className="mono" style={{ color: "var(--text-muted)" }}>
            Workspace:
          </span>
          <input
            type="text"
            className="input-field mono"
            value={workspaceId}
            onChange={(e) => onWorkspaceChange(e.target.value)}
            style={{ width: "220px", padding: "2px 8px", fontSize: "0.75rem" }}
            title="Workspace UUID"
          />
        </div>

        <button
          className="btn btn-primary"
          onClick={handleSync}
          disabled={syncing}
          title="Run read-only Kubernetes collection and state reconciliation"
        >
          <RefreshCw size={14} className={syncing ? "animate-spin" : ""} />
          {syncing ? "Syncing..." : "Sync Now"}
        </button>
      </div>

      {syncResult && (
        <div
          className="callout callout-info"
          style={{
            position: "fixed",
            bottom: "24px",
            right: "24px",
            zIndex: 1000,
            boxShadow: "0 8px 30px rgba(0,0,0,0.5)",
          }}
        >
          <CheckCircle2 size={18} style={{ color: "var(--status-success)" }} />
          <div>
            <strong>Discovery Synced:</strong> {syncResult.resources_created} created,{" "}
            {syncResult.resources_updated} updated ({syncResult.resources_total} total) in{" "}
            {syncResult.duration_ms}ms.
          </div>
        </div>
      )}

      {syncError && (
        <div
          className="callout callout-danger"
          style={{
            position: "fixed",
            bottom: "24px",
            right: "24px",
            zIndex: 1000,
            boxShadow: "0 8px 30px rgba(0,0,0,0.5)",
          }}
        >
          <AlertTriangle size={18} style={{ color: "var(--status-danger)" }} />
          <div>
            <strong>Sync Error:</strong> {syncError}
          </div>
        </div>
      )}
    </header>
  );
};
