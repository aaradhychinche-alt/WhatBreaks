//! WhatBreaks Core Engine – gRPC Discovery Server
//!
//! This crate implements the gRPC transport adapter for the WhatBreaks Core Engine.
//! It bridges the generated protobuf/tonic contracts (`wb-core-proto`) to the
//! authoritative domain models and rule engine (`wb-core-engine`).
//!
//! # Separation of Concerns
//!
//! - Domain logic, invariants, and discovery rules live exclusively in `wb-core-engine`.
//! - Protobuf definitions and gRPC service traits live exclusively in `wb-core-proto`.
//! - This crate handles validation, serialization, deserialization, and gRPC status mapping.

#![allow(clippy::result_large_err)]

pub mod convert;
pub mod server;
pub mod service;

pub use server::{create_answer_service, create_service, run_server};
pub use service::{AnswerServiceImpl, DiscoveryServiceImpl};
