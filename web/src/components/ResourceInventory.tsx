import React, { useState } from "react";
import { Search, Server, AlertCircle, RefreshCw } from "lucide-react";
import type { ResourceItem } from "../types";

interface ResourceInventoryProps {
  resources: ResourceItem[];
  selectedResource: ResourceItem | null;
  onSelectResource: (res: ResourceItem) => void;
  loading: boolean;
  error: string | null;
  onRefresh: () => void;
}

export const ResourceInventory: React.FC<ResourceInventoryProps> = ({
  resources,
  selectedResource,
  onSelectResource,
  loading,
  error,
  onRefresh,
}) => {
  const [search, setSearch] = useState("");
  const [typeFilter, setTypeFilter] = useState("all");

  const filteredResources = resources.filter((r) => {
    const matchesSearch = search
      ? r.provider_id.toLowerCase().includes(search.toLowerCase())
      : true;
    const matchesType =
      typeFilter !== "all"
        ? r.resource_type.toLowerCase() === typeFilter.toLowerCase()
        : true;
    return matchesSearch && matchesType;
  });

  return (
    <div className="panel" style={{ height: "100%" }}>
      <div className="panel-header">
        <div className="panel-title">
          <Server size={18} style={{ color: "var(--accent-primary)" }} />
          <span>Discovered Resources</span>
          <span className="panel-badge">{filteredResources.length}</span>
        </div>
        <button
          className="btn btn-secondary"
          onClick={onRefresh}
          disabled={loading}
          style={{ padding: "4px 8px", fontSize: "0.75rem" }}
          title="Refresh resource inventory"
        >
          <RefreshCw size={12} className={loading ? "animate-spin" : ""} />
        </button>
      </div>

      <div className="search-filter-box">
        <div style={{ position: "relative", flex: 1 }}>
          <input
            type="text"
            className="input-field"
            placeholder="Search provider ID (e.g. backend)..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          <Search
            size={14}
            style={{
              position: "absolute",
              right: "10px",
              top: "11px",
              color: "var(--text-dim)",
            }}
          />
        </div>

        <select
          className="select-field"
          value={typeFilter}
          onChange={(e) => setTypeFilter(e.target.value)}
        >
          <option value="all">All Kinds</option>
          <option value="deployment">Deployment</option>
          <option value="pod">Pod</option>
          <option value="service">Service</option>
          <option value="configmap">ConfigMap</option>
          <option value="secret">Secret</option>
          <option value="persistentvolumeclaim">PVC</option>
          <option value="serviceaccount">ServiceAccount</option>
          <option value="ingress">Ingress</option>
          <option value="node">Node</option>
        </select>
      </div>

      {loading && (
        <div className="empty-state">
          <RefreshCw size={24} className="animate-spin" style={{ color: "var(--accent-primary)" }} />
          <p className="empty-state-desc">Loading infrastructure inventory...</p>
        </div>
      )}

      {error && !loading && (
        <div className="callout callout-danger">
          <AlertCircle size={18} style={{ color: "var(--status-danger)" }} />
          <div>
            <strong>Inventory Error:</strong> {error}
          </div>
        </div>
      )}

      {!loading && !error && filteredResources.length === 0 && (
        <div className="empty-state">
          <Server size={32} style={{ color: "var(--text-dim)" }} />
          <div className="empty-state-title">No Resources Discovered</div>
          <p className="empty-state-desc">
            No resources found matching the criteria. Click "Sync Now" to sweep the connected Kubernetes cluster.
          </p>
        </div>
      )}

      {!loading && !error && filteredResources.length > 0 && (
        <div className="resource-list">
          {filteredResources.map((res) => {
            const isSelected =
              selectedResource?.provider_id === res.provider_id &&
              selectedResource?.resource_type === res.resource_type;

            const typeClass = res.resource_type.toLowerCase();

            return (
              <div
                key={`${res.provider}-${res.resource_type}-${res.provider_id}`}
                className={`resource-card ${isSelected ? "selected" : ""}`}
                onClick={() => onSelectResource(res)}
              >
                <div className="resource-card-header">
                  <span className={`type-pill ${typeClass}`}>
                    {res.resource_type}
                  </span>
                  <span className="mono" style={{ fontSize: "0.7rem", color: "var(--text-dim)" }}>
                    {new Date(res.last_observed_at).toLocaleTimeString()}
                  </span>
                </div>
                <div className="resource-name mono">{res.provider_id}</div>
                <div className="resource-meta">
                  <span>Provider: {res.provider}</span>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
};
