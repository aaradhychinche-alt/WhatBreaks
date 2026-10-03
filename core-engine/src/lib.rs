//! WhatBreaks Core Engine
//!
//! This is the root of the WB Core Engine crate.
//! The Core Engine is provider-agnostic: it does not import Kubernetes,
//! AWS, GCP, or any other provider SDK.
//!
//! # Modules
//!
//! - [`resource`]: The foundational Entity / Resource model.

pub mod resource;
