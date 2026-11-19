package persona.buildlogic

import java.nio.file.Files
import java.nio.file.StandardCopyOption.ATOMIC_MOVE
import java.nio.file.StandardCopyOption.REPLACE_EXISTING
import javax.inject.Inject
import org.gradle.api.DefaultTask
import org.gradle.api.file.ConfigurableFileCollection
import org.gradle.api.file.DirectoryProperty
import org.gradle.api.file.RegularFileProperty
import org.gradle.api.provider.ListProperty
import org.gradle.api.provider.MapProperty
import org.gradle.api.provider.Property
import org.gradle.api.tasks.CacheableTask
import org.gradle.api.tasks.Input
import org.gradle.api.tasks.InputFiles
import org.gradle.api.tasks.Internal
import org.gradle.api.tasks.OutputFile
import org.gradle.api.tasks.PathSensitive
import org.gradle.api.tasks.PathSensitivity
import org.gradle.api.tasks.TaskAction
import org.gradle.process.ExecOperations

/**
 * Builds the shared Rust core's JNI library for this machine (native/app-core) and copies it into the build directory,
 * where host-JVM tests load it. The inputs are the core's sources, manifests, lockfile and pinned toolchain file, the
 * cargo profile and features, the host and the Rust flags in the environment; with none of them changed the task is
 * UP-TO-DATE, or FROM-CACHE in another checkout, and cargo does not run (#10712). Cargo's target directory is its
 * incremental state for this checkout only, never part of the cache key.
 */
@CacheableTask
abstract class HostCoreLibrary @Inject constructor(private val execOperations: ExecOperations) : DefaultTask() {
    @get:InputFiles @get:PathSensitive(PathSensitivity.RELATIVE)
    abstract val sources: ConfigurableFileCollection

    /** A cargo profile: `dev` (cargo's `debug` directory) or `release`. */
    @get:Input abstract val profile: Property<String>

    @get:Input abstract val features: ListProperty<String>

    /** The library is a binary for this OS and architecture. */
    @get:Input abstract val host: Property<String>

    /** RUSTFLAGS and CARGO_ENCODED_RUSTFLAGS as cargo inherits them (empty when unset). */
    @get:Input abstract val rustFlags: MapProperty<String, String>

    @get:Internal abstract val workspace: DirectoryProperty

    @get:Internal abstract val cargoTargetDirectory: DirectoryProperty

    @get:OutputFile abstract val library: RegularFileProperty

    @TaskAction fun build() {
        val profileName = profile.get()
        execOperations.exec {
            workingDir = workspace.get().asFile
            environment("CARGO_TARGET_DIR", cargoTargetDirectory.get().asFile.absolutePath)
            val featureArguments = features.get().takeIf { it.isNotEmpty() }?.let { listOf("--features", it.joinToString(",")) }
            commandLine(listOf("cargo", "build", "--locked", "--profile", profileName, "-p", "persona-core-ffi") +
                featureArguments.orEmpty())
        }
        val built = cargoTargetDirectory.get().dir(if (profileName == "dev") "debug" else profileName)
            .file(hostLibraryName()).asFile.toPath()
        // A new file, never an overwrite: macOS refuses to load a signed library whose bytes changed in place.
        val target = library.get().asFile.toPath()
        Files.createDirectories(target.parent)
        val temporary = Files.createTempFile(target.parent, "${target.fileName}.", ".tmp")
        Files.copy(built, temporary, REPLACE_EXISTING)
        Files.move(temporary, target, REPLACE_EXISTING, ATOMIC_MOVE)
    }
}

/** The core's JNI library file name on this host OS. */
fun hostLibraryName(osName: String = System.getProperty("os.name")): String = when {
    osName.startsWith("Mac") -> "libpersona_core.dylib"
    osName.startsWith("Windows") -> "persona_core.dll"
    else -> "libpersona_core.so"
}
