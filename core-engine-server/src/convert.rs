//! Conversion between protobuf transport types and authoritative domain models.
//!
//! Preserves strict separation of concerns:
//! - Domain models remain authoritative and contain all domain invariants.
//! - Proto models are plain data transfer containers.
//! - All validation errors on malformed network input return `tonic::Status::invalid_argument`.

use chrono::{DateTime, Utc};
use tonic::Status;
use wb_core_engine::evidence::{
    CollectorId, Evidence, EvidenceId, EvidenceSource, ObservationType,
};
use wb_core_engine::relationship::Relationship;
use wb_core_engine::resource::{Provider, ResourceIdentity, ResourceKind};
use wb_core_engine::{DiscoveredRelationship, DiscoveryResult};
use wb_core_proto::core_v1 as proto;

/// Convert a protobuf `ResourceIdentity` into the domain `ResourceIdentity`.
pub fn proto_to_resource_identity(p: proto::ResourceIdentity) -> Result<ResourceIdentity, Status> {
    if p.provider.trim().is_empty() {
        return Err(Status::invalid_argument(
            "resource_identity.provider cannot be empty",
        ));
    }
    if p.resource_type.trim().is_empty() {
        return Err(Status::invalid_argument(
            "resource_identity.resource_type cannot be empty",
        ));
    }
    if p.provider_id.trim().is_empty() {
        return Err(Status::invalid_argument(
            "resource_identity.provider_id cannot be empty",
        ));
    }

    Ok(ResourceIdentity::new(
        Provider::new(p.provider),
        ResourceKind::new(p.resource_type),
        p.provider_id,
    ))
}

/// Convert a protobuf `EvidenceSource` into the domain `EvidenceSource`.
pub fn proto_to_evidence_source(p: proto::EvidenceSource) -> Result<EvidenceSource, Status> {
    if p.provider.trim().is_empty() {
        return Err(Status::invalid_argument(
            "evidence.source.provider cannot be empty",
        ));
    }
    if p.collector.trim().is_empty() {
        return Err(Status::invalid_argument(
            "evidence.source.collector cannot be empty",
        ));
    }

    Ok(EvidenceSource::new(
        Provider::new(p.provider),
        CollectorId::new(p.collector),
    ))
}

/// Convert a protobuf `Evidence` message into the authoritative domain `Evidence`.
pub fn proto_to_evidence(p: proto::Evidence) -> Result<Evidence, Status> {
    // 1. EvidenceId (validate UUID format)
    let evidence_id: EvidenceId = serde_json::from_str(&format!("\"{}\"", p.id)).map_err(|e| {
        Status::invalid_argument(format!(
            "malformed evidence id '{}': must be a valid UUID ({})",
            p.id, e
        ))
    })?;

    // 2. Source (required)
    let source_proto = p
        .source
        .ok_or_else(|| Status::invalid_argument("missing required field: evidence.source"))?;
    let source = proto_to_evidence_source(source_proto)?;

    // 3. ObservedAt (RFC 3339 format)
    let observed_at = DateTime::parse_from_rfc3339(&p.observed_at)
        .map(|dt| dt.with_timezone(&Utc))
        .map_err(|e| {
            Status::invalid_argument(format!(
                "malformed observed_at timestamp '{}': expected RFC 3339 format ({})",
                p.observed_at, e
            ))
        })?;

    // 4. ObservationType
    if p.observation_type.trim().is_empty() {
        return Err(Status::invalid_argument(
            "evidence.observation_type cannot be empty",
        ));
    }
    let observation_type = ObservationType::new(p.observation_type);

    // 5. Subject (required)
    let subject_proto = p
        .subject
        .ok_or_else(|| Status::invalid_argument("missing required field: evidence.subject"))?;
    let subject = proto_to_resource_identity(subject_proto)?;

    // 6. Data payload (valid JSON bytes required)
    if p.data.is_empty() {
        return Err(Status::invalid_argument(
            "evidence.data cannot be empty: expected valid JSON payload",
        ));
    }
    let data: serde_json::Value = serde_json::from_slice(&p.data)
        .map_err(|e| Status::invalid_argument(format!("malformed evidence data JSON: {}", e)))?;

    Ok(Evidence {
        id: evidence_id,
        source,
        observed_at,
        observation_type,
        subject,
        data,
    })
}

/// Convert a domain `ResourceIdentity` to protobuf.
pub fn resource_identity_to_proto(identity: &ResourceIdentity) -> proto::ResourceIdentity {
    proto::ResourceIdentity {
        provider: identity.provider.as_str().to_string(),
        resource_type: identity.resource_type.as_str().to_string(),
        provider_id: identity.provider_id.clone(),
    }
}

/// Convert a domain `Relationship` to protobuf.
pub fn relationship_to_proto(rel: &Relationship) -> proto::Relationship {
    proto::Relationship {
        source: Some(resource_identity_to_proto(&rel.source)),
        target: Some(resource_identity_to_proto(&rel.target)),
        kind: rel.kind.as_str().to_string(),
        category: rel.category.as_str().to_string(),
    }
}

/// Convert a protobuf `Relationship` to domain `Relationship`.
pub fn proto_to_relationship(p: proto::Relationship) -> Result<Relationship, Status> {
    let source_proto = p
        .source
        .ok_or_else(|| Status::invalid_argument("missing required field: relationship.source"))?;
    let source = proto_to_resource_identity(source_proto)?;

    let target_proto = p
        .target
        .ok_or_else(|| Status::invalid_argument("missing required field: relationship.target"))?;
    let target = proto_to_resource_identity(target_proto)?;

    let kind_str = p.kind.trim();
    if kind_str.is_empty() {
        return Err(Status::invalid_argument(
            "relationship.kind cannot be empty",
        ));
    }
    let kind_upper = kind_str.to_ascii_uppercase();
    let canonical_cat = match kind_upper.as_str() {
        "DEPENDS_ON" => Some(wb_core_engine::relationship::RelationshipCategory::Dependency),
        "CALLS" => Some(wb_core_engine::relationship::RelationshipCategory::Invocation),
        "READS_FROM" => Some(wb_core_engine::relationship::RelationshipCategory::DataFlow),
        "WRITES_TO" => Some(wb_core_engine::relationship::RelationshipCategory::DataFlow),
        "OWNS" => Some(wb_core_engine::relationship::RelationshipCategory::Ownership),
        "RUNS_ON" => Some(wb_core_engine::relationship::RelationshipCategory::Placement),
        "AUTHORIZES" => Some(wb_core_engine::relationship::RelationshipCategory::Authorization),
        _ => None,
    };

    let kind = if canonical_cat.is_some() {
        wb_core_engine::relationship::RelationshipKind::new(kind_upper)
    } else {
        wb_core_engine::relationship::RelationshipKind::new(kind_str)
    };

    let category_opt = match p.category.trim().to_lowercase().as_str() {
        "dependency" => Some(wb_core_engine::relationship::RelationshipCategory::Dependency),
        "dataflow" => Some(wb_core_engine::relationship::RelationshipCategory::DataFlow),
        "invocation" => Some(wb_core_engine::relationship::RelationshipCategory::Invocation),
        "authorization" => Some(wb_core_engine::relationship::RelationshipCategory::Authorization),
        "ownership" => Some(wb_core_engine::relationship::RelationshipCategory::Ownership),
        "placement" => Some(wb_core_engine::relationship::RelationshipCategory::Placement),
        "network" => Some(wb_core_engine::relationship::RelationshipCategory::Network),
        _ => None,
    };

    let effective_cat = category_opt.or(canonical_cat);

    if let Some(canonical) = kind.category() {
        if let Some(cat) = category_opt {
            if cat != canonical {
                return Err(Status::invalid_argument(format!(
                    "relationship kind '{}' requires category '{:?}', but '{:?}' was provided",
                    kind, canonical, cat
                )));
            }
        }
        Ok(Relationship::new(source, target, kind))
    } else if let Some(cat) = effective_cat {
        Ok(Relationship::with_category(source, target, kind, cat))
    } else {
        Err(Status::invalid_argument(format!(
            "relationship kind '{}' has no canonical category; valid category must be provided",
            kind
        )))
    }
}

/// Convert a protobuf `RelationshipEvidence` to domain `Relationship` and evidence IDs.
pub fn proto_to_relationship_evidence(
    p: proto::RelationshipEvidence,
) -> Result<(Relationship, Vec<wb_core_engine::evidence::EvidenceId>), Status> {
    let rel_proto = p.relationship.ok_or_else(|| {
        Status::invalid_argument("missing required field: relationship_evidence.relationship")
    })?;
    let rel = proto_to_relationship(rel_proto)?;

    let mut evidence_ids = Vec::with_capacity(p.evidence_ids.len());
    for id_str in p.evidence_ids {
        let ev_id: wb_core_engine::evidence::EvidenceId =
            serde_json::from_str(&format!("\"{}\"", id_str)).map_err(|e| {
                Status::invalid_argument(format!("malformed evidence id '{}': {}", id_str, e))
            })?;
        evidence_ids.push(ev_id);
    }

    Ok((rel, evidence_ids))
}

/// Convert a domain `DiscoveredRelationship` to protobuf.
pub fn discovered_relationship_to_proto(
    dr: &DiscoveredRelationship,
) -> proto::DiscoveredRelationship {
    proto::DiscoveredRelationship {
        relationship: Some(relationship_to_proto(&dr.relationship)),
        supporting_evidence_ids: dr
            .supporting_evidence
            .iter()
            .map(|id| id.to_string())
            .collect(),
    }
}

/// Convert a domain `DiscoveryResult` to protobuf `DiscoveryResult`.
pub fn discovery_result_to_proto(res: DiscoveryResult) -> proto::DiscoveryResult {
    let outcome = match res {
        DiscoveryResult::Discovered(dr) => {
            proto::discovery_result::Outcome::Discovered(discovered_relationship_to_proto(&dr))
        }
        DiscoveryResult::Insufficient => {
            proto::discovery_result::Outcome::Insufficient(proto::Insufficient {})
        }
        DiscoveryResult::Conflict { description } => {
            proto::discovery_result::Outcome::Conflict(proto::Conflict { description })
        }
        DiscoveryResult::Invalid { description } => {
            proto::discovery_result::Outcome::Invalid(proto::Invalid { description })
        }
    };

    proto::DiscoveryResult {
        outcome: Some(outcome),
    }
}

// ---------------------------------------------------------------------------
// Answer Engine Conversions
// ---------------------------------------------------------------------------

use wb_core_engine::answer::{
    AnswerEvidence, AnswerRelationship, AnswerRequest, ExplanationFact, ImpactAnswer, ImpactPath,
    ImpactSummary,
};
use wb_core_engine::traversal::TraversalDirection;
use wb_core_engine::ImpactedResource;

/// Convert a protobuf `AnalyzeImpactRequest` into the domain `AnswerRequest`.
pub fn proto_to_answer_request(p: proto::AnalyzeImpactRequest) -> Result<AnswerRequest, Status> {
    let target_proto = p
        .target
        .ok_or_else(|| Status::invalid_argument("missing required field: target"))?;
    let target = proto_to_resource_identity(target_proto)?;

    let direction = match p.direction.trim().to_lowercase().as_str() {
        "incoming" => TraversalDirection::Incoming,
        "outgoing" => TraversalDirection::Outgoing,
        other => {
            return Err(Status::invalid_argument(format!(
                "invalid traversal direction '{}': must be 'incoming' or 'outgoing'",
                other
            )));
        }
    };

    if p.max_depth == 0 {
        return Err(Status::invalid_argument("max_depth must be greater than 0"));
    }

    Ok(AnswerRequest::new(target, direction, p.max_depth as usize))
}

/// Convert domain `ImpactSummary` to protobuf `ImpactSummary`.
pub fn impact_summary_to_proto(s: &ImpactSummary) -> proto::ImpactSummary {
    proto::ImpactSummary {
        impacted_count: s.impacted_count as u32,
        direct_count: s.direct_count as u32,
        indirect_count: s.indirect_count as u32,
        max_depth: s.max_depth as u32,
    }
}

/// Convert domain `ImpactedResource` to protobuf `ImpactedResource`.
pub fn impacted_resource_to_proto(r: &ImpactedResource) -> proto::ImpactedResource {
    proto::ImpactedResource {
        resource: Some(resource_identity_to_proto(&r.resource)),
        depth: r.depth as u32,
    }
}

/// Convert domain `AnswerRelationship` to protobuf `AnswerRelationship`.
pub fn answer_relationship_to_proto(ar: &AnswerRelationship) -> proto::AnswerRelationship {
    proto::AnswerRelationship {
        relationship: Some(relationship_to_proto(&ar.relationship)),
        state: ar.state.as_str().to_string(),
        evidence_ids: ar.evidence_ids.iter().map(|id| id.to_string()).collect(),
    }
}

/// Convert domain `ImpactPath` to protobuf `ImpactPath`.
pub fn impact_path_to_proto(p: &ImpactPath) -> proto::ImpactPath {
    proto::ImpactPath {
        resources: p.resources.iter().map(resource_identity_to_proto).collect(),
        relationships: p.relationships.iter().map(relationship_to_proto).collect(),
    }
}

/// Convert domain `AnswerEvidence` to protobuf `AnswerEvidence`.
pub fn answer_evidence_to_proto(e: &AnswerEvidence) -> proto::AnswerEvidence {
    proto::AnswerEvidence {
        id: e.id.to_string(),
        source: Some(proto::EvidenceSource {
            provider: e.source.provider.as_str().to_string(),
            collector: e.source.collector.as_str().to_string(),
        }),
        observed_at: e.observed_at.to_rfc3339(),
        observation_type: e.observation_type.as_str().to_string(),
    }
}

/// Convert domain `ExplanationFact` to protobuf `ExplanationFact`.
pub fn explanation_fact_to_proto(f: &ExplanationFact) -> proto::ExplanationFact {
    proto::ExplanationFact {
        path: Some(impact_path_to_proto(&f.path)),
        relationships: f
            .relationships
            .iter()
            .map(answer_relationship_to_proto)
            .collect(),
        evidence_ids: f.evidence_ids.iter().map(|id| id.to_string()).collect(),
    }
}

/// Convert domain `ImpactAnswer` to protobuf `AnalyzeImpactResponse`.
pub fn impact_answer_to_proto(ans: ImpactAnswer) -> proto::AnalyzeImpactResponse {
    proto::AnalyzeImpactResponse {
        target: Some(resource_identity_to_proto(&ans.target)),
        summary: Some(impact_summary_to_proto(&ans.summary)),
        impacted_resources: ans
            .impacted_resources
            .iter()
            .map(impacted_resource_to_proto)
            .collect(),
        relationships: ans
            .relationships
            .iter()
            .map(answer_relationship_to_proto)
            .collect(),
        paths: ans.paths.iter().map(impact_path_to_proto).collect(),
        evidence: ans.evidence.iter().map(answer_evidence_to_proto).collect(),
        explanation_facts: ans
            .explanation_facts
            .iter()
            .map(explanation_fact_to_proto)
            .collect(),
    }
}
