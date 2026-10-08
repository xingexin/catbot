import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

// Resolve actual containers through Compose, including legacy project names.
// Use TEST_DOCKER_CONTEXT only when explicitly supplied by the test caller.
export const dockerEnvironment = {
  ...process.env,
  ...(process.env.TEST_DOCKER_CONTEXT
    ? { DOCKER_CONTEXT: process.env.TEST_DOCKER_CONTEXT }
    : {}),
};

export function composeContainer(service) {
  const id = execFileSync(
    fileURLToPath(new URL("./compose", import.meta.url)),
    ["ps", "-q", service],
    { encoding: "utf8", env: dockerEnvironment },
  ).trim();
  if (!id || /\s/.test(id)) {
    throw new Error(`Expected one running ${service} container in this Compose project`);
  }
  return id;
}
