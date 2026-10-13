import { serve } from "@catbot/plugin-sdk";
import { summarize } from "./summarize.js";

serve({ summarize }).catch((error) => {
  process.stderr.write(String(error) + "\n");
  process.exitCode = 1;
});
