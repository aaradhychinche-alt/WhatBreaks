package answer

// ResourceIdentity uniquely identifies an infrastructure resource across providers.
type ResourceIdentity struct {
	Provider     string `json:"provider"`
	ResourceType string `json:"resource_type"`
	ProviderID   string `json:"provider_id"`
}

// EvidenceSource identifies the origin of an evidence observation.
type EvidenceSource struct {
	Provider  string `json:"provider"`
	Collector string `json:"collector"`
}

// Relationship models a directed semantic link between two infrastructure resources.
type Relationship struct {
	Source   ResourceIdentity `json:"source"`
	Target   ResourceIdentity `json:"target"`
	Kind     string           `json:"kind"`
	Category string           `json:"category"`
}

// ChangeType identifies the nature of a proposed change.
type ChangeType string

const (
	ChangeTypeDelete  ChangeType = "DELETE"
	ChangeTypeUpdate  ChangeType = "UPDATE"
	ChangeTypeScale   ChangeType = "SCALE"
	ChangeTypeReplace ChangeType = "REPLACE"
)

// ProposedChange defines the change intent specified by the caller.
type ProposedChange struct {
	ChangeType ChangeType `json:"change_type"`
	Details    string     `json:"details,omitempty"`
}

// ChangeAssessment provides the deterministic assessment of the proposed change.
type ChangeAssessment struct {
	ChangeType   ChangeType `json:"change_type"`
	ImpactNature string     `json:"impact_nature"`
	Assumptions  []string   `json:"assumptions"`
	Limitations  []string   `json:"limitations"`
}

// ImpactSummary provides deterministic aggregate facts summarizing the candidate blast radius.
type ImpactSummary struct {
	ImpactedCount uint32 `json:"impacted_count"`
	DirectCount   uint32 `json:"direct_count"`
	IndirectCount uint32 `json:"indirect_count"`
	MaxDepth      uint32 `json:"max_depth"`
}

// ImpactedResource pairs an impacted resource identity with its shortest hop distance.
type ImpactedResource struct {
	Resource     ResourceIdentity `json:"resource"`
	Depth        uint32           `json:"depth"`
	ImpactType   string           `json:"impact_type,omitempty"`
	ImpactReason string           `json:"impact_reason,omitempty"`
}

// AnswerRelationship represents an infrastructure relationship with its derived operational state and supporting evidence IDs.
type AnswerRelationship struct {
	Relationship Relationship `json:"relationship"`
	State        string       `json:"state"`
	EvidenceIDs  []string     `json:"evidence_ids"`
}

// ImpactPath represents a directed traversal path of resources and relationships.
type ImpactPath struct {
	Resources     []ResourceIdentity `json:"resources"`
	Relationships []Relationship     `json:"relationships"`
}

// AnswerEvidence represents lightweight factual metadata supporting relationships in the answer.
type AnswerEvidence struct {
	ID              string         `json:"id"`
	Source          EvidenceSource `json:"source"`
	ObservedAt      string         `json:"observed_at"`
	ObservationType string         `json:"observation_type"`
}

// ExplanationFact provides a structured, factual explanation of why a resource is candidate-impacted.
type ExplanationFact struct {
	Path          ImpactPath           `json:"path"`
	Relationships []AnswerRelationship `json:"relationships"`
	EvidenceIDs   []string             `json:"evidence_ids"`
}

// ImpactAnswer is the complete deterministic, explainable answer returned by the Answer Engine.
type ImpactAnswer struct {
	Target            ResourceIdentity     `json:"target"`
	Summary           ImpactSummary        `json:"summary"`
	ImpactedResources []ImpactedResource   `json:"impacted_resources"`
	Relationships     []AnswerRelationship `json:"relationships"`
	Paths             []ImpactPath         `json:"paths"`
	Evidence          []AnswerEvidence     `json:"evidence"`
	ExplanationFacts  []ExplanationFact    `json:"explanation_facts"`
	ChangeAssessment  *ChangeAssessment    `json:"change_assessment,omitempty"`
}

// ImpactRequest defines the incoming payload for impact analysis.
type ImpactRequest struct {
	Target         *ResourceIdentity `json:"target"`
	Direction      string            `json:"direction"`
	MaxDepth       uint32            `json:"max_depth"`
	WorkspaceID    string            `json:"workspace_id,omitempty"`
	ProposedChange *ProposedChange   `json:"proposed_change,omitempty"`
}
