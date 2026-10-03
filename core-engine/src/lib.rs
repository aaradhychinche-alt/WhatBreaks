//! WhatBreaks Core Engine
//!
//! This is the root of the WB Core Engine crate.
//! The Core Engine is provider-agnostic: it does not import Kubernetes,
//! AWS, GCP, or any other provider SDK.
//!
//! # Modules
//!
//! - [`resource`]: The foundational Entity / Resource model.
//! - [`relationship`]: Directional relationship model between resource identities.

pub mod relationship;
pub mod resource;

pub use relationship::{Relationship, RelationshipCategory, RelationshipKind};
pub use resource::{
    Attributes, Provider, Resource, ResourceId, ResourceIdentity, ResourceKind, ResourceMetadata,
};
