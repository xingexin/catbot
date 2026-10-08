export function isolatedEnvironment(home: string): Record<string, string> {
  const env: Record<string, string> = {
    PATH: process.env.PATH ?? "",
    HOME: home,
    LANG: "en_US.UTF-8",
  };
  for (const name of [
    "HTTPS_PROXY",
    "HTTP_PROXY",
    "NO_PROXY",
    "SSL_CERT_FILE",
  ]) {
    const value = process.env[name];
    if (value) env[name] = value;
  }
  return env;
}
