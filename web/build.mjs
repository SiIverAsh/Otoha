import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const srcDir = path.join(__dirname, "src");
const distDir = path.join(__dirname, "dist");

fs.mkdirSync(distDir, { recursive: true });
for (const file of ["index.html", "styles.css", "app.js"]) {
  fs.copyFileSync(path.join(srcDir, file), path.join(distDir, file));
}
console.log("Built web/dist successfully.");
