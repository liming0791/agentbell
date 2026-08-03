export const releaseTargets = Object.freeze([
  Object.freeze({ goos: "windows", goarch: "amd64" }),
  Object.freeze({ goos: "windows", goarch: "arm64" }),
  Object.freeze({ goos: "darwin", goarch: "amd64" }),
  Object.freeze({ goos: "darwin", goarch: "arm64" }),
  Object.freeze({ goos: "linux", goarch: "amd64" }),
  Object.freeze({ goos: "linux", goarch: "arm64" })
]);

const supportedTargets = new Set(
  releaseTargets.map((target) => `${target.goos}/${target.goarch}`)
);

export function releaseArtifacts({ goos, goarch }) {
  if (!supportedTargets.has(`${goos}/${goarch}`)) {
    throw new Error(`Unsupported release target: ${goos}/${goarch}.`);
  }
  const extension = goos === "windows" ? ".exe" : "";
  const artifacts = [
    {
      command: "./cmd/agentbell",
      fileName: `agentbell-${goos}-${goarch}${extension}`,
      role: "core"
    },
    {
      command: "./cmd/agentbell-bridge",
      fileName: `agentbell-bridge-${goos}-${goarch}${extension}`,
      role: "hook"
    }
  ];
  if (goos === "windows") {
    artifacts.push({
      command: "./cmd/agentbell-bridge",
      fileName: `agentbell-service-${goos}-${goarch}${extension}`,
      role: "service"
    });
  }
  return artifacts;
}
