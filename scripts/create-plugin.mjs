import { mkdir, readFile, writeFile, cp, access } from "node:fs/promises";
import { resolve, dirname } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const repository = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const usage = "Usage: node scripts/create-plugin.mjs <lowercase-plugin-id> [--language ts|go]";

export function parseArgs(args) {
  const [name, ...options] = args;
  let language = "ts";
  if (options.length === 2 && options[0] === "--language") language = options[1];
  else if (options.length === 1 && options[0].startsWith("--language=")) language = options[0].slice(11);
  else if (options.length) throw new Error(usage);
  if (!name || !/^[a-z][a-z0-9_-]{0,30}$/.test(name) || !["ts", "go"].includes(language)) {
    throw new Error(usage);
  }
  return { name, language };
}

export async function createPlugin({ name, language }, root = repository) {
  // Validate independently so programmatic callers cannot escape plugins/.
  parseArgs([name, "--language", language]);
  const target = resolve(root, "plugins", name);
  const example = resolve(root, "plugins", language === "go" ? "example-go" : "example");
  const manifest = JSON.parse(await readFile(resolve(example, "plugin.json"), "utf8"));
  manifest.id = name;
  manifest.name = name;
  manifest.version = "1.0.0";
  manifest.templates = [];
  // Check source templates before creating the destination. mkdir refuses existing packages.
  await access(resolve(example, language === "go" ? "main.go" : "src"));
  await mkdir(target);
  if (language === "go") {
    await cp(resolve(example, "main.go"), resolve(target, "main.go"));
  } else {
    await cp(resolve(example, "src"), resolve(target, "src"), { recursive: true });
    await cp(resolve(example, "tsconfig.json"), resolve(target, "tsconfig.json"));
    const pkg = JSON.parse(await readFile(resolve(example, "package.json"), "utf8"));
    pkg.name = "@catbot/plugin-" + name;
    pkg.version = "1.0.0";
    await writeFile(resolve(target, "package.json"), JSON.stringify(pkg, null, 2) + "\n");
  }
  await writeFile(resolve(target, "plugin.json"), JSON.stringify(manifest, null, 2) + "\n");
  return language === "go"
    ? `Created plugins/${name}. Run sh scripts/build-go-plugins.sh ${name}.`
    : `Created plugins/${name}. Run npm install, then npm run build -w plugins/${name}.`;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    process.stdout.write(await createPlugin(parseArgs(process.argv.slice(2))) + "\n");
  } catch (error) {
    process.stderr.write(String(error.message ?? error) + "\n");
    process.exitCode = 1;
  }
}
