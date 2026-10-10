import { useState, useEffect, useCallback } from "react";
import { Header } from "./components/Header";
import { ResourceInventory } from "./components/ResourceInventory";
import { ResourceInspector } from "./components/ResourceInspector";
import { ChangeProposalForm } from "./components/ChangeProposalForm";
import { ImpactResultView } from "./components/ImpactResultView";
import type {
  ChangeType,
  ImpactAnswer,
  ImpactRequest,
  ResourceItem,
} from "./types";
import {
  analyzeImpact,
  DEFAULT_WORKSPACE_ID,
  fetchCSRFToken,
  listResources,
} from "./api/client";

export default function App() {
  const [workspaceId, setWorkspaceId] = useState<string>(DEFAULT_WORKSPACE_ID);
  const [resources, setResources] = useState<ResourceItem[]>([]);
  const [selectedResource, setSelectedResource] = useState<ResourceItem | null>(null);
  const [loadingResources, setLoadingResources] = useState<boolean>(true);
  const [resourcesError, setResourcesError] = useState<string | null>(null);

  const [impactAnswer, setImpactAnswer] = useState<ImpactAnswer | null>(null);
  const [analyzing, setAnalyzing] = useState<boolean>(false);
  const [impactError, setImpactError] = useState<string | null>(null);

  // Initialize CSRF token
  useEffect(() => {
    fetchCSRFToken();
  }, []);

  const loadResources = useCallback(async (wsId: string) => {
    setLoadingResources(true);
    setResourcesError(null);
    try {
      const data = await listResources(wsId);
      setResources(data.resources);
      if (data.resources.length > 0 && !selectedResource) {
        setSelectedResource(data.resources[0]);
      }
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : "Failed to load resources";
      setResourcesError(message);
      setResources([]);
    } finally {
      setLoadingResources(false);
    }
  }, [selectedResource]);

  useEffect(() => {
    loadResources(workspaceId);
  }, [workspaceId, loadResources]);

  const handleAnalyze = async (
    changeType: ChangeType,
    direction: "incoming" | "outgoing",
    maxDepth: number,
    details: string
  ) => {
    if (!selectedResource) return;

    setAnalyzing(true);
    setImpactError(null);
    try {
      const req: ImpactRequest = {
        target: {
          provider: selectedResource.provider,
          resource_type: selectedResource.resource_type,
          provider_id: selectedResource.provider_id,
        },
        direction,
        max_depth: maxDepth,
        workspace_id: workspaceId,
        proposed_change: {
          change_type: changeType,
          details: details.trim() || undefined,
        },
      };

      const res = await analyzeImpact(req);
      setImpactAnswer(res);
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : "Impact analysis failed";
      setImpactError(message);
      setImpactAnswer(null);
    } finally {
      setAnalyzing(false);
    }
  };

  return (
    <div className="app-container">
      <Header
        workspaceId={workspaceId}
        onWorkspaceChange={(ws) => {
          setWorkspaceId(ws);
          setSelectedResource(null);
          setImpactAnswer(null);
        }}
        onSyncComplete={() => loadResources(workspaceId)}
      />

      <main className="workflow-grid">
        {/* Left Column: Resource Inventory */}
        <section aria-label="Resource Inventory">
          <ResourceInventory
            resources={resources}
            selectedResource={selectedResource}
            onSelectResource={(res) => {
              setSelectedResource(res);
              setImpactAnswer(null);
              setImpactError(null);
            }}
            loading={loadingResources}
            error={resourcesError}
            onRefresh={() => loadResources(workspaceId)}
          />
        </section>

        {/* Right Column: Workflow Steps (Inspector, Change Proposal, Impact Results) */}
        <section aria-label="Impact Workflow" style={{ display: "flex", flexDirection: "column", gap: "20px" }}>
          {selectedResource && (
            <ResourceInspector
              workspaceId={workspaceId}
              resource={selectedResource}
            />
          )}

          <ChangeProposalForm
            selectedResource={selectedResource}
            onAnalyze={handleAnalyze}
            analyzing={analyzing}
          />

          <ImpactResultView
            answer={impactAnswer}
            error={impactError}
            loading={analyzing}
          />
        </section>
      </main>
    </div>
  );
}
