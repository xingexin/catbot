import { serve } from "@secretary/plugin-sdk";
import { parseVideo } from "./parse.js";

serve({
  parse: (args, context) => parseVideo(String(args.artifactId), context),
}).catch((error) => {
  process.stderr.write(String(error) + "\n");
  process.exitCode = 1;
});
