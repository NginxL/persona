const { execFileSync } = require("node:child_process");
const { chmodSync, renameSync, rmSync, statSync } = require("node:fs");
const { join } = require("node:path");

const MINIMUM = "15.0";

function one(values, label) {
  const unique = [...new Set(values)];
  if (unique.length !== 1) throw new Error(`vtool did not report one ${label}: ${unique.join(", ") || "missing"}`);
  return unique[0];
}

function parseBuildMetadata(output) {
  const minimums = [...output.matchAll(/^\s+minos\s+(\S+)\s*$/gm)].map(match => match[1]);
  if (minimums.length === 0) throw new Error("vtool reported no macOS minimum versions");
  const sdk = one([...output.matchAll(/^\s+sdk\s+(\S+)\s*$/gm)].map(match => match[1]), "SDK version");
  const tools = [...output.matchAll(/^\s+tool\s+(\S+)\s*$/gm)].map(match => match[1]);
  const versions = [...output.matchAll(/^\s+version\s+(\S+)\s*$/gm)].map(match => match[1]);
  if (tools.length !== versions.length) throw new Error("vtool reported incomplete linker metadata");
  return {
    minimums,
    sdk,
    tool: tools.length ? one(tools, "linker tool") : null,
    toolVersion: versions.length ? one(versions, "linker version") : null,
  };
}

async function afterPack(context) {
  if (context.electronPlatformName !== "darwin") return;
  const product = context.packager.appInfo.productFilename;
  const bundle = join(context.appOutDir, `${product}.app`);
  const executable = join(bundle, "Contents", "MacOS", product);
  const show = () => execFileSync("vtool", ["-show-build", executable], { encoding: "utf8" });
  const metadata = parseBuildMetadata(show());
  if (metadata.minimums.some(minimum => minimum !== MINIMUM)) {
    const temporary = `${executable}.macos15`;
    const args = ["-set-build-version", "macos", MINIMUM, metadata.sdk];
    if (metadata.tool) args.push("-tool", metadata.tool, metadata.toolVersion);
    args.push("-output", temporary, executable);
    const mode = statSync(executable).mode;
    try {
      rmSync(temporary, { force: true });
      execFileSync("vtool", args);
      chmodSync(temporary, mode);
      renameSync(temporary, executable);
    } finally {
      rmSync(temporary, { force: true });
    }
    if (parseBuildMetadata(show()).minimums.some(minimum => minimum !== MINIMUM)) {
      throw new Error("the macOS executable did not retain the 15.0 deployment target");
    }
  }
}

module.exports = { afterPack, parseBuildMetadata };
