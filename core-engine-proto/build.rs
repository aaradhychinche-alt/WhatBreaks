// build.rs for wb-core-proto
//
// Uses tonic-build to compile proto/wb/core/v1/core.proto into Rust
// message types (via prost) and gRPC client/server stubs (via tonic).
//
// Output is written to OUT_DIR (managed by Cargo) and included via
// tonic::include_proto!() in src/lib.rs.
//
// This build script runs automatically during `cargo build`.
// No separate protoc plugin binary is required beyond protoc itself,
// which tonic-build invokes internally.

fn main() -> Result<(), Box<dyn std::error::Error>> {
    // The path to the .proto file, relative to the workspace root.
    // tonic-build resolves this from the directory where build.rs runs,
    // which is the crate root (core-engine-proto/).
    // We use the CARGO_MANIFEST_DIR env var to anchor the path to the
    // workspace root reliably regardless of how cargo is invoked.
    let manifest_dir = std::env::var("CARGO_MANIFEST_DIR")?;
    let workspace_root = std::path::Path::new(&manifest_dir)
        .parent()
        .expect("core-engine-proto must be a direct child of the workspace root");

    let proto_file = workspace_root.join("proto/wb/core/v1/core.proto");
    let proto_include = workspace_root.join("proto");

    tonic_build::configure()
        // Server stubs (trait definitions only — no implementation).
        // The actual gRPC server is future work in a separate crate.
        .build_server(true)
        // Client stubs are disabled for now.
        // The generated client uses tonic::transport::Channel which requires
        // the "channel" feature — a heavier tokio/transport dependency that
        // is not needed until the Rust gRPC server crate is implemented.
        // Re-enable when the server crate is added.
        .build_client(false)
        .compile_protos(&[&proto_file], &[&proto_include])?;

    // Tell Cargo to re-run this build script only when the proto file changes.
    println!(
        "cargo:rerun-if-changed={}",
        proto_file.display()
    );

    Ok(())
}
