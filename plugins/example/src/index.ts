import { serve } from "@secretary/plugin-sdk";
serve({
  echo: async (args) => ({
    text: String(args.text),
    characters: [...String(args.text)].length,
  }),
  note: async (args, { host, signal }) => {
    const key = "note-" + String(args.name);
    if (args.text !== undefined)
      await host.set(key, { name: args.name, text: args.text }, signal);
    return { note: await host.get(key, signal) };
  },
}).catch((error) => {
  process.stderr.write(String(error) + "\n");
  process.exitCode = 1;
});
