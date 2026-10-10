export interface ResourceIdentity {
  provider: string;
  resource_type: string;
  provider_id: string;
}

export interface ResourceItem {
  provider: string;
  resource_type: string;
  provider_id: string;
  first_observed_at: string;
  last_observed_at: string;
}

export interface ListResourcesResponse {
  workspace_id: string;
  total: number;
  resources: ResourceItem[];
}

export interface ResourceRelationshipItem {
  source: ResourceIdentity;
  target: ResourceIdentity;
  kind: string;
  category: string;
  direction: "OUTGOING" | "INCOMING";
  peer: ResourceIdentity;
  evidence_ids?: string[];
  first_observed_at: string;
  last_observed_at: string;
}

export interface ResourceDetailResponse {
  workspace_id: string;
  resource: ResourceItem;
  relationships: ResourceRelationshipItem[];
}

export interface DiscoverySyncResponse {
  workspace_id: string;
  status: "COMPLETED" | "PARTIAL";
  evidence_count: number;
  resources_created: number;
  resources_updated: number;
  resources_total: number;
  relationships_created: number;
  relationships_total: number;
  conflicts_count: number;
  duration_ms: number;
}

export type ChangeType = "DELETE" | "UPDATE" | "SCALE" | "REPLACE";

export interface ProposedChange {
  change_type: ChangeType;
  details?: string;
}

export interface ImpactRequest {
  target: ResourceIdentity;
  direction: "incoming" | "outgoing";
  max_depth: number;
  workspace_id?: string;
  proposed_change?: ProposedChange;
}

export interface ImpactSummary {
  impacted_count: number;
  direct_count: number;
  indirect_count: number;
  max_depth: number;
}

export interface ImpactedResource {
  resource: ResourceIdentity;
  depth: number;
  impact_type?: string;
  impact_reason?: string;
}

export interface AnswerRelationship {
  relationship: {
    source: ResourceIdentity;
    target: ResourceIdentity;
    kind: string;
    category: string;
  };
  state: string;
  evidence_ids: string[];
}

export interface ImpactPath {
  resources: ResourceIdentity[];
  relationships: {
    source: ResourceIdentity;
    target: ResourceIdentity;
    kind: string;
    category: string;
  }[];
}

export interface AnswerEvidence {
  id: string;
  source: {
    provider: string;
    collector: string;
  };
  observed_at: string;
  observation_type: string;
}

export interface ExplanationFact {
  path: ImpactPath;
  relationships: AnswerRelationship[];
  evidence_ids: string[];
}

export interface ChangeAssessment {
  change_type: ChangeType;
  impact_nature: string;
  assumptions: string[];
  limitations: string[];
}

export interface ImpactAnswer {
  target: ResourceIdentity;
  summary: ImpactSummary;
  impacted_resources: ImpactedResource[];
  relationships: AnswerRelationship[];
  paths: ImpactPath[];
  evidence: AnswerEvidence[];
  explanation_facts: ExplanationFact[];
  change_assessment?: ChangeAssessment;
}

export interface ApiError {
  error: string;
  code: string;
}
