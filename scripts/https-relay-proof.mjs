const proofPattern = /^HTTPS_RELAY_SMOKE_PASS paired=1 deliveries=2 history=2 duplicate=1 recovery=1 metadata=1 connector=https state=healthy running=1 tls=verified spki=pinned$/u;

export function parseHTTPSRelaySmokeProof(output) {
  const canonical = String(output).trim();
  if (!proofPattern.test(canonical)) {
    throw new Error("HTTPS relay smoke proof is incomplete or invalid.");
  }
  return {
    paired: 1,
    deliveries: 2,
    history: 2,
    duplicate: true,
    recovery: true,
    metadataOnly: true,
    connector: "https",
    state: "healthy",
    running: true,
    tls: "verified",
    spki: "pinned"
  };
}
