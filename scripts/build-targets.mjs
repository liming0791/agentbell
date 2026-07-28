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
  return [
    {
      command: "./cmd/agentbell",
      fileName: `agentbell-${goos}-${goarch}${extension}`
    },
    {
      command: "./cmd/agentbell-bridge",
      fileName: `agentbell-bridge-${goos}-${goarch}${extension}`
    }
  ];
}
