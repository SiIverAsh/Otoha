import { spawnSync } from "node:child_process";

// Keep this entry point for scripts that used the previous frontend build.
const result = spawnSync("go", ["generate", "./web/views"], {
  cwd: new URL("../", import.meta.url),
  stdio: "inherit",
});

if (result.error) {
  console.error(`Could not generate Templ views: ${result.error.message}`);
}
process.exitCode = result.status ?? 1;
