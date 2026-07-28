import { URL } from "node:url";

const githubAccept = "application/vnd.github+json";

function requestHeaders(token) {
  const headers = {
    accept: githubAccept,
    "user-agent": "agentbell-bootstrap"
  };
  if (token) {
    headers.authorization = `Bearer ${token}`;
  }
  return headers;
}

function nextPageURL(response, repositoryAPI) {
  const link = response.headers?.get?.("link");
  if (!link) {
    return null;
  }
  for (const entry of link.split(",")) {
    const match = /^\s*<([^>]+)>;\s*rel="([^"]+)"\s*$/.exec(entry);
    if (!match || match[2] !== "next") {
      continue;
    }
    const candidate = new URL(match[1]);
    const expected = new URL(repositoryAPI);
    if (
      candidate.origin !== expected.origin ||
      !candidate.pathname.startsWith(`${expected.pathname}/releases`)
    ) {
      throw new Error("GitHub release pagination returned an unsafe URL.");
    }
    return candidate.toString();
  }
  return null;
}

async function request(fetchImpl, url, token) {
  return fetchImpl(url, {
    headers: requestHeaders(token),
    redirect: "follow"
  });
}

export async function fetchGitHubReleaseMetadata({
  fetchImpl,
  repositoryAPI,
  tagName,
  token
}) {
  const byTagURL =
    `${repositoryAPI}/releases/tags/${encodeURIComponent(tagName)}`;
  const byTagResponse = await request(fetchImpl, byTagURL, token);
  if (byTagResponse.ok) {
    return byTagResponse.json();
  }
  if (byTagResponse.status !== 404) {
    throw new Error(`Download failed (${byTagResponse.status}) for ${byTagURL}`);
  }

  // GitHub's release-by-tag endpoint intentionally returns 404 for drafts.
  // Authenticated release listings include drafts, so use that as the fallback
  // needed by the pre-publication release smoke.
  let pageURL = `${repositoryAPI}/releases?per_page=100`;
  while (pageURL) {
    const pageResponse = await request(fetchImpl, pageURL, token);
    if (!pageResponse.ok) {
      throw new Error(`Download failed (${pageResponse.status}) for ${pageURL}`);
    }
    const releases = await pageResponse.json();
    if (!Array.isArray(releases)) {
      throw new Error("GitHub releases endpoint returned no release list.");
    }
    const release = releases.find(
      (candidate) => candidate?.tag_name === tagName
    );
    if (release) {
      return release;
    }
    pageURL = nextPageURL(pageResponse, repositoryAPI);
  }
  throw new Error(
    `GitHub release ${tagName} was not found, including authenticated drafts.`
  );
}
