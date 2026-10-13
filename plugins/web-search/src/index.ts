import { serve } from "@catbot/plugin-sdk";
import { searchWeb } from "./search.js";
import { readPage } from "./read.js";

serve({
  search: (args, context) =>
    searchWeb({ query: args.query, limit: args.limit }, context),
  read: (args, context) =>
    readPage({ url: args.url, maxChars: args.maxChars }, context),
}).catch(() => {
  process.stderr.write("Web search plugin startup failed\n");
  process.exitCode = 1;
});
