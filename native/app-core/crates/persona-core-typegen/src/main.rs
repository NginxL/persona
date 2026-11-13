//! Generates the clients' codecs for the core's wire types (`persona_core_runtime::wire`), bincode encoded.
//!
//!     cargo run -p persona-core-typegen            # writes the generated sources
//!     cargo run -p persona-core-typegen -- --check # fails when they are out of date
//!
//! Kotlin goes to android/core/rustcore. The generated files are committed, so a change to a wire type and its codecs
//! land together.

use std::path::{Path, PathBuf};

use persona_core_runtime::wire::{
    Action, ChatEvent, ChatPatch, ChatSnapshot, Config, Effect, ListOp, Request, WireBatch,
};
use serde_generate::{CodeGeneratorConfig, Encoding, SourceInstaller, kotlin};
use serde_reflection::{Registry, Tracer, TracerConfig};

const KOTLIN_PACKAGE: &str = "persona.core.rustcore.wire";

fn registry() -> Registry {
    let mut tracer = Tracer::new(TracerConfig::default());
    tracer.trace_simple_type::<Request>().expect("Request");
    tracer.trace_simple_type::<ListOp>().expect("ListOp");
    tracer.trace_simple_type::<ChatEvent>().expect("ChatEvent");
    tracer.trace_simple_type::<WireBatch>().expect("WireBatch");
    tracer.trace_simple_type::<ChatSnapshot>().expect("ChatSnapshot");
    tracer.trace_simple_type::<Config>().expect("Config");
    tracer.trace_simple_type::<Action>().expect("Action");
    tracer.trace_simple_type::<Effect>().expect("Effect");
    tracer.trace_simple_type::<ChatPatch>().expect("ChatPatch");
    tracer.registry().expect("every wire type is traced")
}

fn write_kotlin(root: &Path, registry: &Registry) {
    let config = CodeGeneratorConfig::new(KOTLIN_PACKAGE.to_owned()).with_encodings([Encoding::Bincode]);
    let installer = kotlin::Installer::new(root.to_path_buf());
    installer.install_module(&config, registry).expect("Kotlin codecs");
    installer.install_serde_runtime().expect("Kotlin serde runtime");
    installer.install_bincode_runtime().expect("Kotlin bincode runtime");
}

fn files(root: &Path) -> Vec<(PathBuf, Vec<u8>)> {
    let mut out = Vec::new();
    let mut stack = vec![root.to_path_buf()];
    while let Some(dir) = stack.pop() {
        let Ok(entries) = std::fs::read_dir(&dir) else { continue };
        for entry in entries {
            let path = entry.expect("directory entry").path();
            if path.is_dir() {
                stack.push(path);
            } else {
                let relative = path.strip_prefix(root).expect("under root").to_path_buf();
                out.push((relative, std::fs::read(&path).expect("read")));
            }
        }
    }
    out.sort();
    out
}

/// The generated trees under `root`: the wire package and the codec runtime.
fn generated(root: &Path) -> Vec<(PathBuf, Vec<u8>)> {
    ["persona/core/rustcore/wire", "com/novi"]
        .iter()
        .flat_map(|dir| {
            files(&root.join(dir))
                .into_iter()
                .map(move |(path, bytes)| (Path::new(dir).join(path), bytes))
        })
        .collect()
}

fn main() {
    let repository = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../..");
    let kotlin_root = repository.join("android/core/rustcore/src/main/kotlin");
    let registry = registry();
    if std::env::args().any(|a| a == "--check") {
        let scratch = std::env::temp_dir().join(format!("persona-core-typegen-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&scratch);
        write_kotlin(&scratch, &registry);
        let fresh = generated(&scratch);
        let _ = std::fs::remove_dir_all(&scratch);
        if fresh != generated(&kotlin_root) {
            eprintln!(
                "persona-core-typegen: the Kotlin codecs are out of date; run `cargo run -p persona-core-typegen`"
            );
            std::process::exit(1);
        }
        println!("persona-core-typegen: codecs up to date");
        return;
    }
    for dir in ["persona/core/rustcore/wire", "com/novi"] {
        let _ = std::fs::remove_dir_all(kotlin_root.join(dir));
    }
    write_kotlin(&kotlin_root, &registry);
    println!("persona-core-typegen: wrote {}", kotlin_root.display());
}
